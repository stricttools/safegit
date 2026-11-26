//go:build !windows

package testutil

import (
	"os"
	"testing"
)

func WriteExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}
func CleanupExecutables() {}
