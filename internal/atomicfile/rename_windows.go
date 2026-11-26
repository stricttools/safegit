// Package atomicfile provides same-volume atomic replacement. It never falls
// back to copy/delete, which could expose a partial destination.
package atomicfile

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"time"
)

func Rename(oldPath, newPath string) error {
	old, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	dest, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = windows.MoveFileEx(old, dest, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		// Ordinary Go readers do not share delete access. Retry the namespace
		// operation only; never remove a destination to make replacement succeed.
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || !time.Now().Before(deadline) {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
		}
		time.Sleep(time.Millisecond)
	}
}
