//go:build linux

package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A kernel without pidfds (Linux older than 5.3) cannot run a hook: every
// signal the sweep sends goes through a pidfd, so the run is refused with an
// error naming the missing support and the kernel requirement, before the hook
// is started. There is no fallback to signalling bare pids.
func TestNoPidfdSupportRefusesTheRun(t *testing.T) {
	prev := openPidfd
	openPidfd = func(int) (int, syscall.Errno) { return -1, syscall.ENOSYS }
	defer func() { openPidfd = prev }()

	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\ntouch '"+marker+"'\n")

	results, err := Run(context.Background(), store(gitDir), nil, 30, DefaultStopCap, nil, noWarn)
	if err == nil {
		t.Fatalf("the run succeeded without pidfd support; results %+v", results)
	}
	for _, want := range []string{"hook pre-pre-push: ", "pidfd", "Linux 5.3 or later", syscall.ENOSYS.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q: %v", want, err)
		}
	}
	if len(results) != 0 {
		t.Errorf("results = %+v, want none: the hook must not have run", results)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the hook ran without pidfd support")
	}
}
