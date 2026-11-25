package cards

import (
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

func TestStatusCard(t *testing.T) {
	ok := &core.Result{Card: core.CardStatus, Title: "Status", Data: &core.StatusData{OK: true, Checks: []core.StatusCheck{
		{Name: "info", OK: true, LatencyMs: 182, Detail: "api.hyperliquid.xyz"},
		{Name: "config", OK: true, Detail: "~/.config/hlp/config.toml"},
	}}}
	out := Render(ok, plainOpts(80))
	if !strings.Contains(strings.Split(out, "\n")[0], "OK") || !strings.Contains(out, "182MS") {
		t.Fatalf("healthy:\n%s", out)
	}
	bad := &core.Result{Card: core.CardStatus, Title: "Status", Data: &core.StatusData{Checks: []core.StatusCheck{
		{Name: "info", OK: true, LatencyMs: 90},
		{Name: "ws", OK: false, Detail: "handshake timed out"},
	}}}
	out = Render(bad, plainOpts(40))
	if !strings.Contains(out, "1 FAILED") || !strings.Contains(out, "handshake timed out") {
		t.Fatalf("failed:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if len([]rune(l)) > 40 {
			t.Fatalf("overflow: %q", l)
		}
	}
}
