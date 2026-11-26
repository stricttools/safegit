package lock

import "os"

// FileInfo does not expose the Windows file ID. The ownership check uses
// os.SameFile (volume serial and file ID); this is only a polling hint.
func inodeOf(info os.FileInfo) uint64 { return 0 }
