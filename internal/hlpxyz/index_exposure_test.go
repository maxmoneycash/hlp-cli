package hlpxyz

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestIndexExposureColumnsMatchDetailObservations(t *testing.T) {
	f := newFakeServer(t)
	f.set("/api/vaults?idxLev", func(w http.ResponseWriter, _ *http.Request) { w.Write(readFixture(t, "idx_lev.json")) })
	got, err := f.client().IndexExposure(context.Background(), []string{addrGarbage})
	if err != nil || !got.Complete || len(got.Members) != 1 {
		t.Fatalf("exposure read: %+v %v", got, err)
	}
	var detail VaultDetail
	if err := json.Unmarshal(readFixture(t, "vault_detail_light_lev.json"), &detail); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, p := range got.Members[0].Observations {
		for _, l := range detail.Leverage {
			if p.T != l.TS {
				continue
			}
			matched++
			for _, pair := range [][2]float64{{p.Equity, l.AccountValue}, {p.GrossNotional, l.TotalNtlPos}, {p.NetNotional, l.NetNtlPos}, {p.MarginUsed, l.TotalMarginUsed}} {
				if math.Abs(pair[0]-pair[1]) > 0.01 {
					t.Fatalf("column meaning disagrees at %d: got %v source %v", p.T, pair[0], pair[1])
				}
			}
		}
	}
	if matched < 2 {
		t.Fatalf("fixture did not verify multiple matching source observations: %d", matched)
	}
}

func TestIndexExposureBatchPresenceAndPendingCache(t *testing.T) {
	f := newFakeServer(t)
	addresses := make([]string, 61)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("0x%040x", i+1)
	}
	calls := 0
	f.set("/api/vaults?idxLev", func(w http.ResponseWriter, r *http.Request) {
		calls++
		batch := strings.Split(r.URL.Query().Get("vaults"), ",")
		if len(batch) > 60 {
			t.Errorf("unbounded exposure batch: %d", len(batch))
		}
		rows := map[string]any{}
		for _, a := range batch {
			rows[strings.ToUpper(a)] = [][]any{{1000, 10, 20, -15, 2}, {3000, 0, 0, 0, 0}, {2000, nil, 0, 0, 0}}
		}
		rows["unrequested"] = [][]any{{4000, 999, 999, 999, 999}}
		pending := 0
		if calls == 1 {
			pending = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"lev": rows, "pending": pending})
	})
	c := f.client()
	got, err := c.IndexExposure(context.Background(), append(addresses, addresses[0]))
	if err != nil || got.Complete || len(got.Members) != 61 || calls != 2 || got.Source.Pending != 1 {
		t.Fatalf("batch/pending: members=%d source=%+v calls=%d err=%v", len(got.Members), got.Source, calls, err)
	}
	m := got.Members[0]
	if len(m.Observations) != 2 || m.Observations[0].NetNotional != -15 || m.Observations[1].Equity != 0 || m.AsOf != 3000 || !m.Stale || m.Complete {
		t.Fatalf("null, signed or zero value changed meaning: %+v", m)
	}
	if got.Source.FetchedAt != fixtureNow.UnixMilli() || got.ObservedAt != fixtureNow.UnixMilli() {
		t.Fatalf("source and transport dates lost: %+v", got.Source)
	}
	_, err = c.IndexExposure(context.Background(), addresses)
	if err != nil || calls != 3 {
		t.Fatalf("pending response cached or hidden polling: calls=%d err=%v", calls, err)
	}
	large := make([]string, 201)
	for i := range large {
		large[i] = fmt.Sprintf("0x%040x", i+1)
	}
	if _, err := c.IndexExposure(context.Background(), large); err == nil || calls != 3 {
		t.Fatal("exposure member limit did not prevent requests")
	}
}

func TestIndexExposureMissingAndEmptyRemainDistinct(t *testing.T) {
	f := newFakeServer(t)
	f.set("/api/vaults?idxLev", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"lev": map[string]any{addrGarbage: []any{}}})
	})
	got, err := f.client().IndexExposure(context.Background(), []string{addrGarbage, addrBredo})
	if err != nil || got.Complete || len(got.Source.Missing) != 1 || got.Source.Missing[0] != addrBredo {
		t.Fatalf("missing coverage lost: %+v %v", got, err)
	}
	for _, m := range got.Members {
		if len(m.Observations) != 0 || m.AsOf != 0 || m.Complete != (m.Address == addrGarbage) {
			t.Fatalf("empty/missing fabricated exposure: %+v", m)
		}
	}
}
