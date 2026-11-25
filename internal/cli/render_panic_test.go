package cli

import (
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
)

// A card renderer that panics (the shoot's negative strings.Repeat in the
// vault table at 100 columns) must not crash the CLI: the result is printed
// as JSON with a notice.
func TestRenderCardRecoversPanic(t *testing.T) {
	orig := renderCardFn
	t.Cleanup(func() { renderCardFn = orig })
	renderCardFn = func(*core.Result, cards.Opts) string { panic("strings: negative Repeat count") }
	a := &app{io: IO{Width: 100, Getenv: func(string) string { return "" }}, g: &globals{}, reg: core.Default()}
	out := a.renderCard(&core.Env{}, &core.Result{Card: core.CardVaults, Title: "Vaults", Data: []map[string]any{{"name": "HLP"}}}, Mode{Plain: true})
	if !strings.Contains(out, "could not draw the vaults card at 100 columns") || !strings.Contains(out, `"HLP"`) {
		t.Fatalf("fallback output:\n%s", out)
	}
}

// Free-form questions are not run as a vault name search; with no model the
// error names the closest command.
func TestParseLineQuestionSuggestsCommand(t *testing.T) {
	a := &app{io: IO{Getenv: func(string) string { return "" }}, g: &globals{}, reg: core.Default()}
	_, err := a.parseLine("which vaults have the best risk-adjusted returns over the last 90 days?")
	ce := core.AsError(err)
	if ce == nil || !strings.Contains(ce.Hint, "/vaults --sort sharpe") {
		t.Fatalf("want a /vaults --sort sharpe hint, got %v", err)
	}
}
