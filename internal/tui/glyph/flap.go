package glyph

import "time"

// Split-flap timing (DESIGN §7). Each changed cell shows the top half `▀` for
// one tick, the bottom half `▄` for one tick, then the new character; changed
// cells start one tick apart, left to right. A symbol flips at most once per
// FlapThrottle; newer values wait and the latest one lands on the next flip.
const (
	FlapTick     = 33 * time.Millisecond
	FlapThrottle = 2600 * time.Millisecond
)

// Cell phases.
const (
	Settled uint8 = 0 // R is the character
	Top     uint8 = 1 // R is '▀'
	Bottom  uint8 = 2 // R is '▄'
)

// Cell is one character cell of a flap. R is the rune to draw: the character
// when Phase is Settled, else the flap half ('▀' or '▄'), so a caller can
// print R and style by Phase (Muted on Surface while flipping).
type Cell struct {
	R     rune
	Phase uint8
}

// Flap is one ticker item's flipping text. It holds no timer and reads no
// clock: Set, Cells, Busy and Next all take now, and the caller schedules a
// redraw at Next(now) only while Busy(now). The zero value is not ready; use
// NewFlap.
type Flap struct {
	cur []rune // the text shown when settled (target of the current flip)

	// The current flip: from -> cur, started at start; order[i] is the stagger
	// slot of cell i, or -1 when cell i does not flip.
	from   []rune
	order  []int
	slots  int
	start  time.Time
	flips  bool // a flip has ever started (the throttle window is live)
	primed bool // the first value has been set

	pending    []rune
	hasPending bool
}

// NewFlap returns an empty flap.
func NewFlap() *Flap { return &Flap{} }

// Text is the latest text the flap shows or is flipping to (not a pending
// value still waiting for the throttle window).
func (f *Flap) Text() string { return string(f.cur) }

// Set asks the flap to show text. The first value appears at once with no
// flip (nothing changed yet). After that a changed value starts a flip at now,
// unless the last flip started less than FlapThrottle ago: then it waits for
// the window to end, and only the latest value waiting is kept.
func (f *Flap) Set(text string, now time.Time) {
	f.advance(now)
	rs := []rune(text)
	if !f.primed {
		f.cur, f.primed = rs, true
		return
	}
	if f.flips && now.Before(f.start.Add(FlapThrottle)) {
		if runesEqual(rs, f.cur) {
			f.pending, f.hasPending = nil, false
		} else {
			f.pending, f.hasPending = rs, true
		}
		return
	}
	f.begin(rs, now)
}

// begin starts a flip to rs at t. Only changed characters flip; when the
// length changes, every cell from the first difference on flips.
func (f *Flap) begin(rs []rune, t time.Time) {
	f.pending, f.hasPending = nil, false
	if runesEqual(rs, f.cur) {
		return
	}
	from := f.cur
	order := make([]int, len(rs))
	first := -1
	for i := range rs {
		if i >= len(from) || from[i] != rs[i] {
			first = i
			break
		}
	}
	tail := len(rs) != len(from)
	slot := 0
	for i := range rs {
		changed := i >= len(from) || from[i] != rs[i] || (tail && first >= 0 && i >= first)
		if changed {
			order[i] = slot
			slot++
		} else {
			order[i] = -1
		}
	}
	if slot == 0 {
		// Only a shorter string with the same prefix: nothing to flip, but it
		// still counts as a change shown now.
		f.cur = rs
		return
	}
	f.from, f.order, f.slots, f.start, f.flips = from, order, slot, t, true
	f.cur = rs
}

// advance starts a waiting value once its throttle window has ended. The flip
// is dated at the window's end, not at now, so the result does not depend on
// how often the caller looks.
func (f *Flap) advance(now time.Time) {
	if !f.hasPending || !f.flips {
		return
	}
	at := f.start.Add(FlapThrottle)
	if now.Before(at) {
		return
	}
	f.begin(f.pending, at)
}

// end is when the current flip has fully settled.
func (f *Flap) end() time.Time {
	return f.start.Add(time.Duration(f.slots+1) * FlapTick)
}

func (f *Flap) inFlight(now time.Time) bool {
	return f.flips && f.slots > 0 && now.Before(f.end()) && !now.Before(f.start)
}

// Cells is what to draw at now, one Cell per character of the current text.
func (f *Flap) Cells(now time.Time) []Cell {
	f.advance(now)
	out := make([]Cell, len(f.cur))
	flying := f.inFlight(now)
	for i, r := range f.cur {
		out[i] = Cell{R: r}
		if !flying || f.order[i] < 0 {
			continue
		}
		e := now.Sub(f.start) - time.Duration(f.order[i])*FlapTick
		switch {
		case e < 0: // not its turn yet: the old character
			if i < len(f.from) {
				out[i].R = f.from[i]
			} else {
				out[i].R = ' '
			}
		case e < FlapTick:
			out[i] = Cell{R: '▀', Phase: Top}
		case e < 2*FlapTick:
			out[i] = Cell{R: '▄', Phase: Bottom}
		}
	}
	return out
}

// Busy reports whether the flap needs another redraw: a flip is in flight,
// or a newer value is waiting for its throttle window.
func (f *Flap) Busy(now time.Time) bool {
	f.advance(now)
	return f.inFlight(now) || f.hasPending
}

// Next is how long until the flap's picture next changes, or 0 when it is
// idle and nothing needs scheduling.
func (f *Flap) Next(now time.Time) time.Duration {
	f.advance(now)
	if f.inFlight(now) {
		e := now.Sub(f.start)
		return FlapTick - e%FlapTick
	}
	if f.hasPending {
		if d := f.start.Add(FlapThrottle).Sub(now); d > 0 {
			return d
		}
		return time.Nanosecond
	}
	return 0
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
