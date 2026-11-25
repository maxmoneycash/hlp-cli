package core_test

import (
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/registry"
)

func TestDiscoverAndTags(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	res := mustInvoke(t, env, "discover_vaults", `{}`)
	d := res.Data.(*registry.Discovery)
	if res.Card != core.CardDiscover || d.Total < 20 || len(d.Kinds) == 0 || len(d.Groups) == 0 {
		t.Fatalf("discover: total %d kinds %d groups %d", d.Total, len(d.Kinds), len(d.Groups))
	}
	th := mustInvoke(t, env, "discover_vaults", `{"theses":true}`).Data.(*registry.Discovery)
	for _, g := range th.Groups {
		if !g.Thesis {
			t.Fatalf("theses kept group %s", g.Group)
		}
	}
	idx := mustInvoke(t, env, "discover_vaults", `{"kind":"indexes"}`).Data.(*registry.Discovery)
	for _, e := range idx.Entries {
		if e.Kind != registry.KindIndex {
			t.Fatalf("kind filter kept %s", e.ID)
		}
	}
	if _, err := invoke(t, env, "discover_vaults", `{"tag":["memez-nope"]}`); code(err) != core.CodeUsage {
		t.Fatalf("unknown tag: %v", err)
	}
	if _, err := invoke(t, env, "list_vaults", `{"tag":["nope-not-a-tag"]}`); code(err) != core.CodeUsage {
		t.Fatalf("list_vaults unknown tag: %v", err)
	}
	if _, err := invoke(t, env, "list_vaults", `{"kind":"index"}`); code(err) != core.CodeUsage {
		t.Fatalf("list_vaults kind index: %v", err)
	}
	// every row of a tag filter is a curated vault carrying the tag
	tags := env.Vaults
	if tags == nil {
		tags = registry.MustDefault()
	}
	byAddr := tags.TagsByAddress()
	vs := mustInvoke(t, env, "list_vaults", `{"tag":["memes"]}`).Data.([]domain.VaultSummary)
	if len(vs) == 0 {
		t.Fatal("no memes vaults")
	}
	for _, v := range vs {
		if !strings.Contains(strings.Join(byAddr[strings.ToLower(v.Address)], ","), "memes") {
			t.Fatalf("%s (%s) lacks the memes tag: %v", v.Name, v.Address, byAddr[strings.ToLower(v.Address)])
		}
	}
}

func TestVaultRedirectsCuratedNonVaults(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	_, err := invoke(t, env, "get_vault", `{"vault":"quant-spxmeme"}`)
	if e := core.AsError(err); e == nil || e.Code != core.CodeUsage || !strings.Contains(e.Hint, "strat") {
		t.Fatalf("want a hint to strat, got %+v", e)
	}
}
