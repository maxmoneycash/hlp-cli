package cards

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/tui/glyph"
)

// indexCard renders hlp.xyz vault indexes: the list (name, slug, kind), or
// one index with its description and the latest allocation's weights.
// core.IndexData is unwrapped; the bare list and index shapes are still
// read for saved transcripts.
func (p *painter) indexCard(r *core.Result) card {
	if d, ok := as[core.IndexData](r.Data); ok {
		inner := *r
		if d.Index != nil {
			inner.Data = *d.Index
		} else {
			inner.Data = d.Indexes
		}
		return p.indexCard(&inner)
	}
	if list, ok := as[[]hlpxyz.Index](r.Data); ok {
		c := card{title: "Indexes", focal: strconv.Itoa(len(list)), focalStyle: p.st.bold}
		slugW, nameW := 0, 0
		for _, ix := range list {
			slugW = max(slugW, ansi.StringWidth(ix.Def.Slug))
			nameW = max(nameW, ansi.StringWidth(indexName(ix)))
		}
		for _, ix := range list {
			name := indexName(ix)
			pad := strings.Repeat(" ", max(0, slugW-ansi.StringWidth(ix.Def.Slug)))
			line := p.st.muted.Render(ix.Def.Slug) + pad + "  " + p.st.text.Render(name)
			npad := strings.Repeat(" ", max(0, nameW-ansi.StringWidth(name)))
			switch {
			case isTestIndex(ix):
				line += npad + "  " + p.st.warn.Render(glyph.Note+" TEST")
			case ix.Def.Official:
				line += npad + "  " + p.lbl("official")
			}
			c.body = append(c.body, ansi.Truncate(line, p.w, "…"))
		}
		return c
	}
	if v, ok := as[core.IndexView](r.Data); ok && len(v.Members) > 0 {
		return p.indexMembersCard(r, v)
	}
	ix, ok := as[hlpxyz.Index](r.Data)
	if !ok {
		if v, isView := as[core.IndexView](r.Data); isView {
			ix, ok = v.Index, true
		}
	}
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: indexName(ix), name: true, sub: ix.Def.Slug}
	if d := strings.TrimSpace(ix.Def.Description); d != "" && !p.o.Compact {
		c.notes = append(c.notes, p.st.muted.Render(d))
	}
	if len(ix.Allocs) == 0 {
		return c
	}
	last := ix.Allocs[len(ix.Allocs)-1]
	type w struct {
		addr string
		v    float64
	}
	var ws []w
	for a, v := range last.Weights {
		ws = append(ws, w{a, v})
	}
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].v != ws[j].v {
			return ws[i].v > ws[j].v
		}
		return ws[i].addr < ws[j].addr
	})
	c.focal, c.focalStyle = strconv.Itoa(len(ws)), p.st.bold
	c.body = append(c.body, p.section("allocation"))
	limit := p.rowLimit(12)
	for i, x := range ws {
		if i == limit {
			c.body = p.more(c.body, len(ws), limit)
			break
		}
		c.body = append(c.body, ansi.Truncate(p.st.text.Render(fmt.Sprintf("%6.1f%%", x.v*100))+"  "+p.st.muted.Render(x.addr), p.w, "…"))
	}
	return c
}

func indexName(ix hlpxyz.Index) string {
	if ix.Def.Name != "" {
		return ix.Def.Name
	}
	return ix.Def.Slug
}

// isTestIndex flags hlp.xyz test indexes (a backend data issue: they
// should not be listed; labelled here rather than presented as official).
func isTestIndex(ix hlpxyz.Index) bool {
	return strings.Contains(strings.ToLower(ix.Def.Name), "test") || strings.Contains(strings.ToLower(ix.Def.Slug), "test")
}

// clipLines keeps the first n lines, ending the last kept one with "…"
// when lines were dropped (the full text is in --json / ctrl+o).
func (p *painter) clipLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n]...)
	out[n-1] = ansi.Truncate(out[n-1], max(p.w-1, 1), "") + "…"
	return out
}

// indexMembersCard: one index, its latest allocation by vault name with
// TVL and 30-day return. The focal number is the number of vaults.
func (p *painter) indexMembersCard(r *core.Result, v core.IndexView) card {
	title := v.Def.Name
	if title == "" {
		title = r.Title
	}
	c := card{title: title, name: true, sub: v.Def.Slug, focal: strconv.Itoa(len(v.Members)), focalStyle: p.st.bold}
	if d := strings.TrimSpace(v.Def.Description); d != "" && !p.o.Compact {
		c.notes = append(c.notes, p.st.muted.Render(d))
	}
	c.body = append(c.body, p.section("allocation"))
	limit := p.rowLimit(12)
	rows := make([][]cell, 0, len(v.Members))
	for i, m := range v.Members {
		if i == limit {
			break
		}
		name := m.Name
		if name == "" {
			name = ShortAddr(m.Address)
		}
		ret := p.cm(dash)
		if m.Return30d != nil {
			ret = p.cs(SignedPct(*m.Return30d, 1), *m.Return30d)
		}
		tvl := p.cm(dash)
		if m.TVL > 0 {
			tvl = p.c(USD(m.TVL))
		}
		rows = append(rows, []cell{p.c(Pct(m.Weight, 1)), p.c(name), tvl, ret})
	}
	cols := []column{
		{title: "weight", right: true, keep: 100}, {title: "vault", flex: true, minW: 8, keep: 95},
		{title: "tvl", right: true, keep: 60}, {title: "30d", right: true, keep: 80},
	}
	c.body = append(c.body, p.table(cols, rows, p.w, nil)...)
	if len(v.Members) > limit {
		c.body = p.more(c.body, len(v.Members), limit)
	}
	return c
}
