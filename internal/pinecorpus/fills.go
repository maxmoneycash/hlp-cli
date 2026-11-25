package pinecorpus

import (
	"sort"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// FillsToTrades rebuilds TradingView-style trades from a fill list (hlp's
// backtest.Result.Fills): every opening fill starts a lot, closing fills
// close lots first-in first-out (TradingView's default close rule) and a
// fill that crosses zero closes everything and opens the remainder the
// other way. Each closed piece becomes one Trade with PnL net of its share
// of both fills' fees. Lots still open at the end are returned with
// Open=true and no exit.
func FillsToTrades(fills []domain.Fill) []Trade {
	fs := append([]domain.Fill(nil), fills...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Time < fs[j].Time })
	type lot struct {
		t       Trade
		qty     float64 // remaining
		feeRate float64 // entry fee per unit
	}
	var lots []lot
	var out []Trade
	const eps = 1e-12
	for _, f := range fs {
		if f.Sz <= 0 {
			continue
		}
		q := f.Sz
		if f.Side == domain.Sell {
			q = -q
		}
		feeRate := f.Fee / f.Sz
		at := time.UnixMilli(f.Time).UTC()
		rem := q
		// Reduce opposite lots.
		for len(lots) > 0 && rem != 0 && lots[0].t.Long != (rem > 0) {
			l := &lots[0]
			take := min(l.qty, abs(rem))
			sign := 1.0
			if !l.t.Long {
				sign = -1
			}
			tr := l.t
			tr.Qty = take
			tr.ExitTime, tr.ExitPrice = at, f.Px
			tr.PnL = (f.Px-l.t.EntryPrice)*take*sign - l.feeRate*take - feeRate*take
			out = append(out, tr)
			l.qty -= take
			if rem > 0 {
				rem -= take
			} else {
				rem += take
			}
			if l.qty <= eps {
				lots = lots[1:]
			}
			if abs(rem) <= eps {
				rem = 0
			}
		}
		if rem != 0 {
			lots = append(lots, lot{t: Trade{Long: rem > 0, EntryTime: at, EntryPrice: f.Px, Qty: abs(rem)}, qty: abs(rem), feeRate: feeRate})
		}
	}
	for _, l := range lots {
		t := l.t
		t.Qty, t.Open = l.qty, true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EntryTime.Before(out[j].EntryTime) })
	for i := range out {
		out[i].N = i + 1
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
