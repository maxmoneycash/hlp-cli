package chart

import (
	"math"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Closes returns the close of every candle.
func Closes(cs []domain.Candle) []float64 {
	out := make([]float64, len(cs))
	for i, c := range cs {
		out[i] = c.C
	}
	return out
}

// SMA returns the simple moving average of closes, aligned 1:1 with cs and
// NaN until period candles are available. period < 1 yields all NaN.
// Exported so the strategy package and Pine ta.sma reuse the chart's numbers.
func SMA(cs []domain.Candle, period int) []float64 { return SMAValues(Closes(cs), period) }

// EMA returns the exponential moving average of closes (alpha = 2/(period+1)),
// aligned 1:1 with cs. It is seeded with the SMA of the first period values,
// so it is NaN for the first period-1 candles (TA-Lib / PyneCore semantics).
func EMA(cs []domain.Candle, period int) []float64 { return EMAValues(Closes(cs), period) }

// SMAValues is SMA over an arbitrary series. A NaN input restarts the window.
func SMAValues(vals []float64, period int) []float64 {
	out := make([]float64, len(vals))
	sum, n := 0.0, 0
	for i, v := range vals {
		out[i] = math.NaN()
		if period < 1 {
			continue
		}
		if math.IsNaN(v) {
			sum, n = 0, 0
			continue
		}
		sum += v
		n++
		if n > period {
			sum -= vals[i-period]
			n = period
		}
		if n == period {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// EMAValues is EMA over an arbitrary series (SMA-seeded; a NaN input
// restarts the seed).
func EMAValues(vals []float64, period int) []float64 {
	out := make([]float64, len(vals))
	alpha := 2 / float64(period+1)
	prev, sum, n := math.NaN(), 0.0, 0
	for i, v := range vals {
		out[i] = math.NaN()
		if period < 1 {
			continue
		}
		if math.IsNaN(v) {
			prev, sum, n = math.NaN(), 0, 0
			continue
		}
		if math.IsNaN(prev) {
			sum += v
			n++
			if n == period {
				prev = sum / float64(period)
				out[i] = prev
			}
			continue
		}
		prev = alpha*v + (1-alpha)*prev
		out[i] = prev
	}
	return out
}

// HeikinAshi transforms candles: HAclose = (O+H+L+C)/4, HAopen = average of
// the previous HA open and close (first: (O+C)/2), HAhigh/HAlow include the
// HA open and close. Time, volume and trade count are kept.
func HeikinAshi(cs []domain.Candle) []domain.Candle {
	out := make([]domain.Candle, len(cs))
	for i, c := range cs {
		hc := (c.O + c.H + c.L + c.C) / 4
		ho := (c.O + c.C) / 2
		if i > 0 {
			ho = (out[i-1].O + out[i-1].C) / 2
		}
		out[i] = domain.Candle{
			T: c.T, O: ho, C: hc,
			H: math.Max(c.H, math.Max(ho, hc)),
			L: math.Min(c.L, math.Min(ho, hc)),
			V: c.V, N: c.N,
		}
	}
	return out
}
