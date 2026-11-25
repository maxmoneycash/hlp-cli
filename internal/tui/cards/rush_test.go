package cards

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// rushOpts is an inline card in the default theme at TrueColor.
func rushOpts(w int) Opts {
	th, _ := theme.Get(theme.NameRush)
	return Opts{Width: w, Theme: th, Profile: colorprofile.TrueColor, Compact: true, Location: time.UTC}
}

// DESIGN.md section 5.1: no footers, no hint walls, no AI tells, only the
// kit's marks. Checked on every inline card in rush at TrueColor.
func TestRushInlineHasNoBannedText(t *testing.T) {
	stubCharts(t)
	banned := []string{"as of", "As of", "try ", "—", "–", "✓", "✗", "•", "∞", "enter opens", "more ·"}
	for name, r := range fixtureResults(t) {
		s := stripANSI(Render(r, rushOpts(104)))
		for _, b := range banned {
			if strings.Contains(s, b) {
				t.Errorf("%s contains %q:\n%s", name, b, s)
			}
		}
	}
	e := stripANSI(Error(&core.Error{Code: core.CodeUnknownCoin, Message: "unknown coin X", Hint: "list markets with `hlp markets --query X`"}, rushOpts(104)))
	for _, b := range append(banned, "UNKNOWN_COIN", "✗") {
		if strings.Contains(e, b) {
			t.Errorf("error card contains %q:\n%s", b, e)
		}
	}
}

var reCols = regexp.MustCompile(`\S+(?: \S+)*`)

// isHeader reports whether a line is a table header: two or more
// UPPERCASE labels separated by wide gaps.
func isHeader(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" || t != strings.ToUpper(t) || !strings.Contains(t, "  ") {
		return false
	}
	for _, f := range strings.Fields(t) {
		if f != "#" && !reLabel.MatchString(f) {
			return false
		}
	}
	return true
}

var reLabel = regexp.MustCompile(`^([A-Z]|[A-Z0-9][A-Z0-9/]*[A-Z/])$`)

// DESIGN.md section 5.1 rule 9: an inline table shows at most 6 columns
// and 5 rows (8 for positions), and a card has at most one meta line
// between its header and its body.
func TestInlineTableLimits(t *testing.T) {
	stubCharts(t)
	tables := 0
	defer func() {
		if tables < 8 {
			t.Errorf("found %d inline tables; the header detector is broken", tables)
		}
	}()
	for name, r := range fixtureResults(t) {
		if name == "kv" || name == "fallback" || name == "text" || name == "order" {
			continue // not tables: key/value pairs, a dump, prose, the confirm card
		}
		lines := strings.Split(stripANSI(Render(r, rushOpts(104))), "\n")
		maxRows := 5
		if name == "account" || name == "paper" {
			maxRows = 8
		}
		for i, l := range lines {
			if i == 0 || !isHeader(l) {
				continue // line 0 is the card title, not a table header
			}
			tables++
			if n := len(reCols.FindAllString(strings.TrimSpace(l), -1)); n > maxInlineCols {
				t.Errorf("%s: %d columns in %q", name, n, l)
			}
			rows := 0
			for _, r := range lines[i+1:] {
				t := strings.TrimSpace(r)
				if t == "" || strings.HasPrefix(t, "+") && !strings.Contains(t, " ") || strings.HasPrefix(t, "!") {
					break
				}
				rows++
			}
			if rows > maxRows {
				t.Errorf("%s: %d rows under %q", name, rows, l)
			}
		}
	}
}

// The panic at table.go (strings.Repeat with a negative count) came from a
// column narrower than its text; any width must render.
func TestTableNeverPanics(t *testing.T) {
	p := newPainter(plainOpts(100).normalized())
	cols := []column{{title: "vault", flex: true, minW: 12, keep: 100, mid: true}, {title: "tvl", right: true, keep: 50}, {title: "", keep: 1}}
	rows := [][]cell{{p.c("Systemic Strategies HyperGrowth"), p.c("$1.2M"), p.c("")}, {p.c("x"), p.c(""), p.c("")}}
	for w := -4; w <= 40; w++ {
		out := p.table(cols, rows, w, nil)
		if len(out) != 3 {
			t.Fatalf("width %d: %d lines", w, len(out))
		}
	}
	// a row with fewer cells than columns, and a mark gutter
	_ = p.table(cols, [][]cell{{p.c("only")}}, 3, func(int) string { return ">" })
}

// Plain output (CLI --plain, MCP text) keeps the ASCII minus; styled
// output uses U+2212.
func TestMinusSign(t *testing.T) {
	stubCharts(t)
	for name, r := range fixtureResults(t) {
		if s := Render(r, plainOpts(100)); strings.Contains(s, minus) {
			t.Errorf("%s: plain output has U+2212", name)
		}
	}
	s := stripANSI(Render(fixtureResults(t)["chart"], rushOpts(104)))
	if !strings.Contains(s, minus+"2.6%") || strings.Contains(s, " -2.6%") {
		t.Errorf("styled chart change should carry U+2212:\n%s", s)
	}
}

// Max drawdown is a negative number by definition: ink, red only past the
// cap (DESIGN.md section 2 rule 3).
func TestDrawdownInkUntilCap(t *testing.T) {
	p := newPainter(rushOpts(100).normalized())
	if p.ddStyle(-0.21).Render("x") != p.st.text.Render("x") {
		t.Error("-21% drawdown should be ink")
	}
	if p.ddStyle(-0.31).Render("x") != p.st.down.Render("x") {
		t.Error("-31% drawdown should be red")
	}
}

func TestFocal(t *testing.T) {
	stubCharts(t)
	rs := fixtureResults(t)
	cases := []struct {
		name, big, small string
		neg              bool
	}{
		{"chart", "65462", minus + "2.6%  24H", false},
		{"vault", "+93.2", "%  ALL", false},
		{"account", "1.7", "M  " + minus + "$3,540", false},
		{"backtest", "+93.2", "%  420D", false},
		{"arena", "+93.2", "%  qwen3 (local)", false},
	}
	for _, c := range cases {
		v, big, small, ok := Focal(rs[c.name], rushOpts(104))
		if !ok || big != c.big || small != c.small || (v < 0) != c.neg {
			t.Errorf("%s: Focal = %v %q %q %v, want %q %q", c.name, v, big, small, ok, c.big, c.small)
		}
	}
	if _, _, _, ok := Focal(rs["markets"], rushOpts(104)); ok {
		t.Error("a count is not a hero number")
	}
	if _, _, _, ok := Focal(nil, rushOpts(104)); ok {
		t.Error("nil result")
	}
	// Hero leaves the header focal to the pane.
	o := rushOpts(104)
	o.Compact, o.Hero = false, true
	head := strings.Split(stripANSI(Render(rs["chart"], o)), "\n")[0]
	if strings.Contains(head, "65,462") {
		t.Errorf("hero card keeps its focal: %q", head)
	}
}

// Plumbing never reaches the card; the inline tag names what it knows.
func TestWarningTags(t *testing.T) {
	r := &core.Result{Card: core.CardKV, Title: "x", Data: [][2]string{{"a", "b"}}, Meta: domain.Meta{Warnings: []string{
		"rpc https://rpc.hyperliquid-testnet.xyz/evm failed; retried via lens",
		"49 order(s) clipped by margin (leverage above min(script, maxLeverage))",
		"something else entirely",
	}}}
	s := stripANSI(Render(r, rushOpts(80)))
	if !strings.Contains(s, "! 49 ORDERS CLIPPED +1") || strings.Contains(s, "rpc") {
		t.Errorf("inline tags:\n%s", s)
	}
	o := rushOpts(80)
	o.Compact = false
	s = stripANSI(Render(r, o))
	if !strings.Contains(s, "NOTES") || !strings.Contains(s, "something else entirely") || strings.Contains(s, "rpc") {
		t.Errorf("full notes:\n%s", s)
	}
	for w, want := range map[string]string{
		"HyperEVM testnet (chain 998) prototype; not investable":                     "NOT INVESTABLE",
		"curve from the Hyperliquid portfolio: hlp.xyz vault history is out of date": "HISTORY OLD",
		"hlp.xyz history out of date: last updated 201 days ago":                     "HISTORY 201D OLD",
	} {
		if got := tagFor(w); got != want {
			t.Errorf("tagFor(%q) = %q, want %q", w, got, want)
		}
	}
}

// Agent prose: at most 72 columns, no heading sizes, no code background.
func TestMarkdownRush(t *testing.T) {
	th, _ := theme.Get(theme.NameRush)
	lines := Markdown("# Title\n\nRun `/vault hlp` to see it. "+strings.Repeat("word ", 40), Opts{Width: 140, Theme: th, Profile: colorprofile.TrueColor})
	for _, l := range lines {
		if w := len([]rune(stripANSI(l))); w > mdWidth {
			t.Errorf("line %d wide: %q", w, stripANSI(l))
		}
		if strings.Contains(l, "\x1b[48") || strings.Contains(l, ";48;") {
			t.Errorf("background in prose: %q", l)
		}
	}
	if strings.Contains(stripANSI(strings.Join(lines, "\n")), "#") {
		t.Errorf("heading marks kept:\n%s", strings.Join(lines, "\n"))
	}
}

// TestInlineCardFitsContent: an inline card is as wide as its content
// (DESIGN.md 6.1), not the 100-column cap, and its focal leads as compact
// big digits under the title instead of a value at the far right.
func TestInlineCardFitsContent(t *testing.T) {
	r := fixtureResults(t)["vaults"]
	out := ansi.Strip(Render(r, Opts{Width: 104, Compact: true}))
	lines := strings.Split(out, "\n")
	widest := 0
	for _, l := range lines {
		widest = max(widest, len([]rune(strings.TrimRight(l, " "))))
	}
	if widest >= 100 {
		t.Fatalf("card is %d wide, not content sized:\n%s", widest, out)
	}
	if h := strings.TrimSpace(lines[0]); h != "VAULTS  5" {
		t.Fatalf("header %q: the focal belongs under the title:\n%s", h, out)
	}
	if !strings.ContainsAny(lines[1], "█▀▄") || !strings.ContainsAny(lines[3], "█▀▄") {
		t.Fatalf("no big focal under the title:\n%s", out)
	}
}
