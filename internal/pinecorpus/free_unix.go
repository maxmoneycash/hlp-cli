//go:build unix

package pinecorpus

import "golang.org/x/sys/unix"

// freeBytes reports the free space available to this user under path.
func freeBytes(path string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
