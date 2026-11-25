package hlpxyz

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// IndexExposurePoint is one observed member-account snapshot from idxLev.
// Values are USD, not ratios; NetNotional is signed. These observations have
// not been weighted by an index allocation or filled between source dates.
type IndexExposurePoint struct {
	T             int64   `json:"t"`
	Equity        float64 `json:"equity"`
	GrossNotional float64 `json:"grossNotional"`
	NetNotional   float64 `json:"netNotional"`
	MarginUsed    float64 `json:"marginUsed"`
}

type IndexExposureMember struct {
	Address      string               `json:"address"`
	Observations []IndexExposurePoint `json:"observations"`
	AsOf         int64                `json:"asOf"` // newest source observation, independent of display clipping
	Stale        bool                 `json:"stale"`
	Complete     bool                 `json:"complete"` // source row was returned and decoded, not continuous calendar coverage
}

type IndexExposure struct {
	Members    []IndexExposureMember `json:"members"`
	Source     IndexConstraintSource `json:"source"` // idxLev transport/completeness metadata
	ObservedAt int64                 `json:"observedAt"`
	Complete   bool                  `json:"complete"`
	Window     string                `json:"window"`
	Sampled    bool                  `json:"sampled"`
	Warnings   []string              `json:"warnings"`
}

// IndexExposure reads only explicit requests in batches. No read of an index
// definition/list starts this work, and pending responses are never polled.
func (c *Client) IndexExposure(ctx context.Context, addresses []string) (IndexExposure, error) {
	ctx, cancel := context.WithTimeout(ctx, indexConstraintsTimeout)
	defer cancel()
	requested := map[string]bool{}
	for _, a := range addresses {
		addr, err := NormalizeAddress(a)
		if err != nil {
			return IndexExposure{}, err
		}
		requested[addr] = true
	}
	if len(requested) > 200 {
		return IndexExposure{}, fmt.Errorf("index exposure supports at most 200 distinct member addresses")
	}
	addrs := make([]string, 0, len(requested))
	for a := range requested {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	out := IndexExposure{Members: []IndexExposureMember{}, Source: IndexConstraintSource{Complete: true, Missing: []string{}}, ObservedAt: c.opts.Now().UnixMilli(), Complete: true, Window: "all", Warnings: []string{}}
	for begin := 0; begin < len(addrs); begin += 60 {
		batch := addrs[begin:min(begin+60, len(addrs))]
		var wire struct {
			Lev        map[string][][]*float64 `json:"lev"`
			Pending    int                     `json:"pending"`
			Incomplete bool                    `json:"incomplete"`
		}
		req := vaultsReq("idxLev", ttlDetail, "vaults", strings.Join(batch, ","))
		req.cacheable = func(b []byte) bool {
			var s struct {
				Pending    int  `json:"pending"`
				Incomplete bool `json:"incomplete"`
			}
			return json.Unmarshal(b, &s) == nil && s.Pending == 0 && !s.Incomplete
		}
		info, err := c.getJSON(ctx, req, &wire)
		if err != nil {
			out.Source.Complete = false
			out.Source.Missing = append(out.Source.Missing, batch...)
			out.Warnings = append(out.Warnings, "idxLev unavailable: "+err.Error())
			for _, a := range batch {
				out.Members = append(out.Members, IndexExposureMember{Address: a, Observations: []IndexExposurePoint{}})
			}
			continue
		}
		if !info.At.IsZero() && (out.Source.FetchedAt == 0 || info.At.UnixMilli() < out.Source.FetchedAt) {
			out.Source.FetchedAt = info.At.UnixMilli()
		}
		out.Source.Cached = out.Source.Cached || info.Cached
		out.Source.Stale = out.Source.Stale || info.Stale
		out.Source.Pending += wire.Pending
		out.Source.Complete = out.Source.Complete && !wire.Incomplete && wire.Pending == 0
		wire.Lev = lowerConstraintKeys(wire.Lev)
		for _, a := range batch {
			rows, ok := wire.Lev[a]
			m := IndexExposureMember{Address: a, Observations: []IndexExposurePoint{}, Complete: ok && !wire.Incomplete && wire.Pending == 0}
			if !ok {
				out.Source.Missing = append(out.Source.Missing, a)
			}
			for _, r := range rows {
				if !validExposureRow(r) {
					m.Complete = false
					continue
				}
				m.Observations = append(m.Observations, IndexExposurePoint{T: int64(*r[0]), Equity: *r[1], GrossNotional: *r[2], NetNotional: *r[3], MarginUsed: *r[4]})
			}
			sort.SliceStable(m.Observations, func(i, j int) bool { return m.Observations[i].T < m.Observations[j].T })
			if len(m.Observations) > 0 {
				m.AsOf = m.Observations[len(m.Observations)-1].T
				m.Stale = c.opts.Now().Sub(time.UnixMilli(m.AsOf)) > StaleAfter
			}
			out.Source.Complete = out.Source.Complete && m.Complete
			out.Members = append(out.Members, m)
		}
	}
	out.Complete = out.Source.Complete
	if !out.Complete {
		out.Warnings = append(out.Warnings, "Member exposure source is incomplete; missing observations are not zero exposure")
	}
	return out, nil
}

func validExposureRow(r []*float64) bool {
	if len(r) != 5 {
		return false
	}
	for _, v := range r {
		if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
			return false
		}
	}
	// Equity and net notional may be negative. Preserve them without dividing
	// by equity or treating a zero account as missing.
	return *r[0] > 0 && *r[0] <= 1<<53-1 && *r[0] == math.Trunc(*r[0]) && *r[2] >= 0 && *r[4] >= 0
}
