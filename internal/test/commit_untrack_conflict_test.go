package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Naming one path as a thing to COMMIT and as a thing to UNTRACK says two
// opposite things about the same commit, so it is refused rather than resolved.
//
// The refusal used to be spelled for files only: the check sat inside the
// branch that handles a single path, so the same contradiction written with a
// DIRECTORY -- `--untrack dir -- dir` -- fell through to the directory
// expansion, exited 0, and silently resolved to the removal. Which of the two
// arguments wins is exactly what the caller has not said.

var untrackConflictEnv = []string{"CLAUDE_CODE_SESSION_ID=untrack-conflict-test"}

// untrackConflictRepo tracks a directory of two files plus a file beside it.
func untrackConflictRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "dir/a.txt", "a\n")
	testutil.WriteFile(t, dir, "dir/b.txt", "b\n")
	testutil.WriteFile(t, dir, "other.txt", "other\n")
	safegitCommitEnv(t, dir, untrackConflictEnv, "seed", "dir/a.txt", "dir/b.txt", "other.txt")
	return dir
}

// TestCommitRefusesADirectoryNamedBothPositionallyAndInUntrack is the audit's
// reproduction, exactly.
func TestCommitRefusesADirectoryNamedBothPositionallyAndInUntrack(t *testing.T) {
	dir := untrackConflictRepo(t)
	head := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "other.txt", "changed\n")

	_, stderr, code := runSafegitEnv(t, dir, untrackConflictEnv, "commit", "-m", "contradiction",
		"--untrack", "dir", "--", "dir", "other.txt")

	if code != exitcode.Usage {
		t.Errorf("a directory named both positionally and in --untrack exited %d, want %d (Usage); stderr: %s",
			code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "dir") || !strings.Contains(stderr, "--untrack") {
		t.Errorf("the refusal must name the path and the flag it contradicts; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
		t.Errorf("the contradiction was resolved into a commit anyway: HEAD moved %s -> %s", head, got)
	}
}

// TestCommitRefusesAFileNamedBothPositionallyAndInUntrack is the spelling that
// was already refused, kept as the control the directory case is matched
// against: same exit code, same message.
func TestCommitRefusesAFileNamedBothPositionallyAndInUntrack(t *testing.T) {
	dir := untrackConflictRepo(t)
	head := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, untrackConflictEnv, "commit", "-m", "contradiction",
		"--untrack", "other.txt", "--", "other.txt")

	if code != exitcode.Usage {
		t.Errorf("a file named both positionally and in --untrack exited %d, want %d (Usage); stderr: %s",
			code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "--untrack") {
		t.Errorf("the refusal must name the flag it contradicts; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
		t.Errorf("HEAD moved %s -> %s despite the refusal", head, got)
	}
}

// TestCommitUntracksADirectoryNamedOnlyInUntrack is the other side of the
// refusal: a directory named ONLY by --untrack is not a contradiction at all,
// and every path under it still goes.
func TestCommitUntracksADirectoryNamedOnlyInUntrack(t *testing.T) {
	dir := untrackConflictRepo(t)

	testutil.WriteFile(t, dir, "other.txt", "changed\n")

	_, stderr, code := runSafegitEnv(t, dir, untrackConflictEnv, "commit", "-m", "untrack the directory",
		"--untrack", "dir", "--", "other.txt")
	if code != 0 {
		t.Fatalf("untracking a directory nothing else names failed (code %d): %s", code, stderr)
	}

	for _, path := range []string{"dir/a.txt", "dir/b.txt"} {
		if _, ok := testutil.Show(t, dir, "HEAD", path); ok {
			t.Errorf("%s is still tracked after being untracked", path)
		}
	}
	if content, ok := testutil.Show(t, dir, "HEAD", "other.txt"); !ok || !strings.Contains(content, "changed") {
		t.Errorf("the committed path did not go in: %q (present=%v)", content, ok)
	}
}
