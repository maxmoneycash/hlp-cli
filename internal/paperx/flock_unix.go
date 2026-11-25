//go:build unix

package paperx

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f and returns its release.
// A failed lock degrades to no lock (paper state is local and advisory).
func lockFile(f *os.File) func() {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return func() {}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
}
