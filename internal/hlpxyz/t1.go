package hlpxyz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// UnmarshalJSON accepts the columnar "t1" object and also the array form
// that light and non-enc responses use: `[]` (light, no trades) or row
// objects ({time|_ts, coin, dir, px, sz, startPosition, fee, closedPnl,
// tid, oid}), which are converted to columns so DecodeT1 handles every
// shape.
func (t *TradesT1) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*t = TradesT1{}
		return nil
	}
	if b[0] != '[' {
		type plain TradesT1
		var p plain
		if err := json.Unmarshal(b, &p); err != nil {
			return err
		}
		*t = TradesT1(p)
		return nil
	}
	var rows []struct {
		Time      json.RawMessage `json:"time"`
		TS        int64           `json:"_ts"`
		Coin      string          `json:"coin"`
		Dir       string          `json:"dir"`
		Px        flexNum         `json:"px"`
		Sz        flexNum         `json:"sz"`
		StartPos  flexNum         `json:"startPosition"`
		Fee       flexNum         `json:"fee"`
		ClosedPnL flexNum         `json:"closedPnl"`
		TID       *int64          `json:"tid"`
		OID       *int64          `json:"oid"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return fmt.Errorf("hlpxyz: trades rows: %w", err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rowTime(rows[i].Time, rows[i].TS) < rowTime(rows[j].Time, rows[j].TS)
	})
	return t.fromRows(len(rows), func(i int) rowT1 {
		r := rows[i]
		return rowT1{rowTime(r.Time, r.TS), r.Coin, r.Dir, float64(r.Px), float64(r.Sz), float64(r.StartPos), float64(r.Fee), float64(r.ClosedPnL), r.TID, r.OID}
	})
}

// flexNum decodes a JSON number or a decimal string (Hyperliquid style).
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(bytes.TrimSpace(b), `"`)
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*f = flexNum(v)
	return nil
}

type rowT1 struct {
	t                         int64
	coin, dir                 string
	px, sz, sp, fee, closedPn float64
	tid, oid                  *int64
}

// rowTime prefers _ts, then a numeric "time" in ms.
func rowTime(raw json.RawMessage, ts int64) int64 {
	if ts > 0 {
		return ts
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

// fromRows rebuilds the columnar form (absolute ids are re-encoded as
// running-sum deltas so DecodeT1 restores them).
func (t *TradesT1) fromRows(n int, row func(int) rowT1) error {
	*t = TradesT1{W: "t1", N: n}
	coinIx, dirIx := map[string]int{}, map[string]int{}
	var prevT, tidAcc, oidAcc int64
	for i := 0; i < n; i++ {
		r := row(i)
		if _, ok := coinIx[r.coin]; !ok {
			coinIx[r.coin] = len(t.Coins)
			t.Coins = append(t.Coins, r.coin)
		}
		if _, ok := dirIx[r.dir]; !ok {
			dirIx[r.dir] = len(t.Dirs)
			t.Dirs = append(t.Dirs, r.dir)
		}
		if i == 0 {
			t.T0 = r.t
			t.TS = append(t.TS, 0)
		} else {
			t.TS = append(t.TS, r.t-prevT)
		}
		prevT = r.t
		t.C = append(t.C, coinIx[r.coin])
		t.D = append(t.D, dirIx[r.dir])
		t.TID = append(t.TID, delta(r.tid, &tidAcc))
		t.OID = append(t.OID, delta(r.oid, &oidAcc))
		t.Px = append(t.Px, r.px)
		t.Sz = append(t.Sz, r.sz)
		t.SP = append(t.SP, r.sp)
		t.Fee = append(t.Fee, r.fee)
		t.PnL = append(t.PnL, r.closedPn)
	}
	return nil
}

func delta(abs *int64, acc *int64) *int64 {
	if abs == nil {
		return nil
	}
	d := *abs - *acc
	*acc = *abs
	return &d
}

// DecodeT1 expands the columnar trades into fills (time-ordered).
//
// Encoding: times are t0 plus running sums of the ts deltas;
// tid and oid are running sums of their non-null deltas (a null leaves the
// accumulator unchanged and yields 0 for that row). Side is derived from
// the direction (Open Long / Close Short / Short > Long buy; the rest
// sell), falling back to the start position for Auto-Deleveraging,
// Settlement and liquidations. Rows are returned sorted by time (stable,
// so same-timestamp rows keep their server order). Mismatched column
// lengths or out-of-range coin/dir indexes are an error.
func DecodeT1(t TradesT1) ([]domain.Fill, error) {
	n := len(t.TS)
	if t.N > 0 && t.N != n {
		return nil, fmt.Errorf("hlpxyz: t1 n=%d but %d timestamps", t.N, n)
	}
	if n == 0 {
		return nil, nil
	}
	if t.W != "" && t.W != "t1" {
		return nil, fmt.Errorf("hlpxyz: unknown trades encoding %q", t.W)
	}
	cols := map[string]int{"c": len(t.C), "d": len(t.D), "px": len(t.Px), "sz": len(t.Sz), "sp": len(t.SP), "fee": len(t.Fee), "pnl": len(t.PnL)}
	for name, l := range cols {
		if l != n {
			return nil, fmt.Errorf("hlpxyz: t1 column %s has %d values, want %d", name, l, n)
		}
	}
	if (len(t.TID) != 0 && len(t.TID) != n) || (len(t.OID) != 0 && len(t.OID) != n) {
		return nil, fmt.Errorf("hlpxyz: t1 tid/oid length %d/%d, want %d", len(t.TID), len(t.OID), n)
	}
	out := make([]domain.Fill, n)
	ts := t.T0
	var tidAcc, oidAcc int64
	for i := 0; i < n; i++ {
		ts += t.TS[i]
		if t.C[i] < 0 || t.C[i] >= len(t.Coins) {
			return nil, fmt.Errorf("hlpxyz: t1 row %d: coin index %d out of range", i, t.C[i])
		}
		if t.D[i] < 0 || t.D[i] >= len(t.Dirs) {
			return nil, fmt.Errorf("hlpxyz: t1 row %d: dir index %d out of range", i, t.D[i])
		}
		f := domain.Fill{
			Coin:      t.Coins[t.C[i]],
			Dir:       t.Dirs[t.D[i]],
			Px:        t.Px[i],
			Sz:        t.Sz[i],
			Time:      ts,
			StartPos:  t.SP[i],
			ClosedPnL: t.PnL[i],
			Fee:       t.Fee[i],
		}
		if i < len(t.TID) && t.TID[i] != nil {
			tidAcc += *t.TID[i]
			f.TID = uint64(max(tidAcc, 0))
		}
		if i < len(t.OID) && t.OID[i] != nil {
			oidAcc += *t.OID[i]
			f.OID = uint64(max(oidAcc, 0))
		}
		f.Side = sideOf(f.Dir, f.StartPos)
		f.Liq = strings.Contains(strings.ToLower(f.Dir), "liquidat")
		out[i] = f
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

// sideOf maps a Hyperliquid fill direction to the aggressor side.
func sideOf(dir string, startPos float64) domain.Side {
	switch dir {
	case "Open Long", "Close Short", "Short > Long", "Buy":
		return domain.Buy
	case "Open Short", "Close Long", "Long > Short", "Sell":
		return domain.Sell
	}
	d := strings.ToLower(dir)
	switch {
	case strings.Contains(d, "close long") || strings.Contains(d, "open short"):
		return domain.Sell
	case strings.Contains(d, "close short") || strings.Contains(d, "open long"):
		return domain.Buy
	}
	// Auto-Deleveraging, Settlement, liquidations: they reduce the
	// position, so a long sells and a short buys.
	if startPos < 0 {
		return domain.Buy
	}
	return domain.Sell
}
