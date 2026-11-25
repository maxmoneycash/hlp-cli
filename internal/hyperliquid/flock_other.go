//go:build !unix

package hyperliquid

import "os"

// lockFile is a no-op where flock is unavailable (Windows): processes may
// race on the shared bucket, which only makes it slightly optimistic.
func lockFile(*os.File) func() { return func() {} }
