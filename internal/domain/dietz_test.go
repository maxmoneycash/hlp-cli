package domain

import (
	"math"
	"testing"
)

func TestModifiedDietz(t *testing.T) {
	cases := []struct {
		name       string
		a0, a1, dp float64
		want       float64
		ok         bool
	}{
		{"no flow equals simple return", 100, 110, 10, 0.10, true},
		{"mid-period deposit halves into base", 100, 130, 10, 10.0 / 110, true},
		{"withdrawal shrinks base", 100, 90, 10, 10.0 / 90, true},
		{"flow at 50% is unmeasurable", 100, 160, 10, 0, false},
		{"large withdrawal unmeasurable", 100, 40, -5, 0, false},
		{"no starting capital", 0, 1000, 5, 0, false},
		{"NaN", 100, math.NaN(), 1, 0, false},
		{"wipeout floors", 100, 0.00001, -99.99999, DietzFloor, true},
	}
	for _, c := range cases {
		r, ok := ModifiedDietz(c.a0, c.a1, c.dp)
		if ok != c.ok || math.Abs(r-c.want) > 1e-12 {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", c.name, r, ok, c.want, c.ok)
		}
	}
}
