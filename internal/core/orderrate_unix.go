//go:build unix

package core

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f and returns its release.
// A failed lock degrades to no lock.
func lockFile(f *os.File) func() {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return func() {}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
}
