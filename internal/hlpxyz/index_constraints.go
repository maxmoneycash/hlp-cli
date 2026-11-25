package hlpxyz

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/metrics"
)

// IndexCapacityPoint is an actual daily idxCap observation, not an interpolated
// permission or a present-day deposit quote. LeaderCapacityUSD is only the
// leader-5% formula (-1 for HLP); AllowDeposits is a separate source field.
type IndexCapacityPoint struct {
	T                 int64   `json:"t"`
	LeaderFraction    float64 `json:"leaderFraction"`
	Equity            float64 `json:"equity"`
	AllowDeposits     bool    `json:"allowDeposits"`
	LeaderCapacityUSD float64 `json:"leaderCapacityUsd"`
}

type IndexConstraintMember struct {
	Address          string               `json:"address"`
	Capacity         []IndexCapacityPoint `json:"capacity"`
	CapacityAsOf     int64                `json:"capacityAsOf"`
	CapacityStale    bool                 `json:"capacityStale"`
	TrackedSince     *int64               `json:"trackedSince"`
	DepositsClosedAt *int64               `json:"depositsClosedAt"`
}

// FetchedAt is transport observation time, not the age of a closure/tracking
// event. Unknown or absent closure dates never imply deposits are open.
type IndexConstraintSource struct {
	FetchedAt int64    `json:"fetchedAt"`
	Cached    bool     `json:"cached"`
	Stale     bool     `json:"stale"`
	Complete  bool     `json:"complete"`
	Pending   int      `json:"pending"`
	Missing   []string `json:"missing"`
}

type IndexConstraints struct {
	Members    []IndexConstraintMember          `json:"members"`
	Sources    map[string]IndexConstraintSource `json:"sources"`
	ObservedAt int64                            `json:"observedAt"`
	Complete   bool                             `json:"complete"`
	Window     string                           `json:"window"`
	Sampled    bool                             `json:"sampled"`
	Warnings   []string                         `json:"warnings"`
}

// IndexConstraints makes at most three requests per 60-address batch, on
// demand. Partial/loading answers are not cached or polled here. All source
// fields remain available without asserting an unverified index simulation.
func (c *Client) IndexConstraints(ctx context.Context, addresses []string) (IndexConstraints, error) {
	ctx, cancel := context.WithTimeout(ctx, indexConstraintsTimeout)
	defer cancel()
	requested := map[string]bool{}
	for _, a := range addresses {
		addr, err := NormalizeAddress(a)
		if err != nil {
			return IndexConstraints{}, err
		}
		requested[addr] = true
	}
	if len(requested) > 200 {
		return IndexConstraints{}, fmt.Errorf("index constraints supports at most 200 distinct member addresses")
	}
	addrs := make([]string, 0, len(requested))
	for a := range requested {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	out := IndexConstraints{Members: []IndexConstraintMember{}, Sources: map[string]IndexConstraintSource{}, ObservedAt: c.opts.Now().UnixMilli(), Complete: true, Window: "all", Warnings: []string{}}
	members := map[string]*IndexConstraintMember{}
	for _, a := range addrs {
		members[a] = &IndexConstraintMember{Address: a, Capacity: []IndexCapacityPoint{}}
	}
	for _, action := range []string{"idxCap", "idxTracked", "idxDepClose"} {
		source := IndexConstraintSource{Complete: true, Missing: []string{}}
		for begin := 0; begin < len(addrs); begin += 60 {
			batch := addrs[begin:min(begin+60, len(addrs))]
			var wire struct {
				Rows       map[string][][]float64 `json:"rows"`
				Since      map[string]*int64      `json:"since"`
				Dates      map[string]*int64      `json:"dates"`
				Pending    int                    `json:"pending"`
				Incomplete bool                   `json:"incomplete"`
			}
			req := vaultsReq(action, ttlDetail, "vaults", strings.Join(batch, ","))
			req.cacheable = func(b []byte) bool {
				var s struct {
					Pending    int  `json:"pending"`
					Incomplete bool `json:"incomplete"`
				}
				return json.Unmarshal(b, &s) == nil && s.Pending == 0 && !s.Incomplete
			}
			info, err := c.getJSON(ctx, req, &wire)
			if err != nil {
				source.Complete = false
				source.Missing = append(source.Missing, batch...)
				out.Warnings = append(out.Warnings, action+" unavailable: "+err.Error())
				continue
			}
			wire.Rows = lowerConstraintKeys(wire.Rows)
			wire.Since = lowerConstraintKeys(wire.Since)
			wire.Dates = lowerConstraintKeys(wire.Dates)
			if !info.At.IsZero() && (source.FetchedAt == 0 || info.At.UnixMilli() < source.FetchedAt) {
				source.FetchedAt = info.At.UnixMilli()
			}
			source.Cached = source.Cached || info.Cached
			source.Stale = source.Stale || info.Stale
			source.Pending += wire.Pending
			source.Complete = source.Complete && !wire.Incomplete && wire.Pending == 0
			// Normalize only requested keys, excluding unrelated response rows.
			for _, a := range batch {
				m := members[a]
				switch action {
				case "idxCap":
					rows, ok := wire.Rows[a]
					if !ok {
						source.Missing = append(source.Missing, a)
						source.Complete = false
						continue
					}
					for _, r := range rows {
						if len(r) != 4 || !validCapacityRow(r) {
							source.Complete = false
							continue
						}
						m.Capacity = append(m.Capacity, IndexCapacityPoint{T: int64(r[0]), LeaderFraction: r[1], Equity: r[2], AllowDeposits: r[3] == 1, LeaderCapacityUSD: metrics.Capacity(r[2], r[1], a == HLPAddress)})
					}
					sort.SliceStable(m.Capacity, func(i, j int) bool { return m.Capacity[i].T < m.Capacity[j].T })
					if len(m.Capacity) > 0 {
						m.CapacityAsOf = m.Capacity[len(m.Capacity)-1].T
						m.CapacityStale = c.opts.Now().Sub(time.UnixMilli(m.CapacityAsOf)) > StaleAfter
					}
				case "idxTracked":
					m.TrackedSince = positiveEvent(wire.Since[a])
					if m.TrackedSince == nil {
						source.Missing = append(source.Missing, a)
						source.Complete = false
					}
				case "idxDepClose":
					m.DepositsClosedAt = positiveEvent(wire.Dates[a])
				}
			}
		}
		out.Sources[action] = source
		out.Complete = out.Complete && source.Complete
	}
	for _, a := range addrs {
		out.Members = append(out.Members, *members[a])
	}
	if !out.Complete {
		out.Warnings = append(out.Warnings, "Constraint sources are incomplete; missing dates do not establish deposit eligibility")
	}
	return out, nil
}

func positiveEvent(t *int64) *int64 {
	if t == nil || *t <= 0 {
		return nil
	}
	return t
}

func validCapacityRow(r []float64) bool {
	for _, v := range r {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return r[0] > 0 && r[0] <= 1<<53-1 && r[0] == math.Trunc(r[0]) && r[1] >= 0 && r[1] <= 1 && r[2] >= 0 && (r[3] == 0 || r[3] == 1)
}

// Keep future batching and network waits bounded even outside an interactive UI.
const indexConstraintsTimeout = 60 * time.Second

func lowerConstraintKeys[T any](values map[string]T) map[string]T {
	out := make(map[string]T, len(values))
	for a, v := range values {
		out[strings.ToLower(a)] = v
	}
	return out
}
