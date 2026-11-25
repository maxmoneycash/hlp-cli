package hlpxyz

import (
	"encoding/json"
	"testing"
)

func TestLiveEquityZeroDiffersFromMissing(t *testing.T) {
	var zero, missing LiveVault
	if err := json.Unmarshal([]byte(`{"address":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","totalEquity":0}`), &zero); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"address":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), &missing); err != nil {
		t.Fatal(err)
	}
	summary := SummaryResponse{Vaults: []SummaryVault{{Address: zero.Address, Equity: 100}}}
	got := MapVaultList(summary, nil, nil, []LiveVault{zero}, fixtureNow)
	if len(got) != 1 || got[0].TVL != 0 || !got[0].TVLKnown {
		t.Fatalf("zero live equity kept stale summary: %+v", got)
	}
	got = MapVaultList(summary, nil, nil, []LiveVault{missing}, fixtureNow)
	if len(got) != 1 || got[0].TVL != 100 {
		t.Fatalf("missing live equity erased known value: %+v", got)
	}
}

func TestDetailPreservesCompleteNAVLossAndZeroEquity(t *testing.T) {
	var nav NAVCurve
	if err := json.Unmarshal([]byte(`{"navNow":0,"curve":[{"ts":1000,"nav":1},{"ts":2000,"nav":0}]}`), &nav); err != nil {
		t.Fatal(err)
	}
	var live VaultLive
	if err := json.Unmarshal([]byte(`{"totalEquity":0,"sharePrice":2}`), &live); err != nil {
		t.Fatal(err)
	}
	detail := VaultDetail{}
	detail.Portfolio.Snapshots = []Snapshot{{TS: 1000, AV: 100}}
	d, err := ToDetail(DetailParts{Address: addrGarbage, Detail: &detail, Live: &live, NAV: &nav, Now: fixtureNow})
	if err != nil || len(d.NAV) != 2 || d.NAV[1].V != 0 || d.SharePrice != 0 || d.Summary.TVL != 0 || !d.Summary.TVLKnown {
		t.Fatalf("complete loss suppressed: %+v %v", d, err)
	}
	var absent VaultLive
	if err := json.Unmarshal([]byte(`{"sharePrice":2}`), &absent); err != nil {
		t.Fatal(err)
	}
	d, err = ToDetail(DetailParts{Address: addrGarbage, Detail: &detail, Live: &absent, Now: fixtureNow})
	if err != nil || d.Summary.TVL != 100 {
		t.Fatalf("absent value erased known history: %+v %v", d, err)
	}
}
