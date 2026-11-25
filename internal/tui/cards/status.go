package cards

import (
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// statusCard is the card of status/doctor. The one thing it says is
// whether everything is healthy: that is the focal line. Below it, one
// row per check with its latency; details stay quiet unless the check
// failed, because the failing line is the only one worth reading.
func (p *painter) statusCard(r *core.Result) card {
	d, ok := as[core.StatusData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if len(strings.Fields(c.title)) > 3 || c.title == "" {
		c.title = "Status"
	}
	failed := 0
	for _, ch := range d.Checks {
		if !ch.OK {
			failed++
		}
	}
	if failed == 0 {
		c.focal, c.focalStyle = "OK", p.st.text
	} else {
		c.focal, c.focalStyle = strconv.Itoa(failed)+" FAILED", p.st.down
	}
	// CHECK  ■  12MS: a pass is an ink ■, a failure a red ■ with its detail;
	// passing details stay in the full card.
	cells := make([][]cell, 0, len(d.Checks))
	for _, ch := range d.Checks {
		state, detail := p.cst(glyph.On, p.st.text), p.cm("")
		if !p.o.Compact {
			detail = p.cm(ch.Detail)
		}
		if !ch.OK {
			state, detail = p.cst(glyph.On, p.st.down), p.c(ch.Detail)
		}
		lat := p.cm("")
		if ch.LatencyMs > 0 {
			lat = p.cm(strconv.FormatInt(ch.LatencyMs, 10) + "MS")
		}
		cells = append(cells, []cell{p.c(ch.Name), state, lat, detail})
	}
	cols := []column{
		{title: "check", minW: 6, keep: 10}, {title: "", keep: 9},
		{title: "", right: true, keep: 5}, {title: "", flex: true, minW: 8, keep: 7},
	}
	c.body = p.table(cols, cells, p.w, nil)
	return c
}
