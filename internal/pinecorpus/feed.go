package pinecorpus

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Feed1m is the corpus's only committed feed: Binance ETH-USDT perp 1m bars
// from 2020-01-01 to 2026-05-04 (Git LFS).
const Feed1m = "data/ohlcv_ETH-USDT-USDT_1m.csv"

// Resample aggregates bars into step-sized bars aligned to Unix-epoch
// multiples of step (open=first, high=max, low=min, close=last,
// volume=sum), the way the corpus derives its 15m chart feed. Input must be
// sorted by time; empty buckets produce no bar.
func Resample(bars []Bar, step time.Duration) []Bar {
	if step <= 0 || len(bars) == 0 {
		return nil
	}
	ms := step.Milliseconds()
	var out []Bar
	cur := int64(math.MinInt64)
	for _, b := range bars {
		k := b.Time.UnixMilli()
		k -= ((k % ms) + ms) % ms
		if k != cur {
			out = append(out, Bar{Time: time.UnixMilli(k).UTC(), Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume})
			cur = k
			continue
		}
		last := &out[len(out)-1]
		last.High = math.Max(last.High, b.High)
		last.Low = math.Min(last.Low, b.Low)
		last.Close = b.Close
		last.Volume += b.Volume
	}
	return out
}

// Candles converts bars to hlp candles (T = open time in ms).
func Candles(bars []Bar) []domain.Candle {
	out := make([]domain.Candle, len(bars))
	for i, b := range bars {
		out[i] = domain.Candle{T: b.Time.UnixMilli(), O: b.Open, H: b.High, L: b.Low, C: b.Close, V: b.Volume}
	}
	return out
}

// ChartFeed returns the corpus feed resampled to step, caching the result
// under root/data/derived (the corpus's own git-ignored derived-feed
// directory).
func ChartFeed(root string, step time.Duration) ([]Bar, error) {
	derived := filepath.Join(root, "data", "derived", fmt.Sprintf("ohlcv_ETH-USDT-USDT_%dm.csv", int(step/time.Minute)))
	if f, err := os.Open(derived); err == nil {
		defer f.Close()
		return ReadOHLCV(bufio.NewReaderSize(f, 1<<20))
	}
	f, err := os.Open(filepath.Join(root, Feed1m))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m1, err := ReadOHLCV(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("pinecorpus: %s: %w", Feed1m, err)
	}
	out := Resample(m1, step)
	if err := os.MkdirAll(filepath.Dir(derived), 0o755); err == nil {
		tmp := derived + ".tmp"
		if err := writeOHLCV(tmp, out); err == nil {
			_ = os.Rename(tmp, derived)
		} else {
			os.Remove(tmp)
		}
	}
	return out, nil
}

func writeOHLCV(path string, bars []Bar) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprintln(w, "timestamp,open,high,low,close,volume")
	g := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	for _, b := range bars {
		fmt.Fprintf(w, "%d,%s,%s,%s,%s,%s\n", b.Time.UnixMilli(), g(b.Open), g(b.High), g(b.Low), g(b.Close), g(b.Volume))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
