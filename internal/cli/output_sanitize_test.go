package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
)

// TestHumanErrorStripsControls: a shared arena file names an entrant path
// carrying an OSC 52 clipboard write; the stat error must reach the TTY
// without ESC or BEL (SECURITY S3).
func TestHumanErrorStripsControls(t *testing.T) {
	var b bytes.Buffer
	writeHumanError(&b, &core.Error{
		Code:    core.CodeUsage,
		Message: "stat ex/x\x1b]52;c;MHhkZWFk\x07.pine: no such file",
		Hint:    "see \x1b]0;pwned\x07",
		Details: []core.PineDiag{{Line: 1, Col: 2, Msg: "bad \x9b2J"}},
	})
	if s := b.String(); strings.ContainsAny(s, "\x1b\x07\u009b") {
		t.Fatalf("raw controls in human error: %q", s)
	}
}
