// Package filelock provides file locking for append operations, using flock(2)
// for advisory locking. There is no Windows implementation on purpose: the
// appended files have no line-size limit, so the lock -- not POSIX O_APPEND
// atomicity -- is the only thing that keeps concurrent appends intact, and a
// no-op lock would silently mean no integrity at all. A Windows build fails to
// compile here; see todo/.defer/windows-lockfileex-support.md.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockedAppend opens path with O_WRONLY|O_APPEND|O_CREATE, acquires an advisory
// lock, writes data, and releases the lock. If mkdirAll is true, the parent
// directory is created first.
func LockedAppend(path string, data []byte, mkdirAll bool) error {
	if mkdirAll {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("locking %s: %w", path, err)
	}
	defer unlockFile(f)

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing to %s: %w", path, err)
	}

	return nil
}
