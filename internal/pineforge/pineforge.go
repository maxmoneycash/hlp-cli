// Package pineforge runs a Pine Script v6 strategy through PineForge as an
// optional, user-installed external engine and reads back its JSON report.
//
// hlp does not bundle, link or download any PineForge component. The user
// installs the pineforge-release container image themselves; hlp only runs
// it as a subprocess (docker run) and parses the report it prints. The image
// contains the Apache-2.0 pineforge-engine runtime and the pineforge-codegen
// transpiler, which is source-available under the PineForge Source License:
// free for noncommercial use and personal trading on one's own account with
// one's own capital, while managing or researching strategies for other
// people's capital, such as a vault with depositors, and any use for a
// company needs a commercial license from PineForge. That licence is between
// the user and PineForge; callers must show LicenseNotice before the first
// run and record the user's acknowledgement (see docs/PINEFORGE.md).
//
// No PineForge code is copied here: the package only speaks the image's
// documented I/O contract (mounted /in/strategy.pine and /in/ohlcv.csv,
// PINEFORGE_* environment variables, JSON report on stdout, exit codes).
package pineforge

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the image hlp suggests. Pin a version: the engine and
// transpiler change their generated code every release.
const DefaultImage = "ghcr.io/pineforge-4pass/pineforge-release:1.12.0"

// EnvImage overrides the image (for example a locally built one).
const EnvImage = "HLP_PINEFORGE_IMAGE"

// LicenseNotice must be shown to the user before the first PineForge run.
const LicenseNotice = `PineForge is an optional external engine that you install and run yourself.
Its Pine-to-C++ transpiler (pineforge-codegen) is source-available under the
PineForge Source License 1.2, not open source. It is free for noncommercial
use and for personal trading: one person backtesting or trading their own
account with their own capital. Backtesting or running strategies for other
people's money, including a vault with depositors, a fund, a prop-firm or
funded account, or for a company, is "Commercial Use" and needs a licence
from PineForge (enterprise@pineforge.dev). hlp does not grant or check this
licence. Terms: https://github.com/pineforge-4pass/pineforge-codegen-oss/blob/main/LICENSE`

// Request describes one backtest.
type Request struct {
	Pine  string // Pine Script v6 source (must start with //@version=6)
	Bars  []Bar  // chart bars, oldest first
	Image string // "" means $HLP_PINEFORGE_IMAGE, then DefaultImage

	// Inputs overrides input.*() values by title; Overrides overrides
	// strategy() header values such as initial_capital or commission_value.
	Inputs    map[string]string
	Overrides map[string]string

	// InputTF and ScriptTF use PineForge's spelling: minutes as a bare
	// integer ("15", "240") or "D", "W". Empty InputTF is detected from
	// the bars.
	InputTF, ScriptTF string

	// Docker is the container CLI ("" means "docker"; "podman" works).
	Docker string
}

// Bar is one OHLCV bar; Time is the bar's open time.
type Bar struct {
	Time                           time.Time
	Open, High, Low, Close, Volume float64
}

// Report is the subset of PineForge's JSON report hlp reads.
type Report struct {
	Engine      string        `json:"engine"`
	Summary     Summary       `json:"summary"`
	Trades      []Trade       `json:"trades"`
	EquityCurve []EquityPoint `json:"equity_curve"`
	// Metrics and Fingerprint are kept raw: their shapes change between
	// releases and hlp only displays or stores them.
	Metrics     json.RawMessage `json:"metrics"`
	Fingerprint json.RawMessage `json:"fingerprint"`
}

// Summary is the report's headline numbers.
type Summary struct {
	TotalTrades   int     `json:"total_trades"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	WinRatePct    float64 `json:"win_rate_pct"`
	NetPnL        float64 `json:"net_pnl"`
	AvgTrade      float64 `json:"avg_trade"`
	BestTrade     float64 `json:"best_trade"`
	WorstTrade    float64 `json:"worst_trade"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	BarsProcessed int     `json:"bars_processed"`
}

// Trade is one closed (or still open) trade from the report.
type Trade struct {
	N           int     `json:"n"`
	Side        string  `json:"side"` // "long" | "short"
	EntryTimeMS int64   `json:"entry_time"`
	ExitTimeMS  int64   `json:"exit_time"`
	EntryPrice  float64 `json:"entry_price"`
	ExitPrice   float64 `json:"exit_price"`
	Qty         float64 `json:"qty"`
	PnL         float64 `json:"pnl"`
	PnLPct      float64 `json:"pnl_pct"`
	MaxRunup    float64 `json:"max_runup"`
	MaxDrawdown float64 `json:"max_drawdown"`
	Commission  float64 `json:"commission"`
	OpenAtEnd   bool    `json:"open_at_end"`
}

// EquityPoint is one point of the equity curve.
type EquityPoint struct {
	TimeMS     int64   `json:"time_ms"`
	Equity     float64 `json:"equity"`
	OpenProfit float64 `json:"open_profit"`
}

// Error is a failed run, classified by the image's exit code.
type Error struct {
	Code   int    // process exit code
	Kind   string // "missing-input" | "compile" | "backtest" | "transpile" | "unknown"
	Msg    string // the engine's error message, when it printed one
	Stderr string // last part of stderr, for diagnostics
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("pineforge %s failed (exit %d): %s", e.Kind, e.Code, e.Msg)
	}
	return fmt.Sprintf("pineforge %s failed (exit %d)", e.Kind, e.Code)
}

// ErrNotInstalled means the container CLI is not on PATH.
var ErrNotInstalled = errors.New("pineforge: container runtime not found; install Docker (or set Request.Docker) and pull " + DefaultImage)

func kindOf(code int) string {
	switch code {
	case 2:
		return "missing-input"
	case 3:
		return "compile"
	case 4:
		return "backtest"
	case 5:
		return "transpile"
	}
	return "unknown"
}

// image resolves the image a request will run.
func (r Request) image() string {
	if r.Image != "" {
		return r.Image
	}
	if v := os.Getenv(EnvImage); v != "" {
		return v
	}
	return DefaultImage
}

// Args returns the container CLI arguments for a run whose inputs are in
// dir (strategy.pine and ohlcv.csv). Exposed for tests and --dry-run.
func (r Request) Args(dir string) ([]string, error) {
	// The image is the last argument; a value starting with "-" (from
	// $HLP_PINEFORGE_IMAGE or config) would be parsed as a docker flag.
	if img := r.image(); strings.HasPrefix(img, "-") || strings.ContainsAny(img, " \t\r\n") {
		return nil, fmt.Errorf("pineforge: invalid image reference %q", img)
	}
	// --pull=never: hlp never downloads the image. The container gets no
	// network, no capabilities and read-only inputs. The script is
	// untrusted (it is transpiled to C++ and compiled), so memory, swap,
	// processes and CPU are capped and /tmp is a size-limited tmpfs.
	// (--read-only and a non-root --user are left out until they are
	// verified against the release image's compile scratch path.)
	args := []string{"run", "--rm", "--pull=never", "--network=none",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--memory=2g", "--memory-swap=2g", "--pids-limit=256", "--cpus=2",
		"--tmpfs", "/tmp:rw,size=512m",
		"-v", filepath.Join(dir, "strategy.pine") + ":/in/strategy.pine:ro",
		"-v", filepath.Join(dir, "ohlcv.csv") + ":/in/ohlcv.csv:ro",
	}
	env := func(k, v string) { args = append(args, "-e", k+"="+v) }
	if len(r.Inputs) > 0 {
		b, err := json.Marshal(r.Inputs)
		if err != nil {
			return nil, err
		}
		env("PINEFORGE_INPUTS", string(b))
	}
	if len(r.Overrides) > 0 {
		b, err := json.Marshal(r.Overrides)
		if err != nil {
			return nil, err
		}
		env("PINEFORGE_OVERRIDES", string(b))
	}
	if r.InputTF != "" {
		env("PINEFORGE_INPUT_TF", r.InputTF)
	}
	if r.ScriptTF != "" {
		env("PINEFORGE_SCRIPT_TF", r.ScriptTF)
	}
	return append(args, r.image()), nil
}

// WriteOHLCV writes bars in the image's CSV format: a
// timestamp,open,high,low,close,volume header, Unix-millisecond timestamps.
func WriteOHLCV(w io.Writer, bars []Bar) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"timestamp", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	for _, b := range bars {
		if err := cw.Write([]string{strconv.FormatInt(b.Time.UnixMilli(), 10), f(b.Open), f(b.High), f(b.Low), f(b.Close), f(b.Volume)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ParseReport decodes a report printed on stdout. A structured engine error
// ({"engine":"pineforge","error":"..."}) is returned as *Error.
func ParseReport(b []byte) (*Report, error) {
	var probe struct {
		Engine string `json:"engine"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("pineforge: report is not JSON: %w", err)
	}
	if probe.Error != "" {
		return nil, &Error{Code: 4, Kind: "backtest", Msg: probe.Error}
	}
	if probe.Engine != "pineforge" {
		return nil, fmt.Errorf("pineforge: unexpected report engine %q", probe.Engine)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("pineforge: report: %w", err)
	}
	return &r, nil
}

// run backtests req in a fresh container with the default limits
// (DefaultTimeout, DefaultMaxOutput) and returns the report. It skips the
// licence and vault gates, so it is unexported: callers outside this
// package go through Detect and then (*Engine).Execute or Backtest, which
// enforce LicenseAccepted and, for Options.Vault, CommercialLicense.
func run(ctx context.Context, req Request) (*Report, error) {
	docker := req.Docker
	if docker == "" {
		docker = "docker"
	}
	if _, err := exec.LookPath(docker); err != nil {
		return nil, ErrNotInstalled
	}
	e := &Engine{Docker: docker, Image: req.image()}
	return e.runRequest(ctx, req)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
