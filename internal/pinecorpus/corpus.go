package pinecorpus

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EnvDir names the environment variable that points at a corpus checkout.
const EnvDir = "HLP_PINECORPUS_DIR"

// Probe is one corpus strategy directory under validation/.
type Probe struct {
	Name string // directory name, e.g. "ta-hma-55-close-cross-01"
	Dir  string
	Meta Meta
	// Keys lists every key in inputs.json (Meta decodes only some).
	Keys []string
}

// plainKeys are inputs.json keys that do not change how a probe runs.
var plainKeys = map[string]bool{"_comment": true, "notes": true, "tv_trades_csv_tz": true}

// Plain reports whether the probe runs on the default 15m chart feed with
// no runtime, strategy, feed, timeframe or magnifier overrides, so a
// harness that only feeds 15m candles can grade it fairly.
func (p Probe) Plain() bool {
	for _, k := range p.Keys {
		if !plainKeys[k] {
			return false
		}
	}
	return !p.Anomaly()
}

// Meta is the optional inputs.json beside a probe. Only the fields hlp uses
// are decoded.
type Meta struct {
	TVTradesTZ   string `json:"tv_trades_csv_tz"` // "" means DefaultTVZone
	TVTradesCSV  string `json:"tv_trades_csv"`    // alternative tape file name
	OHLCV        string `json:"ohlcv_csv"`        // relative to the probe dir
	InputTF      string `json:"input_tf"`
	ScriptTF     string `json:"script_tf"`
	ExpectedTier string `json:"expected_tier"` // "anomaly" for declared TradingView-side defects
	OHLCVStartMS int64  `json:"ohlcv_start_ms"`
	// RuntimeOverrides and StrategyOverrides change the run (broker
	// settings, strategy() header values); a harness that cannot apply
	// them should skip the probe rather than report a mismatch.
	RuntimeOverrides  map[string]any `json:"runtime_overrides"`
	StrategyOverrides map[string]any `json:"strategy_overrides"`
}

// PinePath is the probe's Pine v6 source.
func (p Probe) PinePath() string { return filepath.Join(p.Dir, "strategy.pine") }

// TVTradesPath is the TradingView trade list the probe is graded against.
func (p Probe) TVTradesPath() string {
	if p.Meta.TVTradesCSV != "" {
		return filepath.Join(p.Dir, p.Meta.TVTradesCSV)
	}
	return filepath.Join(p.Dir, "tv_trades.csv")
}

// Anomaly reports whether the corpus declares this probe a TradingView-side
// defect that should not count as a failure.
func (p Probe) Anomaly() bool { return p.Meta.ExpectedTier == "anomaly" }

// DefaultTVZone is the chart time zone the corpus's TradingView tapes were
// exported in when inputs.json names none (the corpus convention).
const DefaultTVZone = "asia_taipei"

// Location is the time zone the TradingView tape's timestamps are written in:
// inputs.json's tv_trades_csv_tz, else DefaultTVZone.
func (p Probe) Location() (*time.Location, error) {
	if p.Meta.TVTradesTZ == "" {
		return Location(DefaultTVZone)
	}
	return Location(p.Meta.TVTradesTZ)
}

// Pine returns the probe's Pine source.
func (p Probe) Pine() (string, error) {
	b, err := os.ReadFile(p.PinePath())
	return string(b), err
}

// TVTrades reads the probe's TradingView trade list.
func (p Probe) TVTrades() ([]Trade, error) {
	loc, err := p.Location()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p.TVTradesPath())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadTVTrades(f, loc)
}

// Dir returns the corpus root from $HLP_PINECORPUS_DIR, or "" when unset.
func Dir() string { return os.Getenv(EnvDir) }

// Probes lists every probe under root/validation that has a strategy.pine,
// sorted by name.
func Probes(root string) ([]Probe, error) {
	base := filepath.Join(root, "validation")
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []Probe
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "strategy.pine")); err != nil {
			continue
		}
		p := Probe{Name: e.Name(), Dir: dir}
		if b, err := os.ReadFile(filepath.Join(dir, "inputs.json")); err == nil {
			if err := json.Unmarshal(b, &p.Meta); err != nil {
				return nil, fmt.Errorf("%s/inputs.json: %w", e.Name(), err)
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(b, &raw) == nil {
				for k := range raw {
					p.Keys = append(p.Keys, k)
				}
				sort.Strings(p.Keys)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Location maps the corpus's tape time-zone names to a *time.Location.
// "" and "utc" are UTC; "asia_taipei" style names become "Asia/Taipei".
func Location(name string) (*time.Location, error) {
	switch strings.ToLower(name) {
	case "", "utc":
		return time.UTC, nil
	}
	// Fixed offsets: "utc_plus_8", "utc_minus_5".
	for _, sign := range []struct {
		pfx string
		mul int
	}{{"utc_plus_", 1}, {"utc_minus_", -1}} {
		if rest, ok := strings.CutPrefix(strings.ToLower(name), sign.pfx); ok {
			h, err := strconv.ParseFloat(rest, 64)
			if err != nil {
				return nil, fmt.Errorf("pinecorpus: time zone %q: %w", name, err)
			}
			return time.FixedZone(name, sign.mul*int(h*3600)), nil
		}
	}
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 2 {
		iana := titleWords(parts[0]) + "/" + titleWords(parts[1])
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc, nil
		}
	}
	return time.LoadLocation(name)
}

func titleWords(s string) string {
	w := strings.Split(strings.ToLower(s), "_")
	for i, x := range w {
		if x != "" {
			w[i] = strings.ToUpper(x[:1]) + x[1:]
		}
	}
	return strings.Join(w, "_")
}

// Trade is one round trip from a trade list.
type Trade struct {
	N           int
	Long        bool
	EntryTime   time.Time
	ExitTime    time.Time // zero when Open
	EntryPrice  float64
	ExitPrice   float64
	Qty         float64
	PnL         float64 // net, in quote currency
	EntrySignal string
	ExitSignal  string
	Open        bool // still open at the end of the tape (TradingView's "Open" exit signal)
	// RangeEnd marks the tape's last trade when TradingView closed it with
	// no signal at the end of the backtest date range: its exit is an
	// artefact of the export window, so graders ignore it.
	RangeEnd bool
}

// Bar is one OHLCV row.
type Bar struct {
	Time                           time.Time
	Open, High, Low, Close, Volume float64
}

// tvTimeLayouts are the "Date and time" spellings TradingView exports use.
var tvTimeLayouts = []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"}

// ReadTVTrades parses a TradingView "List of Trades" CSV export. Rows come
// two per trade (entry and exit, newest first in TradingView's export); the
// result has one Trade per trade number, sorted by N. Column headers carry
// the quote currency ("Price USDT", "Net PnL USDT"), so columns are found by
// prefix. Timestamps are read in loc.
func ReadTVTrades(r io.Reader, loc *time.Location) ([]Trade, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	cr := csv.NewReader(bytes.NewReader(b))
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("pinecorpus: empty trade list")
	}
	col := map[string]int{}
	find := func(key string, prefixes ...string) {
		for i, h := range rows[0] {
			h = strings.TrimSpace(h)
			for _, p := range prefixes {
				if strings.EqualFold(h, p) || strings.HasPrefix(strings.ToLower(h), strings.ToLower(p)+" ") {
					if _, ok := col[key]; !ok {
						col[key] = i
					}
				}
			}
		}
	}
	find("n", "Trade number", "Trade #")
	find("type", "Type")
	find("time", "Date and time", "Date/Time")
	find("signal", "Signal")
	find("price", "Price")
	find("qty", "Size (qty)", "Contracts", "Quantity", "Qty")
	find("pnl", "Net PnL", "Profit")
	for _, k := range []string{"n", "type", "time", "price"} {
		if _, ok := col[k]; !ok {
			return nil, fmt.Errorf("pinecorpus: trade list has no %q column (header %q)", k, rows[0])
		}
	}
	get := func(row []string, k string) string {
		i, ok := col[k]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	byN := map[int]*Trade{}
	for li, row := range rows[1:] {
		if len(row) == 0 || (len(row) == 1 && row[0] == "") {
			continue
		}
		line := li + 2
		n, err := strconv.Atoi(get(row, "n"))
		if err != nil {
			return nil, fmt.Errorf("pinecorpus: line %d: trade number: %w", line, err)
		}
		typ := strings.ToLower(get(row, "type"))
		var entry bool
		switch {
		case strings.HasPrefix(typ, "entry"):
			entry = true
		case strings.HasPrefix(typ, "exit"):
		default:
			return nil, fmt.Errorf("pinecorpus: line %d: unknown type %q", line, get(row, "type"))
		}
		long := strings.HasSuffix(typ, "long")
		ts, err := parseTVTime(get(row, "time"), loc)
		if err != nil {
			return nil, fmt.Errorf("pinecorpus: line %d: %w", line, err)
		}
		price, err := parseNum(get(row, "price"))
		if err != nil {
			return nil, fmt.Errorf("pinecorpus: line %d: price: %w", line, err)
		}
		qty, _ := parseNum(get(row, "qty"))
		pnl, _ := parseNum(get(row, "pnl"))
		t := byN[n]
		if t == nil {
			t = &Trade{N: n, Long: long}
			byN[n] = t
		}
		sig := get(row, "signal")
		if entry {
			t.EntryTime, t.EntryPrice, t.EntrySignal = ts, price, sig
			if qty != 0 {
				t.Qty = qty
			}
		} else {
			t.ExitPrice, t.ExitSignal, t.PnL = price, sig, pnl
			if strings.EqualFold(sig, "Open") {
				t.Open = true
			} else {
				t.ExitTime = ts
			}
			if t.Qty == 0 {
				t.Qty = qty
			}
		}
	}
	out := make([]Trade, 0, len(byN))
	for _, t := range byN {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	if n := len(out); n > 0 {
		last := &out[n-1]
		if !last.Open && last.ExitSignal == "" && !last.ExitTime.IsZero() {
			latest := true
			for _, t := range out[:n-1] {
				if t.ExitTime.After(last.ExitTime) {
					latest = false
					break
				}
			}
			last.RangeEnd = latest
		}
	}
	return out, nil
}

func parseTVTime(s string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	for _, l := range tvTimeLayouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q", s)
}

func parseNum(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "−", "-") // TradingView may use U+2212 minus
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// ReadOHLCV parses the corpus's OHLCV CSV: a header
// timestamp,open,high,low,close,volume with Unix-millisecond timestamps.
func ReadOHLCV(r io.Reader) ([]Bar, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	if len(head) < 6 || !strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(head[0], "\ufeff")), "timestamp") {
		return nil, fmt.Errorf("pinecorpus: unexpected OHLCV header %q", head)
	}
	var out []Bar
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var v [6]float64
		for i := 0; i < 6; i++ {
			if v[i], err = strconv.ParseFloat(rec[i], 64); err != nil {
				return nil, fmt.Errorf("pinecorpus: OHLCV line %d col %d: %w", line, i+1, err)
			}
		}
		out = append(out, Bar{Time: time.UnixMilli(int64(v[0])).UTC(), Open: v[1], High: v[2], Low: v[3], Close: v[4], Volume: v[5]})
	}
}

// Diff summarises how a trade list compares with a reference one.
type Diff struct {
	Want, Got int // trades in each list (inside the compared window)
	Matched   int
	// Missing are reference trades with no counterpart; Extra are the
	// other way round.
	Missing, Extra []Trade
	// Largest relative price errors and absolute PnL error over matched
	// trades (open trades' exit side is skipped).
	MaxEntryRel, MaxExitRel, MaxPnLAbs float64
}

// Exact reports whether every trade matched with prices within tol
// (relative) — hlp's own pass bar, not PineForge's grading rubric.
func (d Diff) Exact(tol float64) bool {
	return d.Want == d.Got && d.Matched == d.Want && d.MaxEntryRel <= tol && d.MaxExitRel <= tol
}

func (d Diff) String() string {
	return fmt.Sprintf("want=%d got=%d matched=%d missing=%d extra=%d maxEntryRel=%.3g maxExitRel=%.3g maxPnL=%.4g",
		d.Want, d.Got, d.Matched, len(d.Missing), len(d.Extra), d.MaxEntryRel, d.MaxExitRel, d.MaxPnLAbs)
}

// Compare pairs got with want by direction and entry time (within slack)
// in entry order, and reports the differences. Only trades whose entry
// falls inside the reference list's entry span are counted on the got side,
// because a tape covers a window of the feed while an engine run usually
// covers all of it.
func Compare(want, got []Trade, slack time.Duration) Diff {
	pairs, missing, extra, nGot := pair(want, got, slack)
	d := Diff{Want: len(want), Got: nGot, Matched: len(pairs), Missing: missing, Extra: extra}
	for _, p := range pairs {
		wt, gt := p[0], p[1]
		d.MaxEntryRel = math.Max(d.MaxEntryRel, rel(gt.EntryPrice, wt.EntryPrice))
		if !wt.Open && !gt.Open {
			d.MaxExitRel = math.Max(d.MaxExitRel, rel(gt.ExitPrice, wt.ExitPrice))
			d.MaxPnLAbs = math.Max(d.MaxPnLAbs, math.Abs(gt.PnL-wt.PnL))
		}
	}
	return d
}

// pair matches got to want (see Compare) and returns the matched
// [want, got] pairs, the unmatched trades of each side and how many got
// trades fell inside want's entry span.
func pair(want, got []Trade, slack time.Duration) (pairs [][2]Trade, missing, extra []Trade, nGot int) {
	w := sortedByEntry(want)
	g := sortedByEntry(got)
	if len(w) > 0 {
		lo, hi := w[0].EntryTime.Add(-slack), w[len(w)-1].EntryTime.Add(slack)
		kept := g[:0:0]
		for _, t := range g {
			if !t.EntryTime.Before(lo) && !t.EntryTime.After(hi) {
				kept = append(kept, t)
			}
		}
		g = kept
	}
	used := make([]bool, len(g))
	j := 0
	for _, wt := range w {
		for j < len(g) && used[j] {
			j++
		}
		match := -1
		for k := j; k < len(g); k++ {
			if g[k].EntryTime.After(wt.EntryTime.Add(slack)) {
				break
			}
			if used[k] || g[k].Long != wt.Long {
				continue
			}
			if absDur(g[k].EntryTime.Sub(wt.EntryTime)) > slack {
				continue
			}
			// Several trades can enter at one time (pyramiding, flips):
			// prefer the closest entry, then exit, price.
			if match < 0 || closer(g[k], g[match], wt) {
				match = k
			}
		}
		if match < 0 {
			missing = append(missing, wt)
			continue
		}
		used[match] = true
		pairs = append(pairs, [2]Trade{wt, g[match]})
	}
	for k, u := range used {
		if !u {
			extra = append(extra, g[k])
		}
	}
	return pairs, missing, extra, len(g)
}

func closer(a, b, ref Trade) bool {
	da, db := math.Abs(a.EntryPrice-ref.EntryPrice), math.Abs(b.EntryPrice-ref.EntryPrice)
	if da != db {
		return da < db
	}
	return math.Abs(a.ExitPrice-ref.ExitPrice) < math.Abs(b.ExitPrice-ref.ExitPrice)
}

func sortedByEntry(ts []Trade) []Trade {
	out := append([]Trade(nil), ts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].EntryTime.Before(out[j].EntryTime) })
	return out
}

func rel(a, b float64) float64 {
	if b == 0 {
		return math.Abs(a - b)
	}
	return math.Abs(a-b) / math.Abs(b)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// EngineTrades reads the probe's PineForge engine tape (engine_trades.csv,
// Apache-2.0), written in TradingView's row layout with UTC timestamps.
func (p Probe) EngineTrades() ([]Trade, error) {
	f, err := os.Open(filepath.Join(p.Dir, "engine_trades.csv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadTVTrades(f, time.UTC)
}
