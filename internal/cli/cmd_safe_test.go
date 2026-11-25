package cli

import "testing"

// These paths fail before any network read.
func TestSafeRegisterNeedsDryRun(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "safe", "register-agent", "--safe", "0x1111111111111111111111111111111111111111", "--agent", "0x7e57a6e0000000000000000000000000000000c1")
	if r.code != 3 || errCode(t, r.stderr) != "SAFETY_REFUSED" {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "safe", "register-agent", "--safe", "nope", "--agent", "0x7e57a6e0000000000000000000000000000000c1", "--dry-run")
	if r.code != 2 || errCode(t, r.stderr) != "USAGE" {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "safe", "info", "0x12")
	if r.code != 2 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
}
