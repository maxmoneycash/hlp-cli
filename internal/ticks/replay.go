package ticks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

const MaxReplayTrades = 2000

var ErrReplayInput = errors.New("invalid replay input")

// ReplayFrame carries a single recorded trade and only the bars changed by it.
// IDs are decimal strings so consumers can retain every bit of a trade ID.
type ReplayFrame struct {
	Time int64   `json:"time"`
	TID  string  `json:"tid"`
	Px   float64 `json:"px"`
	Sz   float64 `json:"sz"`
	Side string  `json:"side"`
	Gap  bool    `json:"gap,omitempty"`
	Bars []Bar   `json:"bars"`
}

type ReplayData struct {
	Coin      string        `json:"coin"`
	Bar       Spec          `json:"bar"`
	Label     string        `json:"label"`
	From      int64         `json:"from"`
	To        int64         `json:"to"`
	Limit     int           `json:"limit"`
	Truncated bool          `json:"truncated"`
	Frames    []ReplayFrame `json:"frames"`
}

// Replay returns a frozen tail of unique trades in the inclusive interval.
// A zero bound is open. Only the local store supplies trades and market names.
func Replay(ctx context.Context, root, coin string, spec Spec, from, to int64, limit int) (*ReplayData, error) {
	if strings.TrimSpace(coin) == "" {
		return nil, fmt.Errorf("%w: coin is required", ErrReplayInput)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReplayInput, err)
	}
	if limit < 1 || limit > MaxReplayTrades {
		return nil, fmt.Errorf("%w: count must be 1-%d", ErrReplayInput, MaxReplayTrades)
	}
	if from < 0 || to < 0 || (to > 0 && from > to) {
		return nil, fmt.Errorf("%w: from and to must be nonnegative epoch milliseconds and from cannot follow to", ErrReplayInput)
	}
	d := &ReplayData{Coin: coin, Bar: spec, Label: spec.Label(coin), Limit: limit, Frames: []ReplayFrame{}}
	if root == "" {
		return d, nil
	}
	coins, err := Coins(root)
	if err != nil {
		return nil, err
	}
	for _, name := range coins {
		if strings.EqualFold(name, coin) {
			d.Coin, d.Label = name, spec.Label(name)
			break
		}
	}
	// Do not Open or lease a recorder. Even an unknown coin is a read-only query.
	store := &Store{dir: filepath.Join(root, CoinDir(d.Coin))}
	days, err := store.Days()
	if err != nil {
		return nil, err
	}
	// Inspect days newest-first so short tails never rebuild the entire store.
	// Keep only the bounded slices to play. Within each touched day use Build's
	// dedupe and stable execution-time ordering (tids are not time ordered).
	agg := NewAggregator(spec, 0)
	var chunks [][]Rec
	left := limit
	for _, day := range slices.Backward(days) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if from > 0 && day < DayOf(from) {
			break
		}
		if to > 0 && day > DayOf(to) {
			continue
		}
		rs, err := store.LoadDay(day)
		if err != nil {
			return nil, err
		}
		rs = Dedupe(rs)
		SortRecs(rs)
		lo := sort.Search(len(rs), func(i int) bool { return rs[i].Time >= from })
		hi := len(rs)
		if to > 0 {
			hi = sort.Search(len(rs), func(i int) bool { return rs[i].Time > to })
		}
		if hi == lo {
			continue
		}
		start := max(lo, hi-left)
		d.Truncated = start > lo
		if left == 0 {
			break // a matching earlier day proves that the returned tail was cut
		}
		chunks = append(chunks, slices.Clone(rs[start:hi]))
		left -= hi - start
		if start > 0 {
			// Starting mid-bar must preserve its true open, count and volume.
			for _, r := range rs[:start] {
				agg.Add(r)
			}
		}
		if d.Truncated || lo > 0 {
			break
		}
	}
	for _, chunk := range slices.Backward(chunks) {
		for _, r := range chunk {
			bars := agg.Add(r)
			if b, ok := agg.Open(); ok {
				bars = append(bars, b)
			}
			side := "buy"
			if r.Side == domain.Sell {
				side = "sell"
			}
			d.Frames = append(d.Frames, ReplayFrame{Time: r.Time, TID: strconv.FormatUint(r.TID, 10),
				Px: r.Px, Sz: r.Sz, Side: side, Gap: r.Gap, Bars: bars})
		}
	}
	if len(d.Frames) > 0 {
		d.From, d.To = d.Frames[0].Time, d.Frames[len(d.Frames)-1].Time
	}
	return d, nil
}
