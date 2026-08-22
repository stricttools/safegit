package main

import (
	"path/filepath"

	"github.com/smm-h/safegit/internal/filelock"
)

// appendJSONLLine appends one pre-marshaled JSON line to <sgDir>/<filename>.
//
// Uses O_APPEND with advisory locking for concurrency safety, the same pattern
// as oplog.Append: the lock, not POSIX append atomicity, is what guarantees
// line integrity, so lines have no size limit.
//
// It lives on its own rather than inside any one writer's file because it is
// the shared spelling of "append a record to a safegit JSONL log": the
// rewrite-map journal is written through it today, and any future log is
// expected to be as well.
func appendJSONLLine(sgDir, filename string, data []byte) error {
	line := append(data[:len(data):len(data)], '\n')
	path := filepath.Join(sgDir, filename)
	return filelock.LockedAppend(path, line, true)
}
