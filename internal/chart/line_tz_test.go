package chart

import (
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// A line chart's time axis must label local boundaries in a zone with a
// UTC offset (PST): truncating in UTC left the axis empty.
func TestLineTimeAxisLocalZone(t *testing.T) {
	loc := time.FixedZone("PST", -8*3600)
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC).UnixMilli()
	var s domain.Series
	for i := 0; i < 72; i++ {
		s = append(s, domain.Point{T: t0 + int64(i)*3_600_000, V: float64(i%5) / 100})
	}
	g := LineChart(LineOpts{Width: 100, Height: 10, Series: []Named{{Name: "APR", Series: s}}, PercentAxis: true, Location: loc}, theme.Default(true))
	rows := strings.Split(g.Plain(), "\n")
	if strings.TrimSpace(rows[len(rows)-1]) == "" {
		t.Fatalf("empty time axis:\n%s", g.Plain())
	}
	if !strings.Contains(g.Plain(), "%") {
		t.Fatalf("percent axis missing:\n%s", g.Plain())
	}
}
