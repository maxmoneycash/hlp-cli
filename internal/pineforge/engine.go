package pineforge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/pinesrc"
)

// This file is the engine adapter: the same shape as internal/pyne
// (Detect / Available / Engine.Execute / Engine.Backtest) so the CLI can
// offer --engine builtin|pyne|pineforge|auto with one switch. PineForge is
// never picked by "auto"; see docs/PINEFORGE.md.

// EnvDocker overrides the container CLI ("docker", "podman" or a path).
const EnvDocker = "HLP_PINEFORGE_DOCKER"

// Limits.
const (
	// DefaultTimeout bounds one run. A new script is transpiled and
	// compiled with g++ inside the container (5 to 10 s) before it runs.
	DefaultTimeout = 5 * time.Minute
	// DefaultMaxOutput caps the report read from stdout. A 222k-bar run
	// with 5k trades prints about 5 MB, mostly the equity curve.
	DefaultMaxOutput = 128 << 20
	MaxSourceBytes   = 1 << 20
	maxStderr        = 64 << 10
	inspectTimeout   = 20 * time.Second
)

// ErrImageMissing means the container CLI works but the image is not
// present locally. hlp never pulls it (runs use --pull=never).
var ErrImageMissing = errors.New("pineforge: image not found locally")

// ErrLicenseNotAccepted means Execute was called before the user accepted
// LicenseNotice (Engine.LicenseAccepted).
var ErrLicenseNotAccepted = errors.New("pineforge: the PineForge licence notice has not been accepted (show pineforge.LicenseNotice, then set Engine.LicenseAccepted)")

// ErrCommercialUse means a vault (other people's capital) run was asked for
// without the user confirming a PineForge commercial licence.
var ErrCommercialUse = errors.New("pineforge: vault use is Commercial Use under the PineForge Source License; confirm you hold a PineForge commercial licence (Engine.CommercialLicense) or use --engine builtin|pyne")

// ErrOutputTooLarge means the report exceeded Engine.MaxOutput.
var ErrOutputTooLarge = errors.New("pineforge: report exceeded the size limit")

// TimeoutError means hlp stopped the run after After.
type TimeoutError struct{ After time.Duration }

func (e *TimeoutError) Error() string { return fmt.Sprintf("pineforge: timed out after %s", e.After) }

// Engine is one container CLI plus one locally present image.
type Engine struct {
	Docker  string // container CLI path
	Image   string // image reference
	ImageID string // from `image inspect` (Detect fills it)
	Version string // image tag, e.g. "1.12.0" ("" when untagged)

	Timeout   time.Duration // default DefaultTimeout
	MaxOutput int64         // default DefaultMaxOutput

	// LicenseAccepted must be set after the user has seen LicenseNotice
	// and accepted it (flag or config). Execute refuses otherwise.
	LicenseAccepted bool
	// CommercialLicense records the user's statement that they hold a
	// PineForge commercial licence; Options.Vault runs require it.
	CommercialLicense bool
}

// Detect finds the container CLI ($HLP_PINEFORGE_DOCKER, else docker, else
// podman) and checks the image ($HLP_PINEFORGE_IMAGE, else DefaultImage) is
// present locally. It never pulls. Errors: ErrNotInstalled, ErrImageMissing
// (with the pull command), or the CLI's own error (daemon not running).
func Detect(ctx context.Context) (*Engine, error) {
	docker, err := findDocker()
	if err != nil {
		return nil, err
	}
	img := Request{}.image()
	e := &Engine{Docker: docker, Image: img, Version: tagOf(img)}
	ictx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ictx, docker, "image", "inspect", "--format", "{{.Id}}", img)
	cmd.WaitDelay = 2 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout = &cappedWriter{w: &out, max: 4 << 10}
	cmd.Stderr = &cappedWriter{w: &stderr, max: 4 << 10}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ictx.Err() != nil {
			return nil, fmt.Errorf("pineforge: %s image inspect timed out (is the container daemon running?)", docker)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "no such image") || strings.Contains(low, "image not known") || strings.Contains(low, "no such object") {
			return nil, fmt.Errorf("%w: %s (pull it yourself with `%s pull %s`)", ErrImageMissing, img, filepath.Base(docker), img)
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("pineforge: %s image inspect %s: %s", docker, img, firstLine(msg))
	}
	e.ImageID = strings.TrimSpace(out.String())
	return e, nil
}

// Available reports whether Detect would succeed. Callers must still not
// pick PineForge under --engine auto.
func Available(ctx context.Context) bool {
	_, err := Detect(ctx)
	return err == nil
}

func findDocker() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvDocker)); v != "" {
		p, err := exec.LookPath(v)
		if err != nil {
			return "", fmt.Errorf("%w (%s=%q)", ErrNotInstalled, EnvDocker, v)
		}
		return p, nil
	}
	for _, name := range []string{"docker", "podman"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNotInstalled
}

func tagOf(img string) string {
	if i := strings.LastIndexByte(img, '@'); i >= 0 {
		img = img[:i]
	}
	i := strings.LastIndexByte(img, ':')
	if i < 0 || strings.Contains(img[i:], "/") {
		return ""
	}
	return img[i+1:]
}

// Options for Execute.
type Options struct {
	// Inputs override input.*() values, keyed by input title or by the
	// variable the input is assigned to (case-insensitive), like hlp's
	// builtin engine and internal/pyne. PineForge keys by title.
	Inputs map[string]any
	// Overrides set strategy() header values (initial_capital,
	// commission_value, ...), passed as PINEFORGE_OVERRIDES.
	Overrides map[string]string
	// Interval is the candles' interval; it becomes PINEFORGE_INPUT_TF.
	// Empty lets the image detect it from the bars.
	Interval domain.Interval
	// ScriptInterval runs the script on a coarser chart than the candles
	// (PINEFORGE_SCRIPT_TF); empty = the candles' interval.
	ScriptInterval domain.Interval
	// Vault marks a run for a strategy vault (other people's capital).
	// It needs Engine.CommercialLicense.
	Vault bool
}

// Result is a typed PineForge run, shaped like pyne.Result.
type Result struct {
	Trades  []Trade       // in report order; OpenAtEnd marks a still-open trade (ExitPrice = last mark)
	Equity  domain.Series // T = bar time as reported, V = equity
	Stats   Stats
	Bars    int
	Elapsed time.Duration
	Image   string
	Report  *Report // the raw report (metrics, fingerprint)

	Warnings []string
}

// Stats summarises the run (the report's summary block).
type Stats struct {
	InitialCapital float64 // first equity point
	FinalEquity    float64
	NetProfit      float64 // closed trades, net of commission
	ClosedTrades   int
	WinTrades      int
	Commission     float64
	MaxDrawdown    float64
}

// Long reports whether the trade is a long.
func (t Trade) Long() bool { return t.Side != "short" }

// Execute backtests src over candles (oldest first) with PineForge's own
// broker (the script's strategy() settings: next-bar-open market fills,
// its commission and slippage, no funding).
func (e *Engine) Execute(ctx context.Context, src string, candles []domain.Candle, o Options) (*Result, error) {
	if !e.LicenseAccepted {
		return nil, ErrLicenseNotAccepted
	}
	if o.Vault && !e.CommercialLicense {
		return nil, ErrCommercialUse
	}
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("pineforge: script is %d bytes, limit %d", len(src), MaxSourceBytes)
	}
	if !strings.Contains(src, "//@version=6") {
		return nil, errors.New("pineforge: only Pine Script v6 is supported (the script must start with //@version=6)")
	}
	bars, err := toBars(candles)
	if err != nil {
		return nil, err
	}
	inputs, unknown := resolveInputs(src, o.Inputs)
	req := Request{Pine: src, Bars: bars, Image: e.Image, Docker: e.Docker, Inputs: inputs, Overrides: o.Overrides,
		InputTF: tf(o.Interval), ScriptTF: tf(o.ScriptInterval)}
	start := time.Now()
	rep, err := e.runRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	res := &Result{Report: rep, Bars: rep.Summary.BarsProcessed, Elapsed: time.Since(start), Image: e.Image}
	for _, k := range unknown {
		res.Warnings = append(res.Warnings, fmt.Sprintf("input %q matches no input title or variable; passed through as is", k))
	}
	res.Trades = rep.Trades
	for _, p := range rep.EquityCurve {
		res.Equity = append(res.Equity, domain.Point{T: p.TimeMS, V: p.Equity})
	}
	s := rep.Summary
	res.Stats = Stats{WinTrades: s.Wins, MaxDrawdown: s.MaxDrawdown}
	for _, t := range rep.Trades {
		res.Stats.Commission += t.Commission
		if !t.OpenAtEnd {
			res.Stats.ClosedTrades++
			res.Stats.NetProfit += t.PnL
		}
	}
	if len(res.Equity) > 0 {
		res.Stats.InitialCapital = res.Equity[0].V
		res.Stats.FinalEquity = res.Equity[len(res.Equity)-1].V
	}
	return res, nil
}

// runRequest writes the inputs, runs the container and parses the report.
func (e *Engine) runRequest(ctx context.Context, req Request) (*Report, error) {
	if len(req.Bars) == 0 {
		return nil, errors.New("pineforge: no bars")
	}
	if req.Image == "" {
		req.Image = e.Image
	}
	dir, err := os.MkdirTemp("", "hlp-pineforge-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "strategy.pine"), []byte(req.Pine), 0o644); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, "ohlcv.csv"))
	if err != nil {
		return nil, err
	}
	if err := WriteOHLCV(f, req.Bars); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	args, err := req.Args(dir)
	if err != nil {
		return nil, err
	}
	name := "hlp-pineforge-" + randHex(6)
	args = append([]string{args[0], "--name", name}, args[1:]...)
	stdout, stderr, code, err := e.exec(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		pe := &Error{Code: code, Kind: kindOf(code), Stderr: tail(stderr, 2000)}
		if _, perr := ParseReport(stdout); perr != nil {
			var x *Error
			if errors.As(perr, &x) {
				pe.Msg = x.Msg
			}
		}
		if code == 125 && pe.Msg == "" { // docker's own failure (e.g. image missing with --pull=never)
			pe.Kind = "container"
			pe.Msg = firstLine(stderr)
		}
		return nil, pe
	}
	return ParseReport(stdout)
}

// exec runs the container CLI with no shell, a timeout and an output cap.
// On timeout or cancellation it also removes the named container, because
// killing the docker client does not stop the container.
func (e *Engine) exec(ctx context.Context, name string, args []string) (stdout []byte, stderr string, code int, err error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOut := e.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, e.Docker, args...)
	cmd.WaitDelay = 3 * time.Second
	var out, errb bytes.Buffer
	ow := &cappedWriter{w: &out, max: maxOut}
	cmd.Stdout = ow
	cmd.Stderr = &cappedWriter{w: &errb, max: maxStderr}
	runErr := cmd.Run()
	stderr = strings.TrimSpace(errb.String())
	if cerr := rctx.Err(); cerr != nil {
		e.removeContainer(name)
		if errors.Is(cerr, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, stderr, 0, &TimeoutError{After: timeout}
		}
		return nil, stderr, 0, ctx.Err()
	}
	if ow.overflow {
		e.removeContainer(name)
		return nil, stderr, 0, ErrOutputTooLarge
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return out.Bytes(), stderr, ee.ExitCode(), nil
		}
		return nil, stderr, 0, fmt.Errorf("pineforge: start %s: %w", e.Docker, runErr)
	}
	return out.Bytes(), stderr, 0, nil
}

func (e *Engine) removeContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Docker, "rm", "-f", name)
	cmd.WaitDelay = 2 * time.Second
	_ = cmd.Run()
}

// cappedWriter keeps at most max bytes and remembers overflow.
type cappedWriter struct {
	w        *bytes.Buffer
	max      int64
	overflow bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if room := c.max - int64(c.w.Len()); int64(len(p)) > room {
		if room > 0 {
			c.w.Write(p[:room])
		}
		c.overflow = true
		return len(p), nil
	}
	return c.w.Write(p)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return tail(s, 300)
}

func toBars(cs []domain.Candle) ([]Bar, error) {
	if len(cs) == 0 {
		return nil, errors.New("pineforge: no candles")
	}
	out := make([]Bar, len(cs))
	var prev int64
	for i, c := range cs {
		for _, v := range []float64{c.O, c.H, c.L, c.C, c.V} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("pineforge: candle %d (t=%d) has a non-finite value", i, c.T)
			}
		}
		if i > 0 && c.T <= prev {
			return nil, fmt.Errorf("pineforge: candles must be strictly time-ordered (index %d)", i)
		}
		prev = c.T
		out[i] = Bar{Time: time.UnixMilli(c.T), Open: c.O, High: c.H, Low: c.L, Close: c.C, Volume: c.V}
	}
	return out, nil
}

// tf maps an hlp interval to PineForge's timeframe spelling: minutes as a
// bare integer, or D / W / M.
func tf(iv domain.Interval) string {
	switch iv {
	case "":
		return ""
	case domain.I1d:
		return "D"
	case domain.I1w:
		return "W"
	case domain.I1M:
		return "M"
	case domain.I3d:
		return "3D"
	}
	if d := iv.Duration(); d > 0 {
		return strconv.Itoa(int(d / time.Minute))
	}
	return ""
}

// resolveInputs maps keys given as input titles or variable names (any
// case) to PineForge's title keys and stringifies the values. Unmatched
// keys pass through and are reported.
func resolveInputs(src string, in map[string]any) (map[string]string, []string) {
	if len(in) == 0 {
		return nil, nil
	}
	byKey := pinesrc.InputKeys(src)
	out := map[string]string{}
	var unknown []string
	for k, v := range in {
		s := fmt.Sprint(v)
		if f, ok := v.(float64); ok {
			s = strconv.FormatFloat(f, 'g', -1, 64)
		}
		if t, ok := byKey[strings.ToLower(k)]; ok {
			out[t] = s
			continue
		}
		out[k] = s
		unknown = append(unknown, k)
	}
	return out, unknown
}

var titleRE = regexp.MustCompile(`(?m)^strategy\s*\(\s*(?:title\s*=\s*)?"((?:[^"\\]|\\.)*)"`)
