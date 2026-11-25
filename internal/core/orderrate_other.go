//go:build !unix

package core

import "os"

// lockFile is a no-op where flock is unavailable (Windows); the in-process
// mutex still serializes one process.
func lockFile(*os.File) func() { return func() {} }
