//go:build !windows

package test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stricttools/safegit/internal/testutil"
)

// TestSignalStopsARunningGitSubprocess: every git subprocess safegit runs is
// tied to the framework's cancellation, so a SIGTERM that arrives while a
// command waits on git stops that git at once, and the command ends with
// 128 + the signal number instead of waiting for git to finish on its own.
//
// The git here is `backup list`'s ls-remote, against a remote whose upload-pack
// is a shell that records its pid and then blocks for a minute. The upload-pack
// lets go of the stderr it inherited, so once git itself is stopped nothing
// else holds the output safegit reads from git.
func TestSignalStopsARunningGitSubprocess(t *testing.T) {
	dir := newRepo(t)
	bare := evalTempDir(t)
	testutil.Git(t, bare, "init", "--bare", "-q")
	scratch := evalTempDir(t)
	marker := filepath.Join(scratch, "upload-pack.pid")
	testutil.Git(t, dir, "remote", "add", "slow", bare)
	testutil.Git(t, dir, "config", "remote.slow.uploadpack",
		"echo $$ > "+marker+".tmp && mv "+marker+".tmp "+marker+"; exec sleep 60 2>/dev/null; :")

	cmd := exec.Command(safegitBin, "backup", "list", "slow")
	cmd.Dir = dir
	cmd.Env = controlledEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	var pid int
	for pid == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if pid == 0 {
		cmd.Process.Kill()
		t.Fatal("the remote's upload-pack never started")
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling safegit: %v; stderr:\n%s", err, stderr.String())
	}
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("safegit was still waiting on git 10s after SIGTERM; stderr:\n%s", stderr.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Errorf("safegit ended with %v, want exit %d; stderr:\n%s", waitErr, 128+int(syscall.SIGTERM), stderr.String())
	}
}
