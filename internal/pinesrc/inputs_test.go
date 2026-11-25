package pinesrc

import "testing"

func TestInputKeys(t *testing.T) {
	src := `//@version=6
strategy("x")
fast = input.int(12, "Fast length", minval = 1)
tf = input.timeframe("D", "TF")
sym = input.symbol("BINANCE:BTCUSDT", "Symbol")
sess = input.session("0930-1600", title = "Session")
gen = input("x", "Generic")
txt = input.text_area("hello", "Notes")
src2 = input.string("close", "Source", options = ["close", "open"]) // pick one
noTitle = input.float(1.5)
`
	got := InputKeys(src)
	for k, want := range map[string]string{
		"fast": "Fast length", "fast length": "Fast length",
		"tf": "TF", "sym": "Symbol", "sess": "Session", "gen": "Generic",
		"txt": "Notes", "src2": "Source",
	} {
		if got[k] != want {
			t.Errorf("InputKeys[%q] = %q, want %q", k, got[k], want)
		}
	}
	for _, bad := range []string{"d", "x", "hello", "close", "notitle"} {
		if _, ok := got[bad]; ok {
			t.Errorf("default value %q taken as a title", bad)
		}
	}
}
