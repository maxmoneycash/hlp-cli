package pyne

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvBin overrides the pyne executable (absolute path). Empty = look up
// "pyne" on PATH.
const EnvBin = "HLP_PYNE_BIN"

// MinVersion is the oldest hoox-pyne whose `execute --ohlcv` / `diagnostics`
// contract this package was written against.
const MinVersion = "0.6.0"

// InstallHint is shown when PYNE is missing.
const InstallHint = "install it separately with `uv tool install hoox-pyne` (AGPL-3.0, optional) or set " + EnvBin

// Limits.
const (
	MaxBars         = 5000    // PYNE's execute cap
	MaxSourceBytes  = 1 << 20 // 1 MiB of Pine
	DefaultTimeout  = 60 * time.Second
	DefaultMaxOut   = 64 << 20 // stdout cap
	versionTimeout  = 20 * time.Second
	maxStderrReport = 2 << 10
)

// ErrNotInstalled means no usable pyne binary was found.
var ErrNotInstalled = errors.New("pyne: not installed; " + InstallHint)

// ErrOutputTooLarge means pyne printed more than Engine.MaxOutput bytes.
var ErrOutputTooLarge = errors.New("pyne: output exceeded the size limit")

// Engine runs one pyne binary.
type Engine struct {
	Bin     string // absolute path of the pyne executable
	Version string // e.g. "0.7.0"

	// Timeout bounds every pyne call (default DefaultTimeout). PYNE's own
	// --timeout is only checked between bars, so hlp kills the process.
	Timeout time.Duration
	// MaxOutput caps stdout bytes (default DefaultMaxOut).
	MaxOutput int64
	// Mode is the PYNE runtime host: interpret (default) | auto | compile.
	Mode string

	// rawHook sees every call's argv and stdout (tests record fixtures).
	rawHook func(args []string, stdout []byte)
}

// Detect finds pyne via HLP_PYNE_BIN or PATH and checks its version.
// It returns ErrNotInstalled (wrapped) when there is none.
func Detect(ctx context.Context) (*Engine, error) {
	bin := strings.TrimSpace(os.Getenv(EnvBin))
	if bin != "" {
		st, err := os.Stat(bin)
		if err != nil || st.IsDir() {
			return nil, fmt.Errorf("%w (%s=%q: not a file)", ErrNotInstalled, EnvBin, bin)
		}
	} else {
		p, err := exec.LookPath("pyne")
		if err != nil {
			return nil, ErrNotInstalled
		}
		bin = p
	}
	e := &Engine{Bin: bin}
	vctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, _, err := e.run(vctx, nil, "--version")
	if err != nil {
		return nil, fmt.Errorf("pyne: %s --version: %w", bin, err)
	}
	v := versionRE.FindString(string(out))
	if v == "" {
		return nil, fmt.Errorf("pyne: %s --version printed %q, expected a version number", bin, firstLine(string(out)))
	}
	if compareVersions(v, MinVersion) < 0 {
		return nil, fmt.Errorf("pyne: version %s is older than %s; upgrade with `uv tool upgrade hoox-pyne`", v, MinVersion)
	}
	e.Version = v
	return e, nil
}

// Available reports whether a usable pyne exists (for --engine auto). It
// shares Cached's result, so `--engine auto` followed by the run spawns
// `pyne --version` once per process.
func Available(ctx context.Context) bool {
	_, err := Cached(ctx)
	return err == nil
}

var (
	detectMu    sync.Mutex
	detectCache = map[string]*Engine{} // resolved binary path -> engine
)

// Cached is Detect with a per-process cache of successful detections,
// keyed by the binary path (HLP_PYNE_BIN or PATH lookup). Failures are not
// cached. Each call returns a fresh copy, so callers may set Timeout/Mode.
func Cached(ctx context.Context) (*Engine, error) {
	bin := strings.TrimSpace(os.Getenv(EnvBin))
	if bin == "" {
		bin, _ = exec.LookPath("pyne")
	}
	detectMu.Lock()
	defer detectMu.Unlock()
	if e, ok := detectCache[bin]; ok && bin != "" {
		c := *e
		return &c, nil
	}
	e, err := Detect(ctx)
	if err != nil {
		return nil, err
	}
	detectCache[e.Bin] = e
	c := *e
	return &c, nil
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// cappedBuffer keeps at most max bytes and remembers overflow.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int64
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - int64(c.buf.Len()); int64(len(p)) > room {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.overflow = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// ExitError is a non-zero pyne exit with its stderr tail.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("pyne exited with status %d", e.Code)
	}
	return fmt.Sprintf("pyne exited with status %d: %s", e.Code, e.Stderr)
}

// run executes pyne with args (no shell). It returns stdout even on a
// non-zero exit (PYNE prints its JSON envelope and exits 1 on script
// errors); err is *ExitError then.
func (e *Engine) run(ctx context.Context, stdin []byte, args ...string) (stdout []byte, stderr string, err error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOut := e.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxOut
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	cmd.Env = append(childEnv(os.Environ()), "NO_COLOR=1", "PYTHONIOENCODING=utf-8", "PYTHONUNBUFFERED=1")
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &cappedBuffer{max: maxOut}
	errBuf := &cappedBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, errBuf
	runErr := cmd.Run()
	if e.rawHook != nil {
		e.rawHook(args, out.buf.Bytes())
	}
	stderr = tail(strings.TrimSpace(errBuf.buf.String()), maxStderrReport)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return nil, stderr, &TimeoutError{After: timeout}
		}
		return nil, stderr, ctxErr
	}
	if out.overflow {
		return nil, stderr, ErrOutputTooLarge
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return out.buf.Bytes(), stderr, &ExitError{Code: ee.ExitCode(), Stderr: stderr}
		}
		return nil, stderr, fmt.Errorf("pyne: start %s: %w", e.Bin, runErr)
	}
	return out.buf.Bytes(), stderr, nil
}

// secretEnvRE matches environment variable names that look like
// credentials. PYNE is third-party code with an HTTP client; it needs none
// of them to evaluate a script, so they are not passed to it.
var secretEnvRE = regexp.MustCompile(`(?i)(KEY|SECRET|TOKEN|PASSWORD|PASSWD|PRIVATE|CREDENTIAL|MNEMONIC|SEED|AUTH|COOKIE|SESSION)`)

// childEnv is env minus credential-looking variables.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if secretEnvRE.MatchString(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// TimeoutError means hlp killed pyne after After.
type TimeoutError struct{ After time.Duration }

func (e *TimeoutError) Error() string { return fmt.Sprintf("pyne: timed out after %s", e.After) }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return tail(s, 200)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
