package ticks

import "sort"

// Series builds the newest want bars (0 = every stored bar) from the store
// plus extra trades this process holds in memory, reading day files newest
// first and stopping once it has enough. Bars restart at each UTC midnight,
// so a day builds the same bars however much history is loaded. Bars of
// days before today are closed; today's newest bar is open.
//
// The returned aggregator holds today's open bar with matching indexes
// (i runs 0..n-1 over the result), ready to take live trades.
func Series(s *Store, extra []Rec, spec Spec, want int, today int64) ([]Bar, *Aggregator, error) {
	byDay := map[int64][]Rec{}
	for _, r := range extra {
		d := DayOf(r.Time)
		byDay[d] = append(byDay[d], r)
	}
	daySet := map[int64]bool{}
	for d := range byDay {
		daySet[d] = true
	}
	if s != nil {
		ds, err := s.Days()
		if err != nil {
			return nil, nil, err
		}
		for _, d := range ds {
			daySet[d] = true
		}
	}
	days := make([]int64, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] > days[j] })

	var chunks [][]Bar
	var agg *Aggregator
	total := 0
	for i, d := range days {
		var rs []Rec
		if s != nil {
			stored, err := s.LoadDay(d)
			if err != nil {
				return nil, nil, err
			}
			rs = stored
		}
		rs = append(rs, byDay[d]...)
		live := i == 0 && d >= today
		bars, a := Build(rs, spec, 0, !live)
		if live {
			agg = a
		}
		chunks = append(chunks, bars)
		total += len(bars)
		if want > 0 && total >= want {
			break
		}
	}
	out := make([]Bar, 0, total)
	for i := len(chunks) - 1; i >= 0; i-- {
		out = append(out, chunks[i]...)
	}
	if want > 0 && len(out) > want {
		out = out[len(out)-want:]
	}
	for i := range out {
		out[i].I = int64(i)
	}
	if agg == nil {
		agg = NewAggregator(spec, int64(len(out)))
	} else if agg.has && len(out) > 0 {
		agg.cur.I = out[len(out)-1].I
		agg.next = agg.cur.I + 1
	} else {
		agg.next = int64(len(out))
	}
	return out, agg, nil
}
