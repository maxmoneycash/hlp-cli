package pine

import (
	"math"
	"strconv"
)

type kind uint8

const (
	kNa kind = iota
	kNum
	kBool
	kStr
	kColor
	kTuple
	kMissing // optional argument not supplied
)

// value is a Pine runtime value. Numeric na is NaN.
type value struct {
	k kind
	f float64
	s string
	t []value
}

var (
	naV      = value{k: kNa, f: math.NaN()}
	missingV = value{k: kMissing, f: math.NaN()}
	trueV    = value{k: kBool, f: 1}
	falseV   = value{k: kBool, f: 0}
)

func num(f float64) value { return value{k: kNum, f: f} }

func boolean(b bool) value {
	if b {
		return trueV
	}
	return falseV
}

func str(s string) value { return value{k: kStr, s: s} }

func (v value) isNa() bool {
	switch v.k {
	case kNa, kMissing:
		return true
	case kNum, kBool:
		return math.IsNaN(v.f)
	case kColor:
		return v.s == ""
	}
	return false
}

func (v value) missing() bool { return v.k == kMissing }

// numOr returns the numeric value, or def when the argument is missing.
func (v value) numOr(def float64) float64 {
	if v.k == kMissing {
		return def
	}
	return v.f
}

// truthy is Pine's condition semantics: na and false are false; numbers are
// true when non-zero.
func (v value) truthy() bool {
	switch v.k {
	case kBool:
		return v.f == 1
	case kNum:
		return !math.IsNaN(v.f) && v.f != 0
	case kStr:
		return v.s != ""
	}
	return false
}

func (v value) String() string {
	switch v.k {
	case kNa, kMissing:
		return "NaN"
	case kNum:
		if math.IsNaN(v.f) {
			return "NaN"
		}
		return strconv.FormatFloat(v.f, 'f', -1, 64)
	case kBool:
		if v.f == 1 {
			return "true"
		}
		return "false"
	case kStr, kColor:
		return v.s
	case kTuple:
		s := "["
		for i, e := range v.t {
			if i > 0 {
				s += ", "
			}
			s += e.String()
		}
		return s + "]"
	}
	return ""
}

// maxBarsBack bounds history kept per series (Pine's default is 5000).
const maxBarsBack = 5001

// series stores one value per bar for the last maxBarsBack bars. carry
// series (var variables) fill skipped bars with the previous value; others
// fill them with na.
type series struct {
	buf   []value
	first int // bar index of the first stored value
	last  int // bar index of the last stored value (-1: empty)
	carry bool
}

func newSeries(carry bool) *series { return &series{last: -1, first: -1, carry: carry} }

func (s *series) pos(bar int) int { return (bar - s.first) % maxBarsBack }

// set stores v at bar (bar >= s.last).
func (s *series) set(bar int, v value) {
	if s.last == -1 {
		s.first, s.last = bar, bar
		s.buf = append(s.buf[:0], v)
		return
	}
	if bar < s.last {
		return
	}
	for s.last < bar {
		fill := naV
		if s.carry {
			fill = s.buf[s.pos(s.last)]
		}
		s.last++
		s.put(s.last, fill)
	}
	s.put(bar, v)
}

func (s *series) put(bar int, v value) {
	i := bar - s.first
	if i < maxBarsBack && i >= len(s.buf) {
		s.buf = append(s.buf, v)
		return
	}
	s.buf[s.pos(bar)] = v
}

// get returns the value at bar, na when out of range. For carry series a bar
// after the last set returns the last value.
func (s *series) get(bar int) value {
	if s.last == -1 || bar < s.first || bar < s.last-maxBarsBack+1 {
		return naV
	}
	if bar > s.last {
		if s.carry {
			return s.buf[s.pos(s.last)]
		}
		return naV
	}
	return s.buf[s.pos(bar)]
}

// ring keeps the last n floats (for window indicators).
type ring struct {
	buf  []float64
	n    int
	next int
	cnt  int
}

func newRing(n int) *ring {
	if n < 1 {
		n = 1
	}
	return &ring{buf: make([]float64, n), n: n}
}

func (r *ring) push(v float64) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % r.n
	if r.cnt < r.n {
		r.cnt++
	}
}

// at returns the value k pushes ago (0 = latest).
func (r *ring) at(k int) float64 {
	if k >= r.cnt || k < 0 {
		return math.NaN()
	}
	return r.buf[(r.next-1-k+2*r.n)%r.n]
}

func (r *ring) full() bool { return r.cnt == r.n }
