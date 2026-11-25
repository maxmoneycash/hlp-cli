package hlpxyz

import (
	"strings"
	"testing"
	"time"
)

const osc52 = "Nice vault\x1b]52;c;MHhkZWFk\x07\u202e"

// SECURITY S3: third-party text loses terminal controls at ingestion.
func TestMappersCleanText(t *testing.T) {
	now := time.UnixMilli(1794744000000)
	var det VaultDetail
	det.VaultInfo.Name, det.VaultInfo.Desc = osc52, "line one\nline\u009b two"
	det.Meta.LastUpdated = now.Add(-72 * time.Hour).UnixMilli()
	d, err := ToDetail(DetailParts{Address: "0x" + strings.Repeat("a", 40), Detail: &det, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary.Name != "Nice vault]52;c;MHhkZWFk" || strings.ContainsAny(d.Description, "\x1b\u009b") || !strings.Contains(d.Description, "\n") {
		t.Fatalf("name %q desc %q", d.Summary.Name, d.Description)
	}
	// COMP #7a / AGENT-UX #14: the stamp is the history's lastUpdated, and
	// the stale flag comes with its reason.
	if !d.Meta.Stale || !d.Summary.UpdatedAt.Equal(time.UnixMilli(det.Meta.LastUpdated)) {
		t.Fatalf("stale %v updatedAt %v", d.Meta.Stale, d.Summary.UpdatedAt)
	}
	found := false
	for _, w := range d.Meta.Warnings {
		found = found || strings.Contains(w, "last updated "+ShortDate(time.UnixMilli(det.Meta.LastUpdated), now)+" (3d ago, over 48h)")
	}
	if !found {
		t.Fatalf("no stale reason in %v", d.Meta.Warnings)
	}

	rows := MapVaultList(SummaryResponse{Vaults: []SummaryVault{{Address: "0x" + strings.Repeat("b", 40), Name: osc52}}}, nil, nil, nil, now)
	if rows[0].Name != "Nice vault]52;c;MHhkZWFk" {
		t.Fatalf("list name %q", rows[0].Name)
	}

	ix := Index{Def: IndexDef{Name: osc52, Description: "a\x1b[2Jb"}, Allocs: []IndexAlloc{{Notes: []string{"\x07x"}}}}
	ix.clean()
	if strings.ContainsAny(ix.Def.Name+ix.Def.Description+ix.Allocs[0].Notes[0], "\x1b\x07\u202e") {
		t.Fatalf("index %+v", ix)
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{-time.Second: "0m", 30 * time.Minute: "30m", 7 * time.Hour: "7h", 47 * time.Hour: "47h", 80 * time.Hour: "3d"} {
		if got := HumanAge(d); got != want {
			t.Errorf("%v: %s, want %s", d, got, want)
		}
	}
}
