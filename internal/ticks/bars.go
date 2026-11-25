package ticks

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Kind is what closes a bar.
type Kind string

const (
	KindTicks  Kind = "ticks"  // N trades
	KindVolume Kind = "volume" // N base units (contracts)
	KindUSD    Kind = "usd"    // N USD notional
	KindRange  Kind = "range"  // high-low reaches N quote units
)

// Spec is a bar type and size: {kind: ticks, size: 500}.
type Spec struct {
	Kind Kind    `json:"kind" jsonschema:"ticks, volume, usd or range"`
	Size float64 `json:"size" jsonschema:"trades per bar (ticks), base units (volume), USD (usd) or price range in quote units (range)"`
}

// MaxTicks bounds a tick bar (a bar that never closes is not a chart).
const MaxTicks = 1_000_000

// Validate reports a bad spec.
func (s Spec) Validate() error {
	if math.IsNaN(s.Size) || math.IsInf(s.Size, 0) || s.Size <= 0 {
		return fmt.Errorf("bar size must be positive, got %v", s.Size)
	}
	switch s.Kind {
	case KindTicks:
		if s.Size != math.Trunc(s.Size) || s.Size > MaxTicks {
			return fmt.Errorf("ticks must be a whole number from 1 to %d, got %v", MaxTicks, s.Size)
		}
	case KindVolume, KindUSD, KindRange:
	default:
		return fmt.Errorf("unknown bar kind %q (ticks, volume, usd, range)", s.Kind)
	}
	return nil
}

// Label is the short chart label: 500T, 10 BTC, $1M, R50.
func (s Spec) Label(base string) string {
	switch s.Kind {
	case KindTicks:
		return strconv.FormatFloat(s.Size, 'f', -1, 64) + "T"
	case KindVolume:
		if base == "" {
			return compact(s.Size) + "V"
		}
		return compact(s.Size) + " " + strings.ToUpper(base)
	case KindUSD:
		return "$" + compact(s.Size)
	case KindRange:
		return "R" + strconv.FormatFloat(s.Size, 'f', -1, 64)
	}
	return string(s.Kind)
}

func compact(v float64) string {
	for _, u := range []struct {
		d float64
		s string
	}{{1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if v >= u.d && math.Mod(v, u.d/10) == 0 {
			return strconv.FormatFloat(v/u.d, 'f', -1, 64) + u.s
		}
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Rec is one stored trade. Gap marks that trades may be missing right
// before it (a reconnect or a restart whose seed did not overlap what was
// stored): bars never span a gap.
type Rec struct {
	Time int64
	TID  uint64
	Px   float64
	Sz   float64
	Side domain.Side
	Gap  bool
}

// FromTrade converts a print.
func FromTrade(t domain.Trade) Rec {
	return Rec{Time: t.Time, TID: t.TID, Px: t.Px, Sz: t.Sz, Side: t.Side}
}

// Bar is one tick, volume or range bar. Times are ms; t is the first
// trade's time (the open), tLast the last's. Volumes split by the
// aggressor side: buyV is volume where the buyer took liquidity.
type Bar struct {
	// I is the bar's position in the series, stable for a stream's life:
	// a live update with the same i replaces the bar.
	I     int64   `json:"i"`
	T     int64   `json:"t"`
	TLast int64   `json:"tLast"`
	O     float64 `json:"o"`
	H     float64 `json:"h"`
	L     float64 `json:"l"`
	C     float64 `json:"c"`
	// V is base volume, VUSD the USD notional (sum of px*sz).
	V     float64 `json:"v"`
	VUSD  float64 `json:"vUsd"`
	N     int     `json:"n"`
	BuyV  float64 `json:"buyV"`
	SellV float64 `json:"sellV"`
	// FirstTID and LastTID are the tids of the open and close trades.
	FirstTID uint64 `json:"firstTid"`
	LastTID  uint64 `json:"lastTid"`
	// Closed is false only for the newest bar while it fills.
	Closed bool `json:"closed"`
	// Partial: closed before reaching its size, because a gap followed or
	// the UTC day ended (bars restart at 00:00 UTC).
	Partial bool `json:"partial,omitempty"`
	// Gap: trades may be missing just before this bar's first trade.
	Gap bool `json:"gap,omitempty"`
}

// Candle converts a bar for the chart renderer (T = first trade time).
func (b Bar) Candle() domain.Candle {
	return domain.Candle{T: b.T, O: b.O, H: b.H, L: b.L, C: b.C, V: b.V, N: b.N}
}

// DayMs is one UTC day in ms; bars are anchored at each UTC midnight.
const DayMs = 86_400_000

// DayOf is the UTC day number of a ms time.
func DayOf(ms int64) int64 {
	d := ms / DayMs
	if ms < 0 && ms%DayMs != 0 {
		d--
	}
	return d
}

// Aggregator folds trades into bars. Feed it trades in time order; a late
// trade (older than the open bar's first trade) still joins the open bar,
// and the open and close always follow the earliest and latest trade time
// (ties keep arrival order), so a sorted replay gives the same bar.
type Aggregator struct {
	spec Spec
	cur  Bar
	has  bool
	day  int64
	next int64
}

// NewAggregator starts an empty aggregator whose first bar gets index next.
func NewAggregator(spec Spec, next int64) *Aggregator {
	return &Aggregator{spec: spec, next: next}
}

// Spec returns the bar spec.
func (a *Aggregator) Spec() Spec { return a.spec }

// Next is the index the next new bar will get.
func (a *Aggregator) Next() int64 { return a.next }

// Open returns the bar being filled.
func (a *Aggregator) Open() (Bar, bool) { return a.cur, a.has }

// Add folds one trade in and returns the bars it closed (0, 1 or 2: a gap
// or a new UTC day closes the open bar before this trade starts the next).
func (a *Aggregator) Add(r Rec) []Bar {
	var out []Bar
	d := DayOf(r.Time)
	if a.has && (r.Gap || d > a.day) {
		if b, ok := a.Flush(); ok {
			out = append(out, b)
		}
	}
	if !a.has {
		a.cur = Bar{I: a.next, T: r.Time, TLast: r.Time, O: r.Px, H: r.Px, L: r.Px, C: r.Px,
			FirstTID: r.TID, LastTID: r.TID, Gap: r.Gap}
		a.next++
		a.has, a.day = true, d
	} else {
		b := &a.cur
		if r.Time < b.T {
			b.T, b.O, b.FirstTID = r.Time, r.Px, r.TID
		}
		if r.Time >= b.TLast {
			b.TLast, b.C, b.LastTID = r.Time, r.Px, r.TID
		}
		b.H, b.L = math.Max(b.H, r.Px), math.Min(b.L, r.Px)
	}
	b := &a.cur
	b.N++
	b.V += r.Sz
	b.VUSD += r.Px * r.Sz
	if r.Side == domain.Sell {
		b.SellV += r.Sz
	} else {
		b.BuyV += r.Sz
	}
	if a.reached() {
		b.Closed = true
		out = append(out, *b)
		a.has = false
	}
	return out
}

// Flush closes the open bar early (Partial when it had not reached its
// size) and returns it.
func (a *Aggregator) Flush() (Bar, bool) {
	if !a.has {
		return Bar{}, false
	}
	b := a.cur
	b.Closed = true
	b.Partial = !a.reached()
	a.has = false
	return b, true
}

// eps absorbs float drift in summed volumes (0.1 + 0.2 reaching 0.3).
const eps = 1e-9

func (a *Aggregator) reached() bool {
	b, s := a.cur, a.spec.Size
	switch a.spec.Kind {
	case KindTicks:
		return float64(b.N) >= s
	case KindVolume:
		return b.V >= s*(1-eps)
	case KindUSD:
		return b.VUSD >= s*(1-eps)
	case KindRange:
		return b.H-b.L >= s*(1-eps)
	}
	return false
}

// SortRecs orders trades by time, keeping arrival order for equal times
// (Hyperliquid tids are not monotonic, so they cannot break ties).
func SortRecs(rs []Rec) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time < rs[j].Time })
}

// Dedupe drops repeated tids (tid 0 is never treated as a duplicate),
// keeping the first; a gap mark on a dropped copy moves to the kept one.
func Dedupe(rs []Rec) []Rec {
	seen := make(map[uint64]int, len(rs))
	out := rs[:0:0]
	for _, r := range rs {
		if r.TID != 0 {
			if i, ok := seen[r.TID]; ok {
				out[i].Gap = out[i].Gap || r.Gap
				continue
			}
			seen[r.TID] = len(out)
		}
		out = append(out, r)
	}
	return out
}

// Build folds trades (in any order; sorted here) into bars. The newest bar
// is returned open unless closeLast is set, when it is flushed. The
// aggregator is returned holding the open bar, ready for live trades.
func Build(rs []Rec, spec Spec, first int64, closeLast bool) ([]Bar, *Aggregator) {
	rs = Dedupe(rs)
	SortRecs(rs)
	a := NewAggregator(spec, first)
	var out []Bar
	for _, r := range rs {
		out = append(out, a.Add(r)...)
	}
	if closeLast {
		if b, ok := a.Flush(); ok {
			out = append(out, b)
		}
	} else if b, ok := a.Open(); ok {
		out = append(out, b)
	}
	return out, a
}
