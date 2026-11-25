package chart

import (
	"fmt"
	"math"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// synth is a deterministic random-walk-ish series (PERFORMANCE harness).
func synth(n int) []domain.Candle {
	cs := make([]domain.Candle, n)
	p := 100.0
	for i := range cs {
		d := math.Sin(float64(i)/7) * 1.5
		cs[i] = domain.Candle{T: int64(i) * 60000, O: p, H: p + 2 + math.Abs(d), L: p - 2 - math.Abs(d), C: p + d, V: float64(i%17) + 1}
		p = cs[i].C
	}
	return cs
}

func benchOpts(w, h int) CandleOpts {
	return CandleOpts{Width: w, Height: h, VolRows: -1, VolumeUSD: true, MAs: []int{20, 50}, EMAs: []int{200},
		Cursor: -1, Header: true, Coin: "BTC", Interval: domain.I1m}
}

// BenchmarkCandlesMatrix is the PERFORMANCE "Chart rendering" table: a full
// frame (grid + ANSI) per size and series length, each frame from scratch.
func BenchmarkCandlesMatrix(b *testing.B) {
	th, _ := theme.Get("hlp")
	for _, sz := range [][2]int{{80, 24}, {200, 60}} {
		for _, n := range []int{500, 1000, 5000} {
			cs := synth(n)
			o := benchOpts(sz[0], sz[1])
			b.Run(fmt.Sprintf("%dx%d/n=%d", sz[0], sz[1], n), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					g, _ := Candles(cs, o, th)
					_ = g.ANSI(colorprofile.TrueColor)
				}
			})
		}
	}
}

// BenchmarkCandlesLive is the TUI's steady state: one pane reusing its grid
// (CandleOpts.Into) while the open bar ticks, so the overlay cache hits.
func BenchmarkCandlesLive(b *testing.B) {
	th, _ := theme.Get("hlp")
	for _, sz := range [][2]int{{80, 24}, {200, 60}} {
		cs := synth(5000)
		o := benchOpts(sz[0], sz[1])
		b.Run(fmt.Sprintf("%dx%d/n=5000", sz[0], sz[1]), func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				cs[len(cs)-1].C = 100 + float64(i%7)*0.1
				i++
				g, _ := Candles(cs, o, th)
				o.Into = g
				_ = g.ANSI(colorprofile.TrueColor)
			}
		})
	}
}
