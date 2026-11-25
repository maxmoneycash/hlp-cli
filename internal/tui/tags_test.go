package tui

import (
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
)

func TestTagCompletion(t *testing.T) {
	env := &core.Env{Vaults: registry.MustDefault()}
	for _, c := range []struct{ in, want string }{
		{"/discover market-n", "/discover market-neutral"},
		{"/vaults --tag tre", "/vaults --tag trend"},
		{"/vaults --tag=tre", "/vaults --tag=trend"},
	} {
		got, _, ok := tagCompletion(env, c.in)
		if !ok || got != c.want {
			t.Errorf("tagCompletion(%q) = %q %v, want %q", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"/vaults tre", "/chart BTC", "/discover ", "discover mark"} {
		if _, _, ok := tagCompletion(env, in); ok {
			t.Errorf("tagCompletion(%q) completed", in)
		}
	}
}

func TestSlashPrefixBeatsAlias(t *testing.T) {
	cmds := []command{{slash: "close", aliases: []string{"flatten"}}, {slash: "fills"}, {slash: "fund-user"}}
	got := matchCommands(cmds, "/f")
	if len(got) < 3 || got[0].slash != "fills" || got[2].slash != "close" {
		t.Fatalf("order %v", got)
	}
}
