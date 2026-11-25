// Package testkit holds shared test fixtures for the strategy packages:
// the recorded BTC 15m candles and an in-memory DataSource / paper Feed.
// It is internal to internal/strategy and only used by tests.
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// RepoRoot returns the repository root (located from this file).
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

type wireCandle struct {
	T int64 `json:"t"`
	// TClose is Hyperliquid's close time "T". It must be decoded into its
	// own field: encoding/json matches keys case-insensitively, so without
	// it "T" would overwrite the open time.
	TClose int64  `json:"T"`
	O      string `json:"o"`
	H      string `json:"h"`
	L      string `json:"l"`
	C      string `json:"c"`
	V      string `json:"v"`
	N      int    `json:"n"`
}

// BTC15m loads the BTC 15m candle sample (a raw Hyperliquid
// candleSnapshot, 301 bars).
func BTC15m() ([]domain.Candle, error) {
	b, err := os.ReadFile(filepath.Join(RepoRoot(), "testdata", "candles", "btc-15m.json"))
	if err != nil {
		return nil, err
	}
	var ws []wireCandle
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, err
	}
	f := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	out := make([]domain.Candle, len(ws))
	for i, w := range ws {
		out[i] = domain.Candle{T: w.T, O: f(w.O), H: f(w.H), L: f(w.L), C: f(w.C), V: f(w.V), N: w.N}
	}
	return out, nil
}

// Synthetic builds n bars for a price path p(i) on interval iv starting at t0.
func Synthetic(t0 int64, iv domain.Interval, n int, p func(i int) float64) []domain.Candle {
	d := iv.Duration().Milliseconds()
	out := make([]domain.Candle, n)
	prev := p(0)
	for i := 0; i < n; i++ {
		c := p(i)
		o := prev
		h, l := max(o, c)*1.002, min(o, c)*0.998
		out[i] = domain.Candle{T: t0 + int64(i)*d, O: o, H: h, L: l, C: c, V: 100 + float64(i%7), N: 10}
		prev = c
	}
	return out
}

// Source is an in-memory strategy.DataSource. Calls are counted.
type Source struct {
	mu      sync.Mutex
	Candle  map[string][]domain.Candle
	Fund    map[string][]domain.FundingPoint
	Calls   int
	FailFor map[string]error
}

// Candles returns up to n bars with open time < end (all when end == 0).
func (s *Source) Candles(_ context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	if err := s.FailFor[coin]; err != nil {
		return nil, err
	}
	cs, ok := s.Candle[coin]
	if !ok {
		return nil, fmt.Errorf("unknown coin %s", coin)
	}
	var out []domain.Candle
	for _, c := range cs {
		if end == 0 || c.T < end {
			out = append(out, c)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return append([]domain.Candle(nil), out...), nil
}

// Funding returns points in [start, end].
func (s *Source) Funding(_ context.Context, coin string, start, end int64) ([]domain.FundingPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.FundingPoint
	for _, f := range s.Fund[coin] {
		if f.Time >= start && (end == 0 || f.Time <= end) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}
