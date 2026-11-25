package hlpxyz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIndexConstraintsBatchPendingCacheAndUnknownClosure(t *testing.T) {
	f := newFakeServer(t)
	addresses := make([]string, 61)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("0x%040x", i+1)
	}
	calls := map[string]int{}
	for _, action := range []string{"idxCap", "idxTracked", "idxDepClose"} {
		f.set("/api/vaults?"+action, func(w http.ResponseWriter, r *http.Request) {
			calls[action]++
			batch := strings.Split(r.URL.Query().Get("vaults"), ",")
			if len(batch) > 60 {
				t.Errorf("unbounded batch: %d", len(batch))
			}
			switch action {
			case "idxCap":
				rows := map[string][][]float64{}
				for _, a := range batch {
					rows[strings.ToUpper(a)] = [][]float64{{1000, 0.1, 100, 0}, {2000, 0.05, 0, 1}}
				}
				rows["unrequested"] = [][]float64{{3000, 1, 999, 1}}
				json.NewEncoder(w).Encode(map[string]any{"rows": rows, "pending": 0})
			case "idxTracked":
				since := map[string]int64{}
				for _, a := range batch {
					since[a] = 123
				}
				json.NewEncoder(w).Encode(map[string]any{"since": since, "pending": 0})
			case "idxDepClose":
				pending := 0
				if calls[action] == 1 {
					pending = 1
				}
				json.NewEncoder(w).Encode(map[string]any{"dates": map[string]int64{addresses[0]: 500}, "pending": pending, "incomplete": false})
			}
		})
	}
	c := f.client()
	got, err := c.IndexConstraints(context.Background(), append(addresses, addresses[0]))
	if err != nil || got.Complete || len(got.Members) != 61 || calls["idxCap"] != 2 || calls["idxTracked"] != 2 || calls["idxDepClose"] != 2 {
		t.Fatalf("batch %+v calls %v err %v", got, calls, err)
	}
	m := got.Members[0]
	if len(m.Capacity) != 2 || m.Capacity[0].AllowDeposits || m.Capacity[0].LeaderCapacityUSD != 100 || m.Capacity[1].Equity != 0 || m.CapacityAsOf != 2000 || !m.CapacityStale || m.TrackedSince == nil || *m.TrackedSince != 123 || m.DepositsClosedAt == nil || *m.DepositsClosedAt != 500 {
		t.Fatalf("source meaning changed: %+v", m)
	}
	if got.Members[1].DepositsClosedAt != nil {
		t.Fatal("unknown closure became a made-up date")
	}
	if got.Sources["idxCap"].FetchedAt != fixtureNow.UnixMilli() || got.ObservedAt != fixtureNow.UnixMilli() {
		t.Fatalf("transport time lost: %+v", got.Sources)
	}
	// Complete source chunks are cached; only the pending chunk is fetched
	// again, on this explicit second call. No hidden polling occurs.
	again, err := c.IndexConstraints(context.Background(), addresses)
	if err != nil || !again.Complete || calls["idxCap"] != 2 || calls["idxTracked"] != 2 || calls["idxDepClose"] != 3 || !again.Sources["idxCap"].Cached {
		t.Fatalf("pending cache: %+v %v %v", again.Sources, calls, err)
	}
}

func TestIndexConstraintsRejectsMalformedCapacityAndBoundsInput(t *testing.T) {
	f := newFakeServer(t)
	for route, body := range map[string]any{
		"/api/vaults?idxCap":      map[string]any{"rows": map[string]any{addrGarbage: [][]float64{{1000, 0.1, 100}, {2000, 0.1, 100, 3}}}},
		"/api/vaults?idxTracked":  map[string]any{"since": map[string]int64{addrGarbage: 123}},
		"/api/vaults?idxDepClose": map[string]any{"dates": map[string]int64{}},
	} {
		f.set(route, func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(body) })
	}
	c := f.client()
	got, err := c.IndexConstraints(context.Background(), []string{addrGarbage})
	if err != nil || got.Complete || len(got.Members) != 1 || len(got.Members[0].Capacity) != 0 {
		t.Fatalf("invalid source row became data: %+v %v", got, err)
	}
	if _, err := c.IndexConstraints(context.Background(), []string{"not-an-address"}); err == nil {
		t.Fatal("invalid address accepted")
	}
	large := make([]string, 201)
	for i := range large {
		large[i] = fmt.Sprintf("0x%040x", i+1)
	}
	if _, err := c.IndexConstraints(context.Background(), large); err == nil {
		t.Fatal("unbounded member batch accepted")
	}
}
