package cards

import "testing"

// Screener names stay apart at narrow widths: the operator prefix moves to
// a short tag and long names lose their middle, not their end.
func TestScreenerNamesStayDistinct(t *testing.T) {
	if got := screenerName("[ Systemic Strategies ] HyperGrowth"); got != "HyperGrowth [Systemic]" {
		t.Fatalf("prefix tag: %q", got)
	}
	a := midTruncate("Systemic Strategies HyperGrowth Long", 22)
	b := midTruncate("Systemic Strategies HyperGrowth Short", 22)
	if a == b || len([]rune(a)) > 22 {
		t.Fatalf("%q vs %q", a, b)
	}
}
