//go:build !unix

package pyne

import "os/exec"

// ownGroup is a no-op where process groups are not available; the
// context still kills the pyne process itself.
func ownGroup(*exec.Cmd) {}
