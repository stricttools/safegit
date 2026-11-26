// Package filelock serializes unlimited-size append records with flock on Unix
// and whole-file LockFileEx locks on Windows. No platform uses a no-op lock.
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

	f, err := openAppend(path)
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
