package tui

import (
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
)

// idleModel is a 140x42 shell with three cards and nothing moving: the
// frame an idle terminal redraws (no allocation added).
func idleModel(tb testing.TB) *Model {
	m := benchModel(tb, 140, 42)
	for i := 0; i < 3; i++ {
		m.add(&entry{kind: entryUser, text: "/vaults"})
		m.add(&entry{kind: entryCard, result: &core.Result{Card: core.CardVaults, Data: sampleVaults(), Meta: domain.Meta{Source: "fixture"}},
			call: core.Call{Tool: "list_vaults"}})
	}
	m.render() // warm the entry caches
	return m
}

// BenchmarkIdleFrame renders the whole screen with nothing changed.
func BenchmarkIdleFrame(b *testing.B) {
	m := idleModel(b)
	b.ReportAllocs()
	for b.Loop() {
		m.render()
	}
}

// BenchmarkWelcomeFrame renders the first screen.
func BenchmarkWelcomeFrame(b *testing.B) {
	m := benchModel(b, 140, 42)
	m.render()
	b.ReportAllocs()
	for b.Loop() {
		m.render()
	}
}
