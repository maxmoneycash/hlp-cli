//go:build !unix

package pinecorpus

// freeBytes is unknown on this platform; Fetch then skips the check.
func freeBytes(string) (uint64, bool) { return 0, false }
