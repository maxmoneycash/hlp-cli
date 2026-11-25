//go:build !unix

package paperx

import "os"

// lockFile is a no-op where flock is unavailable (Windows): processes may
// race on one paper account.
func lockFile(*os.File) func() { return func() {} }
