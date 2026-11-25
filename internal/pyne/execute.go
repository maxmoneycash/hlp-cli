package pyne

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/pinesrc"
)

// Options for Execute.
type Options struct {
	// Inputs override input.* values, keyed by input title or by the
	// variable the input is assigned to (case-insensitive), like hlp's
	// builtin engine. PYNE itself keys by title; Execute translates.
	Inputs map[string]any
	// Ticker / Interval set syminfo.ticker and timeframe.period.
	Ticker   string
	Interval domain.Interval
	// Sample is how many trailing values each plot carries (default 8,
	// max 64; PYNE never returns full plot series on this path).
	Sample int
	// NoInstrument skips the trade/equity export tail (strategy scripts
	// then return only plots, events and stats-less results).
	NoInstrument bool
	// Events asks for PYNE's raw strategy events (--trades).
	Events bool
}

// Result is a typed PYNE run.
type Result struct {
	Script  ScriptInfo
	Bars    int
	Mode    string // interpret | compile (the host that actually ran)
	Elapsed time.Duration

	// Plots by title, trailing values only.
	Plots map[string]Plot
	// Strategy results (strategy scripts with instrumentation).
	Trades   []Trade
	Open     *OpenPosition // position still open at the last bar
	Equity   domain.Series // per bar, T = bar open time
	Position domain.Series // signed base size after each bar's fills
	Stats    Stats

	Events []Event // raw strategy events (Options.Events)
	Alerts []Alert
	Logs   []string // the script's own log.* lines ("level: message")

	Instrumented bool
	Warnings     []string
}

// ScriptInfo is the declaration PYNE saw.
type ScriptInfo struct {
	Name    string
	Type    string // strategy | indicator | library
	Overlay bool
	Inputs  []InputInfo
}

// InputInfo is one input.* as PYNE resolved it.
type InputInfo struct {
	Title   string
	Type    string
	Default any
	Value   any
}

// Plot is the tail of one plot series.
type Plot struct {
	Title   string
	Kind    string // plot | plotshape | plotchar | hline | ...
	Len     int
	NaCount int
	Last    float64   // NaN when na
	Tail    []float64 // trailing values, oldest first; NaN = na
}

// Trade is one closed trade.
type Trade struct {
	EntryID, ExitID     string
	Long                bool
	EntryBar, ExitBar   int
	EntryTime, ExitTime int64 // ms
	EntryPx, ExitPx     float64
	Size                float64 // base units, always positive
	Profit              float64 // USD, net of commission
	Commission          float64
}

// OpenPosition is the position left at the last bar.
type OpenPosition struct {
	Size     float64 // signed
	AvgPx    float64
	OpenPnL  float64
	MarkTime int64
}

// Stats summarises a strategy run (PYNE's strategy.* values on the last bar).
type Stats struct {
	InitialCapital float64
	FinalEquity    float64
	NetProfit      float64 // closed trades
	OpenProfit     float64
	ClosedTrades   int
	WinTrades      int
	Commission     float64 // closed trades
	OpenCommission float64 // entry commission of the open trade(s)
}

// Event is a PYNE strategy event (see PYNE docs "Strategy events").
type Event struct {
	Kind      string   `json:"kind"` // entry | exit | close | close_all | cancel | cancel_all | order
	ID        string   `json:"id"`
	Direction string   `json:"direction"`
	Qty       *float64 `json:"qty"`
	OrderType string   `json:"order_type"`
	Limit     *float64 `json:"limit"`
	Stop      *float64 `json:"stop"`
	OCAName   string   `json:"oca_name"`
	Comment   string   `json:"comment"`
	BarIndex  int      `json:"bar_index"`
	BarTime   int64    `json:"bar_time"`
}

// Alert is an alert() / alertcondition() firing.
type Alert struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Source   string `json:"source"`
	Freq     string `json:"freq"`
	BarIndex int    `json:"bar_index"`
	Time     int64  `json:"time"`
}

// ---- wire ----

type wireBar struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type wireLog struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type wireEnvelope struct {
	OK        bool    `json:"ok"`
	Error     string  `json:"error"`
	Kind      string  `json:"kind"`
	Bars      int     `json:"bars"`
	Mode      string  `json:"mode"`
	Backend   *string `json:"auto_backend"`
	TimedOut  bool    `json:"timed_out"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Plots     map[string]struct {
		Len     int        `json:"len"`
		Last    *float64   `json:"last"`
		NaCount int        `json:"na_count"`
		Sample  []*float64 `json:"sample"`
	} `json:"plots"`
	Extras struct {
		PlotMeta map[string]struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
			Index int    `json:"index"`
		} `json:"plot_meta"`
		Events []Event   `json:"events"`
		Alerts []Alert   `json:"alerts"`
		Logs   []wireLog `json:"logs"`
		Meta   struct {
			ScriptName string    `json:"script_name"`
			ScriptType string    `json:"script_type"`
			Overlay    bool      `json:"overlay"`
			Logs       []wireLog `json:"logs"`
			Inputs     []struct {
				Type    string `json:"type"`
				Default any    `json:"default"`
				Value   any    `json:"value"`
				Title   string `json:"title"`
			} `json:"inputs"`
		} `json:"meta"`
		ErrorKind string `json:"error_kind"`
		ErrorType string `json:"error_type"`
	} `json:"extras"`
}

// Execute runs src over candles (oldest first) and returns typed results.
// Script problems come back as *ScriptError (with line:col diagnostics for
// parse errors); a killed run as *TimeoutError.
func (e *Engine) Execute(ctx context.Context, src string, candles []domain.Candle, o Options) (*Result, error) {
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("pyne: script is %d bytes, limit %d", len(src), MaxSourceBytes)
	}
	if len(candles) == 0 {
		return nil, errors.New("pyne: no candles")
	}
	if len(candles) > MaxBars {
		return nil, fmt.Errorf("pyne: %d candles, PYNE's limit is %d (pass the most recent %d)", len(candles), MaxBars, MaxBars)
	}
	bars, err := toWire(candles)
	if err != nil {
		return nil, err
	}
	inputs, unknown := resolveInputs(src, o.Inputs)
	dir, err := os.MkdirTemp("", "hlp-pyne-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	barsPath := filepath.Join(dir, "bars.json")
	if err := writeJSON(barsPath, bars); err != nil {
		return nil, err
	}
	inputsPath := filepath.Join(dir, "inputs.json")
	if err := writeJSON(inputsPath, inputs); err != nil {
		return nil, err
	}

	instrumented := !o.NoInstrument && isStrategy(src)
	run := func(code string) (*wireEnvelope, error) {
		path := filepath.Join(dir, "script.pine")
		if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
			return nil, err
		}
		args := e.executeArgs(path, barsPath, inputsPath, o)
		out, _, err := e.run(ctx, nil, args...)
		var ee *ExitError
		if err != nil && !errors.As(err, &ee) {
			return nil, err
		}
		var env wireEnvelope
		if jerr := json.Unmarshal(out, &env); jerr != nil {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("pyne: execute: unexpected output %q", firstLine(string(out)))
		}
		return &env, nil
	}

	var warnings []string
	for _, k := range unknown {
		warnings = append(warnings, fmt.Sprintf("input %q matches no input title or variable; passed through as is", k))
	}
	nonce := newNonce()
	code := src
	if instrumented {
		code = instrument(src, nonce)
	}
	env, err := run(code)
	if err != nil {
		return nil, err
	}
	if !env.OK && instrumented {
		// Re-run the untouched script: its error positions are the user's,
		// and if it succeeds the export tail was the problem.
		plain, perr := run(src)
		if perr != nil {
			return nil, perr
		}
		if plain.OK {
			warnings = append(warnings, "PYNE rejected hlp's trade-export tail ("+firstLine(env.Error)+"); trades and equity are unavailable")
			instrumented = false
		}
		env = plain
	}
	if !env.OK {
		return nil, e.scriptError(ctx, src, env)
	}
	res := decode(env, instrumented, nonce)
	res.Warnings = append(warnings, res.Warnings...)
	return res, nil
}

func (e *Engine) executeArgs(script, bars, inputs string, o Options) []string {
	mode := e.Mode
	if mode == "" {
		mode = "interpret"
	}
	sample := o.Sample
	if sample <= 0 {
		sample = 8
	}
	sample = min(sample, 64)
	ticker := o.Ticker
	if ticker == "" {
		ticker = "HLP"
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// PYNE caps its own timeout at 120 s; hlp's process timeout is the
	// real bound.
	inner := math.Min(timeout.Seconds(), 120)
	args := []string{"execute", script,
		"--mode", mode,
		"--ohlcv", bars,
		"--inputs", inputs,
		// "--opt=value" so a value can never be read as another flag.
		"--ticker=" + ticker,
		"--timeframe=" + TVTimeframe(o.Interval),
		"--sample", strconv.Itoa(sample),
		"--timeout", strconv.FormatFloat(inner, 'f', 0, 64),
	}
	if o.Events {
		args = append(args, "--trades")
	}
	return args
}

var lineRE = regexp.MustCompile(`line (\d+)`)

func (e *Engine) scriptError(ctx context.Context, src string, env *wireEnvelope) error {
	se := &ScriptError{Kind: env.Kind, Msg: env.Error}
	if env.TimedOut || strings.Contains(strings.ToLower(env.Error), "timed out") {
		se.Kind = "timeout"
	}
	if se.Kind == "" {
		se.Kind = "runtime"
	}
	if se.Kind == "parse" {
		if ds, err := e.Check(ctx, src); err == nil && len(Errors(ds)) > 0 {
			se.Diagnostics = Errors(ds)
			return se
		}
		if m := lineRE.FindStringSubmatch(env.Error); m != nil {
			n, _ := strconv.Atoi(m[1])
			se.Diagnostics = []Diagnostic{{Line: n, Col: 1, Severity: "error", Msg: firstLine(env.Error)}}
		}
	}
	return se
}

func decode(env *wireEnvelope, instrumented bool, nonce string) *Result {
	pfx := newPrefixes(nonce)
	r := &Result{
		Bars: env.Bars, Mode: env.Mode, Elapsed: time.Duration(env.ElapsedMS * float64(time.Millisecond)),
		Plots: map[string]Plot{}, Events: env.Extras.Events, Alerts: env.Extras.Alerts,
		Instrumented: instrumented,
	}
	if env.Backend != nil && *env.Backend != "" {
		r.Mode = *env.Backend
	}
	m := env.Extras.Meta
	r.Script = ScriptInfo{Name: m.ScriptName, Type: m.ScriptType, Overlay: m.Overlay}
	for _, in := range m.Inputs {
		r.Script.Inputs = append(r.Script.Inputs, InputInfo{Title: in.Title, Type: in.Type, Default: in.Default, Value: in.Value})
	}
	for title, p := range env.Plots {
		pl := Plot{Title: title, Len: p.Len, NaCount: p.NaCount, Last: f64(p.Last), Kind: env.Extras.PlotMeta[title].Kind}
		for _, v := range p.Sample {
			pl.Tail = append(pl.Tail, f64(v))
		}
		r.Plots[title] = pl
	}
	logs := m.Logs
	if len(logs) == 0 {
		logs = env.Extras.Logs
	}
	var last barRow
	haveBar := false
	for _, l := range logs {
		switch {
		case instrumented && strings.HasPrefix(l.Message, pfx.bar):
			b, ok := parseBar(strings.TrimPrefix(l.Message, pfx.bar))
			if !ok {
				continue
			}
			r.Equity = append(r.Equity, domain.Point{T: b.t, V: b.equity})
			r.Position = append(r.Position, domain.Point{T: b.t, V: b.pos})
			last, haveBar = b, true
		case instrumented && strings.HasPrefix(l.Message, pfx.trade):
			if t, ok := parseTrade(strings.TrimPrefix(l.Message, pfx.trade)); ok {
				r.Trades = append(r.Trades, t)
			}
		default:
			r.Logs = append(r.Logs, l.Level+": "+l.Message)
		}
	}
	if !haveBar {
		return r
	}
	for i := range r.Trades {
		t := &r.Trades[i]
		t.Long = direction(r.Position, t)
		r.Stats.Commission += t.Commission
	}
	r.Stats.OpenCommission = last.openComm
	r.Stats.FinalEquity = last.equity
	r.Stats.NetProfit = last.net
	r.Stats.OpenProfit = last.open
	r.Stats.ClosedTrades = int(last.closed)
	r.Stats.WinTrades = int(last.wins)
	r.Stats.InitialCapital = last.equity - last.net - last.open
	if last.pos != 0 && !math.IsNaN(last.pos) {
		r.Open = &OpenPosition{Size: last.pos, AvgPx: last.avgPx, OpenPnL: last.open, MarkTime: last.t}
	}
	if len(r.Trades) != r.Stats.ClosedTrades {
		r.Warnings = append(r.Warnings, fmt.Sprintf("exported %d trades but strategy.closedtrades = %d", len(r.Trades), r.Stats.ClosedTrades))
	}
	return r
}

// direction infers a trade's side: PYNE reports closedtrades.size as an
// absolute value, so use the position sign on the entry bar, else the
// sign of profit vs. price move.
func direction(pos domain.Series, t *Trade) bool {
	if t.EntryBar >= 0 && t.EntryBar < len(pos) {
		if p := pos[t.EntryBar].V; p > 0 {
			return true
		} else if p < 0 {
			return false
		}
	}
	move := t.ExitPx - t.EntryPx
	gross := t.Profit + t.Commission
	if move != 0 && gross != 0 {
		return (move > 0) == (gross > 0)
	}
	return true
}

func f64(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func toWire(cs []domain.Candle) ([]wireBar, error) {
	out := make([]wireBar, len(cs))
	var prev int64
	for i, c := range cs {
		for _, v := range []float64{c.O, c.H, c.L, c.C, c.V} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("pyne: candle %d (t=%d) has a non-finite value", i, c.T)
			}
		}
		if i > 0 && c.T <= prev {
			return nil, fmt.Errorf("pyne: candles must be strictly time-ordered (index %d)", i)
		}
		prev = c.T
		out[i] = wireBar{Time: c.T, Open: c.O, High: c.H, Low: c.L, Close: c.C, Volume: c.V}
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// TVTimeframe maps an hlp interval to a TradingView timeframe string
// ("15", "60", "D", ...). Empty/unknown -> "1".
func TVTimeframe(iv domain.Interval) string {
	switch iv {
	case domain.I1d:
		return "D"
	case domain.I3d:
		return "3D"
	case domain.I1w:
		return "W"
	case domain.I1M:
		return "M"
	}
	if d := iv.Duration(); d > 0 {
		return strconv.Itoa(int(d / time.Minute))
	}
	return "1"
}

// resolveInputs maps hlp-style keys (title or variable name, any case) to
// PYNE's title keys. Keys that match nothing are passed through and
// reported.
func resolveInputs(src string, in map[string]any) (map[string]any, []string) {
	out := map[string]any{}
	if len(in) == 0 {
		return out, nil
	}
	byKey := pinesrc.InputKeys(src) // lower(var or title) -> title
	var unknown []string
	for k, v := range in {
		if t, ok := byKey[strings.ToLower(k)]; ok {
			out[t] = v
			continue
		}
		out[k] = v
		unknown = append(unknown, k)
	}
	sort.Strings(unknown)
	return out, unknown
}
