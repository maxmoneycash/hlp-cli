package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// D-T7 / AGENT-UX #13-#15: get_vault hlp has a real createdAt, a stale
// flag always comes with its reason, and the returns model is named.
func TestGetVaultHLPContract(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	res := mustInvoke(t, env, "get_vault", `{"vault":"hlp"}`)
	d := res.Data.(domain.VaultDetail)
	if d.Summary.CreatedAt.IsZero() || d.Summary.CreatedAt.Year() < 2020 {
		t.Fatalf("createdAt %v", d.Summary.CreatedAt)
	}
	if res.Meta.Stale {
		found := false
		for _, w := range res.Meta.Warnings {
			found = found || strings.Contains(w, "last updated")
		}
		if !found {
			t.Fatalf("stale without a reason: %v", res.Meta.Warnings)
		}
	}
	if len(d.Summary.Returns) > 0 && d.Summary.ReturnsModel == "" {
		t.Fatal("returnsModel missing")
	}
	if d.Summary.SPModel == "" {
		t.Fatal("sharePriceModel missing")
	}
	if d.Description != "" && !strings.HasPrefix(d.Description, core.UntrustedPrefix) {
		t.Fatalf("description not marked: %q", d.Description)
	}
}

// AGENT-UX #11: list_indexes is typed.
func TestListIndexesTyped(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	list := mustInvoke(t, env, "list_indexes", `{}`).Data.(*core.IndexData)
	if len(list.Indexes) == 0 || list.Index != nil {
		t.Fatalf("%+v", list)
	}
	one := mustInvoke(t, env, "list_indexes", `{"slug":"mavericks"}`).Data.(*core.IndexData)
	if one.Index == nil || one.Index.Def.Slug != "mavericks" {
		t.Fatalf("%+v", one)
	}
	for _, name := range []string{"list_indexes", "get_funding"} {
		tl, _ := core.Default().Lookup(name)
		if s := core.Describe(tl); s.Output == nil {
			t.Errorf("%s: no output schema (%s)", name, s.OutputType)
		}
	}
}

// staleXYZ serves a vault detail whose history stopped updating 201 days
// ago (HLP's hlp.xyz history since April).
type staleXYZ struct{ *coretest.FakeXYZ }

func (staleXYZ) VaultDetail(context.Context, string, hlpxyz.DetailOpts) (hlpxyz.VaultDetail, error) {
	var d hlpxyz.VaultDetail
	d.Meta.LastUpdated = coretest.Now.Add(-201 * 24 * time.Hour).UnixMilli()
	return d, nil
}

// Activity history and v6 NAV are independent sources. A fresh NAV must
// survive an older history cache; both ages remain visible.
func TestStaleActivityKeepsFreshNAV(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	env.HLPXYZ = staleXYZ{&coretest.FakeXYZ{}}
	res := mustInvoke(t, env, "get_vault", `{"vault":"`+coretest.Garbage+`"}`)
	d := res.Data.(domain.VaultDetail)
	if res.Meta.Stale || d.SPModel != domain.SPv6 || !d.Freshness["history"].Stale || d.Freshness["nav"].Stale {
		t.Fatalf("model=%v source ages=%+v warnings=%v", d.SPModel, d.Freshness, res.Meta.Warnings)
	}
	if !strings.Contains(strings.Join(res.Meta.Warnings, " "), "activity history") {
		t.Fatal("missing activity age warning")
	}
}

type staleNAVXYZ struct{ staleXYZ }

func (s staleNAVXYZ) NAV(ctx context.Context, address string) (hlpxyz.NAVCurve, error) {
	n, err := s.FakeXYZ.NAV(ctx, address)
	n.BuiltAt = coretest.Now.Add(-72 * time.Hour).UnixMilli()
	return n, err
}

func TestStaleNAVUsesFreshPortfolio(t *testing.T) {
	env := coretest.Env(t, staleNAVXYZ{staleXYZ{&coretest.FakeXYZ{}}})
	res := mustInvoke(t, env, "get_vault", `{"vault":"`+coretest.Garbage+`"}`)
	d := res.Data.(domain.VaultDetail)
	if res.Meta.Stale || d.SPModel != domain.SPHL {
		t.Fatalf("stale %v model %v warnings %v", res.Meta.Stale, d.SPModel, res.Meta.Warnings)
	}
	found := false
	for _, w := range res.Meta.Warnings {
		found = found || strings.HasPrefix(w, "curve from the Hyperliquid portfolio")
	}
	if !found {
		t.Fatalf("no source note: %v", res.Meta.Warnings)
	}
}
