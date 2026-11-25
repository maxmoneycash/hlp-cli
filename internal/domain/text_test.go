package domain

import "testing"

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Nice vault\x1b]52;c;MHhkZWFk\x07", "Nice vault]52;c;MHhkZWFk"},
		{"title\x1b]0;pwned\x1b\\", "title]0;pwned\\"},
		{"\u009b2J clear", "2J clear"},
		{"line1\nline2\tcol", "line1\nline2\tcol"},
		{"del\x7f", "del"},
		{"bidi\u202egnp.exe", "bidignp.exe"},
		{"iso\u2066late\u2069", "isolate"},
		{"zero\u200bwidth\ufeff", "zerowidth"},
		{"S&P 500 · ±2%", "S&P 500 · ±2%"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
