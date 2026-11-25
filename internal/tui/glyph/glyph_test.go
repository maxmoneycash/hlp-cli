package glyph

import (
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestBigGolden(t *testing.T) {
	cases := map[string][3]string{
		"64953": {
			"█▀▀ █ █  █▀█ █▀▀ ▀▀█",
			"█▀█ ▀▀█  ▀▀█ ▀▀█  ▀█",
			"▀▀▀   ▀  ▀▀▀ ▀▀▀ ▀▀▀",
		},
		"−0.9": {
			"    █▀█   █▀█",
			"▀▀▀ █ █   ▀▀█",
			"    ▀▀▀ ▀ ▀▀▀",
		},
		"+367.9": {
			" ▄  ▀▀█ █▀▀ ▀▀█   █▀█",
			"▀█▀  ▀█ █▀█   █   ▀▀█",
			"    ▀▀▀ ▀▀▀   ▀ ▀ ▀▀▀",
		},
	}
	for in, want := range cases {
		got, ok := Big(in)
		if !ok {
			t.Fatalf("Big(%q) not ok", in)
		}
		if got != want {
			t.Errorf("Big(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	wm := [3]string{
		"█ █ █   █▀█",
		"█▀█ █   █▀▀",
		"▀ ▀ ▀▀▀ ▀  ",
	}
	if got := Wordmark(); got != wm {
		t.Errorf("Wordmark()\n got  %q\n want %q", got, wm)
	}
}

func TestBigGrouping(t *testing.T) {
	// "1234567" groups as 1 234 567: two extra blank cells in total.
	plain := 7*3 + 6
	if got := BigWidth("1234567"); got != plain+2 {
		t.Fatalf("BigWidth(1234567) = %d, want %d", got, plain+2)
	}
	// Decimals never group.
	if got, want := BigWidth("1.2345"), 3+1+1+1+4*3+3; got != want {
		t.Fatalf("BigWidth(1.2345) = %d, want %d", got, want)
	}
}

func TestBigWidthMatchesRows(t *testing.T) {
	ins := []string{"0", "64953", "64953.5", "−0.9", "+367.9", "1234567.89", "+0.04", " 12", "999999999"}
	for _, in := range ins {
		rows, ok := Big(in)
		if !ok {
			t.Fatalf("Big(%q) not ok", in)
		}
		w := BigWidth(in)
		for i, r := range rows {
			if lipgloss.Width(r) != w {
				t.Errorf("Big(%q) row %d width %d, BigWidth %d", in, i, lipgloss.Width(r), w)
			}
		}
	}
	for _, r := range Wordmark() {
		if lipgloss.Width(r) != lipgloss.Width(Wordmark()[0]) {
			t.Error("wordmark rows differ in width")
		}
	}
}

func TestBigRejects(t *testing.T) {
	for _, in := range []string{"", "-0.9", "64,953", "$151.6M", "12%", "abc"} {
		if _, ok := Big(in); ok {
			t.Errorf("Big(%q) ok, want false", in)
		}
		if BigWidth(in) != 0 {
			t.Errorf("BigWidth(%q) != 0", in)
		}
	}
}

func TestReticleWidth(t *testing.T) {
	st := func(s string) string { return "\x1b[38;5;202m" + s + "\x1b[m" }
	for w := 4; w <= 200; w++ {
		for _, top := range []bool{true, false} {
			for _, s := range []func(string) string{nil, st} {
				got := Reticle(w, top, s)
				if lipgloss.Width(got) != w {
					t.Fatalf("Reticle(%d,%v) width %d", w, top, lipgloss.Width(got))
				}
				p := ansi.Strip(got)
				l, r := "└─", "─┘"
				if top {
					l, r = "┌─", "─┐"
				}
				if !strings.HasPrefix(p, l) || !strings.HasSuffix(p, r) {
					t.Fatalf("Reticle(%d,%v) = %q", w, top, p)
				}
				if strings.Count(p, "─") != 2 {
					t.Fatalf("Reticle(%d) draws a run: %q", w, p)
				}
			}
		}
	}
	if Reticle(3, true, nil) != "   " || Reticle(0, true, nil) != "" {
		t.Fatal("narrow reticle not blank")
	}
}

func TestFlapOneChangedCell(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 19, 0, 0, 0, time.UTC)
	f := NewFlap()
	f.Set("64,951", t0)
	if f.Busy(t0) {
		t.Fatal("first value should land without a flip")
	}
	if got := cellsText(f.Cells(t0)); got != "64,951" {
		t.Fatalf("first cells %q", got)
	}
	t1 := t0.Add(3 * time.Second)
	f.Set("64,953", t1)

	want := []struct {
		at    time.Duration
		phase uint8
		r     rune
	}{{0, Top, '▀'}, {33 * time.Millisecond, Bottom, '▄'}, {66 * time.Millisecond, Settled, '3'}}
	for _, w := range want {
		now := t1.Add(w.at)
		cs := f.Cells(now)
		changed := 0
		for i, c := range cs {
			if c.Phase != Settled || c.R != []rune("64,951")[i] {
				changed++
			}
		}
		if w.phase != Settled && changed != 1 {
			t.Fatalf("at %v: %d changed cells, want 1", w.at, changed)
		}
		if cs[5].Phase != w.phase || cs[5].R != w.r {
			t.Fatalf("at %v: cell 5 = %+v, want phase %d %q", w.at, cs[5], w.phase, w.r)
		}
		for i := 0; i < 5; i++ {
			if cs[i].Phase != Settled {
				t.Fatalf("at %v: unchanged cell %d flipped", w.at, i)
			}
		}
	}
	end := t1.Add(66 * time.Millisecond)
	if f.Busy(end) {
		t.Fatal("still busy after the flip")
	}
	if f.Next(end) != 0 {
		t.Fatal("Next should be 0 when idle")
	}
	if !f.Busy(t1.Add(10*time.Millisecond)) || f.Next(t1.Add(10*time.Millisecond)) != 23*time.Millisecond {
		t.Fatalf("mid-flip Next = %v", f.Next(t1.Add(10*time.Millisecond)))
	}
}

func TestFlapThrottle(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 19, 0, 0, 0, time.UTC)
	f := NewFlap()
	f.Set("100", t0)
	t1 := t0.Add(time.Second)
	f.Set("101", t1) // starts a flip; the window runs to t1+2.6s
	f.Set("102", t1.Add(500*time.Millisecond))
	f.Set("103", t1.Add(time.Second)) // latest wins
	// During the window the waiting value never shows.
	for d := 100 * time.Millisecond; d < FlapThrottle; d += 100 * time.Millisecond {
		if got := cellsText(f.Cells(t1.Add(d))); got != "101" {
			t.Fatalf("at +%v shows %q, want 101", d, got)
		}
		if !f.Busy(t1.Add(d)) {
			t.Fatalf("at +%v not busy with a value waiting", d)
		}
	}
	if got := f.Next(t1.Add(2 * time.Second)); got != 600*time.Millisecond {
		t.Fatalf("Next while waiting = %v, want 600ms", got)
	}
	open := t1.Add(FlapThrottle)
	cs := f.Cells(open)
	if cs[2].Phase != Top {
		t.Fatalf("window end: cell 2 = %+v, want Top", cs[2])
	}
	if got := cellsText(f.Cells(open.Add(66 * time.Millisecond))); got != "103" {
		t.Fatalf("after flip shows %q, want 103", got)
	}
	// Looking late gives the same result as looking on time.
	g := NewFlap()
	g.Set("100", t0)
	g.Set("101", t1)
	g.Set("103", t1.Add(time.Second))
	if got := cellsText(g.Cells(t1.Add(10 * time.Second))); got != "103" || g.Busy(t1.Add(10*time.Second)) {
		t.Fatalf("late look %q", got)
	}
}

func TestFlapStaggerAndTail(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 19, 0, 0, 0, time.UTC)
	f := NewFlap()
	f.Set("9.99", t0)
	t1 := t0.Add(5 * time.Second)
	f.Set("10.01", t1) // length change: the whole string from cell 0 flips
	cs := f.Cells(t1)
	if cs[0].Phase != Top || cs[1].Phase != Settled {
		t.Fatalf("t1: %+v", cs)
	}
	// Cell k starts k ticks late; before its turn it shows the old character
	// (or blank past the old length).
	cs = f.Cells(t1.Add(FlapTick))
	if cs[0].Phase != Bottom || cs[1].Phase != Top || cs[2].R != '9' || cs[4].R != ' ' {
		t.Fatalf("t1+1 tick: %+v", cs)
	}
	settle := t1.Add(6 * FlapTick)
	if f.Busy(settle) || cellsText(f.Cells(settle)) != "10.01" {
		t.Fatalf("not settled: %q", cellsText(f.Cells(settle)))
	}
	if f.Busy(settle.Add(-time.Nanosecond)) == false {
		t.Fatal("should be busy just before the last cell settles")
	}
}

func TestFlapSameValueNoFlip(t *testing.T) {
	t0 := time.Now()
	f := NewFlap()
	f.Set("1.0", t0)
	f.Set("1.0", t0.Add(5*time.Second))
	if f.Busy(t0.Add(5 * time.Second)) {
		t.Fatal("same text flipped")
	}
}

func cellsText(cs []Cell) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteRune(c.R)
	}
	return b.String()
}

func TestCursorBlink(t *testing.T) {
	k := time.Date(2026, 11, 15, 19, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Duration
		on   bool
		next time.Duration
	}{
		{0, true, 605 * time.Millisecond},
		{604 * time.Millisecond, true, time.Millisecond},
		{605 * time.Millisecond, false, 495 * time.Millisecond},
		{1099 * time.Millisecond, false, time.Millisecond},
		{1100 * time.Millisecond, true, 605 * time.Millisecond},
		{2200*time.Millisecond + 700*time.Millisecond, false, 400 * time.Millisecond},
	}
	for _, c := range cases {
		if got := CursorOn(k, k.Add(c.at)); got != c.on {
			t.Errorf("CursorOn(+%v) = %v", c.at, got)
		}
		if got := BlinkNext(k, k.Add(c.at)); got != c.next {
			t.Errorf("BlinkNext(+%v) = %v, want %v", c.at, got, c.next)
		}
	}
	if !CursorOn(k, k.Add(-time.Second)) {
		t.Error("cursor off before the key")
	}
}

func TestReduced(t *testing.T) {
	t.Setenv("HLP_REDUCED_MOTION", "")
	t.Setenv("TERM", "xterm-256color")
	// NO_MOTION may be set in the developer's shell; unset it for the test.
	t.Setenv("NO_MOTION", "")
	unsetenv(t, "NO_MOTION")
	if Reduced("on") || Reduced("") {
		t.Fatal("reduced with nothing set")
	}
	if !Reduced("off") || !Reduced("OFF") {
		t.Fatal("cfg off not reduced")
	}
	t.Setenv("HLP_REDUCED_MOTION", "1")
	if !Reduced("on") {
		t.Fatal("HLP_REDUCED_MOTION=1 not reduced")
	}
	t.Setenv("HLP_REDUCED_MOTION", "0")
	if Reduced("on") {
		t.Fatal("HLP_REDUCED_MOTION=0 reduced")
	}
	t.Setenv("NO_MOTION", "1")
	if !Reduced("on") {
		t.Fatal("NO_MOTION not reduced")
	}
	unsetenv(t, "NO_MOTION")
	t.Setenv("TERM", "dumb")
	if !Reduced("on") {
		t.Fatal("TERM=dumb not reduced")
	}
}

func TestFloorBasics(t *testing.T) {
	dot := func(s string) string { return "<" + s + ">" }
	if got := Floor([]string{"abc"}, 20, 0, nil); got[0] != "abc" {
		t.Fatal("nil dot changed lines")
	}
	got := Floor([]string{"", "", "abc", "abcdefg   "}, 12, 0, dot)
	plain := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "<", ""), ">", "") }
	if p := plain(got[0]); p != "  ·   ·   ·" {
		t.Fatalf("row 0 %q", p)
	}
	if got[1] != "" {
		t.Fatalf("odd row drew dots: %q", got[1])
	}
	if p := plain(got[2]); p != "abc   ·   ·" {
		t.Fatalf("row 2 %q", p)
	}
	if got[3] != "abcdefg   " {
		t.Fatalf("odd row 3 changed %q", got[3])
	}
	// y0 shifts the parity.
	if got := Floor([]string{"", ""}, 8, 1, dot); got[0] != "" || got[1] == "" {
		t.Fatalf("y0 parity %q", got)
	}
}

// TestFloorNeverOverwrites is the property test: on random styled lines the
// floor never changes a cell that had content (a non-space rune, or a space
// with styling), and only adds `·` cells.
func TestFloorNeverOverwrites(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	styles := []string{"\x1b[1m", "\x1b[38;5;202m", "\x1b[48;2;26;26;29m", "\x1b[2m", "\x1b[38;2;244;243;239m"}
	words := []string{"BTC", "64,953", "−0.9%", "▀█▀", "❯", "漢字", " ", "  ", "·"}
	dot := func(s string) string { return "\x1b[38;5;236m" + s + "\x1b[m" }
	for n := 0; n < 3000; n++ {
		var b strings.Builder
		for k := rng.Intn(6); k > 0; k-- {
			if rng.Intn(2) == 0 {
				b.WriteString(styles[rng.Intn(len(styles))])
			}
			b.WriteString(words[rng.Intn(len(words))])
			if rng.Intn(3) == 0 {
				b.WriteString("\x1b[m")
			}
			b.WriteString(strings.Repeat(" ", rng.Intn(5)))
		}
		line := b.String()
		width := 4 + rng.Intn(60)
		out := Floor([]string{line}, width, 0, dot)[0]
		before := cellMap(line)
		after := cellMap(out)
		for col, c := range before {
			if c.blank {
				continue
			}
			a, ok := after[col]
			if !ok || a.r != c.r || a.styled != c.styled {
				t.Fatalf("case %d: cell %d changed: %q -> %q\n in  %q\n out %q", n, col, c.r, a.r, line, out)
			}
		}
		for col, a := range after {
			b, had := before[col]
			if (!had || b.blank) && a.r != " " && a.r != "·" {
				t.Fatalf("case %d: added %q at %d", n, a.r, col)
			}
			if a.r == "·" && (!had || b.blank) && (col-FloorCol0)%FloorStepX != 0 {
				t.Fatalf("case %d: dot off grid at %d", n, col)
			}
		}
	}
}

type cellInfo struct {
	r      string
	styled bool // any non-reset SGR active
	blank  bool
}

// cellMap walks s with a tiny SGR tracker and returns the content of every
// cell column.
func cellMap(s string) map[int]cellInfo {
	m := map[int]cellInfo{}
	col := 0
	styled := false
	p := ansi.NewParser()
	state := byte(0)
	for len(s) > 0 {
		seq, w, n, newState := ansi.DecodeSequence(s, state, p)
		state = newState
		if strings.HasPrefix(seq, "\x1b[") {
			styled = !(seq == "\x1b[m" || seq == "\x1b[0m")
		} else if w > 0 {
			blank := seq == " " && !styled
			m[col] = cellInfo{r: seq, styled: styled, blank: blank}
			for k := 1; k < w; k++ {
				m[col+k] = cellInfo{r: "", styled: styled}
			}
			col += w
		}
		s = s[n:]
	}
	return m
}

func unsetenv(t *testing.T, k string) {
	t.Helper()
	if err := os.Unsetenv(k); err != nil {
		t.Fatal(err)
	}
}
