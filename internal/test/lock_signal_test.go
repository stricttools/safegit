package test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/testutil"
)

// lockFilesUnder lists every published lock file under a repository's safegit
// state directory. It is what "the process left its locks behind" looks like
// from outside the process.
func lockFilesUnder(t *testing.T, repoDir string) []string {
	t.Helper()
	var found []string
	root := filepath.Join(repoDir, ".git", "safegit")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".lock") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// waitForFile polls for a path to appear, failing the test when it does not.
func waitForFile(t *testing.T, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared within %v", path, within)
}

// TestSignalledCommitReleasesItsLocksAndExitsTheConventionalStatus: a safegit
// process that a signal ends while it holds a lock releases the lock and exits
// 128 + the signal number -- 143 for a SIGTERM.
//
// Both halves are the point. The release is what keeps the next contender from
// waiting out a lock whose holder is already gone, and the status is the Unix
// convention that a shell, a supervisor and a CI runner all read the same way;
// exiting the general failure code instead said "this command failed", which is
// not what happened.
//
// The window is held open by a pre-commit hook that waits: safegit runs git's
// pre-commit hook with the operation and ref locks already taken, so the
// process is signalled at exactly the moment the test needs.
func TestSignalledCommitReleasesItsLocksAndExitsTheConventionalStatus(t *testing.T) {
	dir := newRepo(t)

	scratch := evalTempDir(t)
	started := filepath.Join(scratch, "hook-started")
	release := filepath.Join(scratch, "hook-may-finish")

	// The hook detaches its own output so the test's read of safegit's pipes
	// cannot be held open by a hook that outlives the process, waits for the
	// release file, and gives up on its own so no fixture can hang the suite.
	writeHookScript(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), strings.Join([]string{
		"exec >/dev/null 2>&1",
		"touch " + started,
		"i=0",
		"while [ ! -f " + release + " ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done",
	}, "\n"))

	testutil.WriteFile(t, dir, "signalled.txt", "content\n")

	cmd := exec.Command(safegitBin, "commit", "-m", "signalled mid-commit", "--", "signalled.txt")
	cmd.Dir = dir
	cmd.Env = controlledEnv(t)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting safegit commit: %v", err)
	}
	defer func() {
		_ = os.WriteFile(release, []byte("go\n"), 0o644)
	}()

	waitForFile(t, started, 30*time.Second)

	// The premise: the process really is holding a lock at this moment, so the
	// assertion below is about a release and not about a lock that never existed.
	if held := lockFilesUnder(t, dir); len(held) == 0 {
		t.Fatal("the commit holds no lock file while its pre-commit hook runs, so this test would prove nothing")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the commit: %v", err)
	}
	// Let the hook finish on its own rather than outliving the test.
	if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("the signalled commit did not fail (err=%v)\noutput: %s", waitErr, out.String())
	}
	wantStatus := 128 + int(syscall.SIGTERM)
	if got := exitErr.ExitCode(); got != wantStatus {
		t.Errorf("the signalled commit exited %d, want %d (128 + SIGTERM)\noutput: %s", got, wantStatus, out.String())
	}

	if held := lockFilesUnder(t, dir); len(held) != 0 {
		t.Errorf("the signalled commit left its locks behind: %v", held)
	}
}
