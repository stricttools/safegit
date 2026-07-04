// Package filelock provides platform-safe file locking for append operations.
// On Unix, uses flock(2) for advisory locking. On Windows, flock is a no-op
// since the write sizes are small enough for atomic O_APPEND guarantees and
// scrub policies are serialized by a coordination lock.
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
