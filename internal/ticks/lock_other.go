//go:build !unix

package ticks

import (
	"os"
	"sync"
)

// held stands in for flock where it is unavailable: one writer per coin
// within this process. Two processes may both write there, which only
// duplicates records (readers dedupe by tid).
var held sync.Map

func tryLock(path string) (func(), bool) {
	if _, loaded := held.LoadOrStore(path, true); loaded {
		return nil, false
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}
	return func() { held.Delete(path) }, true
}
