package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The recorded external effect must call the native API, not MSYS's copy-style
// ln fallback, and arbitrary legal path characters must stay data, not code.
func TestWindowsNativeSymlinkEffectQuotesData(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link '[x]; name")
	target := "target '; throw 'injected"
	argv := symlinkCommand(target, link)
	args := make([]string, 0, len(argv)-1)
	for _, a := range argv[1:] {
		args = append(args, a.(string))
	}
	cmd := exec.CommandContext(context.Background(), argv[0].(string), args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native symlink effect: %v %s", err, out)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("not a symlink: %v %v", info, err)
	}
	actual, err := os.Readlink(link)
	if err != nil || actual != target {
		t.Fatalf("link text = %q, want %q: %v", actual, target, err)
	}
}
