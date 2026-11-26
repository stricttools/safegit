//go:build !windows

package testutil

import (
	"os"
	"testing"
)

func SetExecutable(t *testing.T, path string, enabled bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode()
	if enabled {
		mode |= 0111
	} else {
		mode &^= 0111
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func SetDirectoryWritable(t *testing.T, path string, enabled bool) {
	t.Helper()
	mode := os.FileMode(0555)
	if enabled {
		mode = 0755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
