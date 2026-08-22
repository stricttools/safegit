package lock

import (
	"os"
	"syscall"
)

// inodeOf returns the inode number of a stat result, or 0 when the platform
// does not expose one.
//
// safegit builds only for unix (the windows sources were removed with the
// platform), so the type assertion succeeds in practice; the zero fallback
// keeps holderIdentity comparison sound rather than panicking if it ever does
// not -- with the inode always zero, identity falls back to size and
// modification time, which is weaker but never wrong in the unsafe direction:
// a missed change costs a slower poll, never a stolen lock.
func inodeOf(info os.FileInfo) uint64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(st.Ino)
}
