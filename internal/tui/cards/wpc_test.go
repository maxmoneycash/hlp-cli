package cards

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

func TestPriceCard(t *testing.T) {
	one := &core.Result{Card: core.CardPrice, Title: "BTC", Data: []core.PriceRow{
		{Coin: "BTC", Mid: 73500, Mark: 73502, Bid: 73499, Ask: 73501, Change24h: 0.0208, Funding: 0.0000125},
	}}
	out := Render(one, plainOpts(100))
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "73,500") || !strings.Contains(lines[0], "+2.1%  24H") {
		t.Fatalf("focal line: %q", lines[0])
	}
	for _, want := range []string{"BID 73,499", "ASK 73,501", "MARK 73,502", "1H +0.0013%"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	many := &core.Result{Card: core.CardPrice, Title: "Prices", Data: []core.PriceRow{
		{Coin: "BTC", Mid: 73500, Bid: 73499, Ask: 73501, Change24h: 0.02},
		{Coin: "ETH", Mid: 2600, Change24h: -0.037, Funding: -0.00002},
	}}
	out = Render(many, plainOpts(100))
	if strings.Count(out, "\n") > 5 || !strings.Contains(out, "ETH") || !strings.Contains(out, "-3.7%") {
		t.Fatalf("one line per coin:\n%s", out)
	}
	for _, w := range []int{40, 60} {
		for _, l := range strings.Split(Render(many, plainOpts(w)), "\n") {
			if len([]rune(l)) > w {
				t.Fatalf("width %d overflow: %q", w, l)
			}
		}
	}
}

// S3 defence in depth: no escape sequence from data survives Render except
// SGR styling, styled or plain.
func TestRenderDropsTerminalEscapes(t *testing.T) {
	evil := "Nice vault\x1b]52;c;MHhkZWFk\x07\x1b[2J\x1b]0;title\x1b\\‮​ok\x9b"
	r := &core.Result{Card: core.CardPrice, Title: evil, Data: []core.PriceRow{{Coin: evil, Mid: 1}, {Coin: "B", Mid: 2}},
		Meta: domain.Meta{Warnings: []string{evil}}}
	for _, o := range []Opts{plainOpts(100), {Width: 100, Theme: theme.Default(true), Profile: colorprofile.TrueColor}} {
		out := Render(r, o)
		for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "‮", "​", "\x9b", "\u009b"} {
			if strings.Contains(out, bad) {
				t.Fatalf("plain=%v: %q survived:\n%q", o.Plain, bad, out)
			}
		}
		if o.Plain && strings.Contains(out, "\x1b") {
			t.Fatalf("plain output has an escape: %q", out)
		}
		if !o.Plain && !strings.Contains(out, "\x1b[") {
			t.Fatal("styled output lost its SGR styling")
		}
		if !strings.Contains(out, "Nice vault") {
			t.Fatal("text dropped")
		}
	}
	if got := safeTerminal("a\x1b[1;38;2;1;2;3mb\x1b[0m\x1b[5Ac\td\n", false); got != "a\x1b[1;38;2;1;2;3mb\x1b[0mc\td\n" {
		t.Fatalf("sgr kept, cursor move dropped: %q", got)
	}
}

func TestStampZoneAndFooter(t *testing.T) {
	ts := time.Date(2026, 11, 15, 12, 39, 57, 0, time.UTC)
	if got := StampZone(ts, time.UTC); got != "Nov 15 12:39:57 UTC" {
		t.Fatalf("utc: %q", got)
	}
	pst := time.FixedZone("PST", -8*3600)
	if got := StampZone(ts, pst); got != "Nov 15 04:39:57 PST" {
		t.Fatalf("pst: %q", got)
	}
	r := &core.Result{Card: core.CardText, Data: "x", Meta: domain.Meta{Source: "hyperliquid", AsOf: ts}}
	o := plainOpts(100)
	o.Location, o.ZoneLabel = pst, true
	if out := Render(r, o); !strings.Contains(out, "04:39:57 PST") {
		t.Fatalf("footer: %s", out)
	}
	o.ZoneLabel = false
	if out := Render(r, o); strings.Contains(out, "PST") {
		t.Fatalf("no label unless asked: %s", out)
	}
}

func TestToLiqFarAway(t *testing.T) {
	a := domain.Account{Address: "0xabc", Equity: 1000, Positions: []domain.Position{
		{Coin: "LTC", Size: -1, Entry: 45, Mark: 45.42, Value: 45.42, LiqPx: 12960, Leverage: 1},
		{Coin: "BTC", Size: 0.1, Entry: 70000, Mark: 73500, Value: 7350, LiqPx: 60000, Leverage: 5, Cross: true},
	}}
	out := Render(&core.Result{Card: core.CardAccount, Title: "Account", Data: a}, plainOpts(160))
	if strings.Contains(out, "28433") || strings.Contains(out, "285X") || !strings.Contains(out, ">999%") {
		t.Fatalf("to liq:\n%s", out)
	}
	if !strings.Contains(out, "18.4%") {
		t.Fatalf("near liq keeps percent:\n%s", out)
	}
}

func TestChartCardHints(t *testing.T) {
	cs := make([]domain.Candle, 200)
	for i := range cs {
		p := 100 + float64(i%7)
		cs[i] = domain.Candle{T: int64(i) * 900_000, O: p, H: p + 2, L: p - 2, C: p + 1, V: 10}
	}
	d := &core.ChartData{Coin: "BTC", Interval: domain.I15m, Candles: cs, Height: 40, Style: "ascii", Zoom: 1}
	out := Render(&core.Result{Card: core.CardChart, Data: d}, plainOpts(120))
	n := len(strings.Split(out, "\n"))
	if n < 41 || n > 44 {
		t.Fatalf("height 40 gave %d lines", n)
	}
	for _, r := range "▀▄█▌▐│┃" {
		if strings.ContainsRune(out, r) {
			t.Fatalf("ascii style printed %q", r)
		}
	}
	d.Height = 0
	if n := len(strings.Split(Render(&core.Result{Card: core.CardChart, Data: d}, plainOpts(120)), "\n")); n > 20 {
		t.Fatalf("default inline height grew: %d", n)
	}
}

func TestSchemaCard(t *testing.T) {
	list := []core.ToolSchema{{Name: "get_price", Command: "price", Summary: "Current price"}, {Name: "schema", Command: "schema", Summary: "Schemas"}}
	out := Render(&core.Result{Card: core.CardSchema, Title: "Schema", Data: list}, plainOpts(100))
	if !strings.Contains(out, "get_price") || strings.Contains(out, "{") {
		t.Fatalf("list:\n%s", out)
	}
}
