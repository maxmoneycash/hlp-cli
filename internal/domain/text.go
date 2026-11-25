package domain

import "strings"

// CleanText removes characters that can drive or spoof a terminal from
// third-party text (vault names and descriptions, market names, hlp.xyz
// strings, model output): C0 and C1 controls except \n and \t, DEL, the bidi
// overrides U+202A-202E and U+2066-2069, and the zero-width U+200B-200F and
// U+FEFF. Dropping ESC and the C1 introducers defuses OSC 52 clipboard
// writes, title changes and cursor movement (SECURITY S3).
func CleanText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r >= 0x200b && r <= 0x200f, r == 0xfeff:
			return -1
		}
		return r
	}, s)
}
