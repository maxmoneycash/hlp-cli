package chart

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

var monthAbbr = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// Heatmap renders the monthly-returns calendar (years x 12 months + YTD),
// cells tinted by sign and magnitude.
//
// The grid is 1 header row plus one row per year (oldest first). Each cell
// prints the return in percent with a background wash whose strength
// follows |return| relative to the largest month; at 16 colors, where washes
// are dropped, the number itself takes the sign color. Months with no data
// show a dim dot. The YTD column compounds the year's months.
func Heatmap(m []domain.MonthlyReturn, width int, th theme.Theme) *Grid {
	width = max(width, 1)
	byYear := map[int]map[int]float64{}
	maxAbs := 0.0
	for _, r := range m {
		if r.Month < 1 || r.Month > 12 || math.IsNaN(r.Ret) {
			continue
		}
		if byYear[r.Year] == nil {
			byYear[r.Year] = map[int]float64{}
		}
		byYear[r.Year][r.Month] = r.Ret
		maxAbs = math.Max(maxAbs, math.Abs(r.Ret))
	}
	years := make([]int, 0, len(byYear))
	for y := range byYear {
		years = append(years, y)
	}
	sort.Ints(years)
	g := NewGrid(width, 1+len(years))
	if len(years) == 0 {
		g.Text(0, 0, "no monthly returns", th.Dim, LLabel)
		return g
	}

	const yearW = 5 // "2025 "
	cellW := (width - yearW - 1) / 13
	if cellW < 2 {
		cellW = 2
	}
	digits := func(v float64) string {
		p := v * 100
		switch {
		case cellW >= 6:
			s := strconv.FormatFloat(p, 'f', 1, 64)
			if p > 0 && s != "0.0" {
				s = "+" + s
			}
			if s == "-0.0" {
				s = "0.0"
			}
			return s
		case cellW >= 4:
			s := strconv.FormatFloat(math.Round(p), 'f', 0, 64)
			if s == "-0" {
				s = "0"
			}
			if p >= 0.5 {
				s = "+" + s
			}
			return s
		}
		return ""
	}
	rjust := func(s string, n int) string {
		if len(s) >= n {
			return s
		}
		return strings.Repeat(" ", n-len(s)) + s
	}
	colX := func(mo int) int { // mo 0..11 months, 12 = YTD (one extra column of air)
		x := yearW + mo*cellW
		if mo == 12 {
			x++
		}
		return x
	}
	for mo := 0; mo < 13; mo++ {
		lbl := "YTD"
		if mo < 12 {
			lbl = monthAbbr[mo]
		}
		field := cellW - 1
		if field >= 4 {
			lbl = rjust(lbl, field-1) + " " // same right edge as the values
		} else {
			lbl = rjust(lbl[:min(len(lbl), field)], field)
		}
		g.Text(colX(mo), 0, lbl, th.Dim, LLabel)
	}
	cell := func(x, y int, v float64, bold bool) {
		// A value that prints as zero is neutral: identical labels never
		// get opposite colors.
		if math.Abs(v) < 0.0005 || (digits(v) != "" && strings.Trim(digits(v), "+-0.") == "") {
			field := cellW - 1
			s := rjust(digits(0), max(field, 1))
			if field >= 4 {
				s = rjust(digits(0), field-1) + " "
			}
			if field < 3 {
				s = rjust("0", max(field, 1))
			}
			for i, ch := range s {
				if c := g.Set(x+i, y, ch, th.Dim, LLabel); c != nil {
					c.Bold = bold
				}
			}
			return
		}
		dir := th.Up
		if v < 0 {
			dir = th.Down
		}
		strength := 0.12
		if maxAbs > 0 {
			strength += 0.48 * math.Sqrt(math.Abs(v)/maxAbs)
		}
		wash := bgTint(th, dir, strength)
		// Text: the theme's text color, except at 16 colors (no wash) where
		// the sign color carries the meaning.
		fg := theme.Color{True: th.Text.True, C256: th.Text.C256, C16: dir.C16}
		// The wash covers cellW-1 columns; the last column is a gutter so
		// neighboring months read as separate tiles.
		field := cellW - 1
		if field < 3 {
			// Too narrow for numbers: a solid block whose tint is the value.
			solid := tint(th, dir, strength+0.2)
			for i := 0; i < field; i++ {
				g.Set(x+i, y, '█', solid, LLabel)
			}
			return
		}
		s := rjust(digits(v), field)
		if field >= 4 {
			s = rjust(digits(v), field-1) + " "
		}
		for i, ch := range s {
			if c := g.Set(x+i, y, ch, fg, LLabel); c != nil {
				c.BG, c.Bold = wash, bold
			}
		}
	}
	for row, yr := range years {
		y := row + 1
		g.Text(0, y, strconv.Itoa(yr), th.AxisText, LLabel)
		ytd, any := 1.0, false
		for mo := 1; mo <= 12; mo++ {
			v, ok := byYear[yr][mo]
			x := colX(mo - 1)
			if !ok {
				// right-aligned with the numbers' last digit
				dot := x + cellW - 2
				if cellW-1 >= 4 {
					dot--
				}
				g.Set(max(dot, x), y, '·', th.Dim, LLabel)
				continue
			}
			cell(x, y, v, false)
			ytd *= 1 + v
			any = true
		}
		if any {
			cell(colX(12), y, ytd-1, true)
		}
	}
	return g
}
