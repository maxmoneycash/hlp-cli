package cards

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/registry"
)

// discoverCard renders `hlp vaults discover` (*registry.Discovery).
//
// Restraint (Stageshow craft): the count of curated entries is the one
// focal number; the theses (theme and style tags) are the story and get
// text color, every other group stays muted; entry names appear only when
// the filter has narrowed the set enough to read them.
func (p *painter) discoverCard(r *core.Result) card {
	d, ok := as[registry.Discovery](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: "Discover", focal: strconv.Itoa(d.Total), focalStyle: p.st.bold}
	if d.Total == 0 {
		c.focal, c.focalStyle = "NO MATCH", p.st.muted
	}
	var pairs [][2]string
	for _, k := range d.Kinds {
		pairs = append(pairs, [2]string{"", p.st.text.Render(fmt.Sprint(k.Count)) + " " + p.lbl(kindLabel(k.Kind, k.Count))})
	}
	if len(pairs) > 0 {
		c.body = append(c.body, p.meta(pairs...))
	}

	for _, g := range d.Groups {
		if p.o.Compact && !g.Thesis {
			continue
		}
		st := p.st.muted
		if g.Thesis {
			st = p.st.text
		}
		c.body = append(c.body, "", p.section(g.Title))
		c.body = append(c.body, p.chips(g.Tags, st)...)
	}

	if d.Total > 0 && d.Total <= 12 {
		c.body = append(c.body, "", p.section("entries"))
		nameW := 0
		for _, e := range d.Entries {
			nameW = max(nameW, ansi.StringWidth(e.Name))
		}
		nameW = min(nameW, max(12, p.w/2))
		kindW := 0
		for _, e := range d.Entries {
			kindW = max(kindW, len(e.Kind))
		}
		for _, e := range d.Entries {
			name := ansi.Truncate(e.Name, nameW, "…")
			pad := strings.Repeat(" ", max(0, nameW-ansi.StringWidth(name)))
			kind := string(e.Kind) + strings.Repeat(" ", kindW-len(e.Kind))
			line := p.st.text.Render(name) + pad + "  " + p.st.muted.Render(kind) + "  " + p.st.faint.Render(e.ID)
			c.body = append(c.body, ansi.Truncate(line, p.w, "…"))
		}
	}
	if h := d.Hidden; h.Testnet+h.Children+h.Archived > 0 && !p.o.Compact {
		var pairs [][2]string
		if h.Testnet > 0 {
			pairs = append(pairs, [2]string{"testnet", p.st.muted.Render(strconv.Itoa(h.Testnet))})
		}
		if h.Children > 0 {
			pairs = append(pairs, [2]string{"HLP child", p.st.muted.Render(strconv.Itoa(h.Children))})
		}
		if h.Archived > 0 {
			pairs = append(pairs, [2]string{"archived", p.st.muted.Render(strconv.Itoa(h.Archived))})
		}
		c.notes = append(c.notes, p.lbl("hidden")+"   "+p.meta(pairs...))
	}
	return c
}

// kindLabel is a kind's short label, singular for one.
func kindLabel(k registry.Kind, n int) string {
	switch k {
	case registry.KindHyperCore:
		return plural(n, "vault", "vaults")
	case registry.KindHyperEVM:
		return "evm"
	case registry.KindIndex:
		return plural(n, "index", "indexes")
	case registry.KindStrategy:
		return plural(n, "strat", "strats")
	}
	return string(k)
}

// cmd spells a command for this surface: "/fund" in the shell, "hlp fund"
// elsewhere.
func (p *painter) cmd(slash string) string {
	if p.o.Compact {
		return "/" + slash
	}
	return "hlp " + slash
}

// discoverHint names the command that shows live numbers for each kind in
// the result, with the concrete tag when one was given.
func (p *painter) discoverHint(d registry.Discovery) string {
	tag := "<tag>"
	if len(d.Tags) == 1 {
		tag = d.Tags[0]
	}
	vaults := "/vaults --tag " + tag
	if !p.o.Compact {
		vaults = "hlp vaults --tag " + tag
	}
	var hints []string
	have := map[registry.Kind]bool{}
	for _, k := range d.Kinds {
		have[k.Kind] = true
	}
	if have[registry.KindHyperCore] || len(d.Kinds) == 0 {
		hints = append(hints, vaults+" for live numbers")
	}
	if have[registry.KindStrategy] {
		hints = append(hints, p.cmd("strat")+" <id> for strategies")
	}
	if have[registry.KindIndex] {
		hints = append(hints, p.cmd("index")+" <slug> for indexes")
	}
	if have[registry.KindHyperEVM] {
		hints = append(hints, p.cmd("fund")+" for strategy vaults")
	}
	return strings.Join(hints, " · ")
}

// chips lays tags out as "name count" pairs, wrapped to the card width.
func (p *painter) chips(tags []registry.TagCount, st lipgloss.Style) []string {
	var lines []string
	cur, curW := "", 0
	for _, t := range tags {
		w := ansi.StringWidth(t.Tag) + 1 + len(fmt.Sprint(t.Count))
		chip := st.Render(t.Tag) + " " + p.st.faint.Render(fmt.Sprint(t.Count))
		if curW > 0 && curW+3+w > p.w {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur += "   "
			curW += 3
		}
		cur += chip
		curW += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
