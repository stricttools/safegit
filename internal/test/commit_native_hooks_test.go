package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The repository's own git hooks around a commit: pre-commit, commit-msg and
// post-commit, run from .git/hooks the way git runs them. safegit ran only
// pre-commit before, so a repository whose policy lives in a commit-msg hook
// had that policy silently skipped by every safegit commit.

// installHook writes an executable hook of the given name into .git/hooks.
func installHook(t *testing.T, dir, name, script string) {
	t.Helper()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

// hookMarkerLines returns the lines a hook appended to its marker file, or none
// when the hook never ran.
func hookMarkerLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// A commit-msg hook that exits nonzero is the repository saying no. Nothing may
// be committed: not the ref, not a message the hook rejected.
func TestCommitMsgHookRejectionAbortsWithNoCommit(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	baseSHA := safegitCommit(t, dir, "base", "base.txt")
	countBefore := gitLog(t, dir, "HEAD")

	installHook(t, dir, "commit-msg", "#!/bin/sh\necho 'commit-msg: nope' >&2\nexit 1\n")

	testutil.WriteFile(t, dir, "rejected.txt", "rejected\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "should never exist", "--", "rejected.txt")
	if code != exitcode.CommitHookRejected {
		t.Fatalf("exit code = %d, want %d (a hook refusal)\nstdout=%s\nstderr=%s",
			code, exitcode.CommitHookRejected, stdout, stderr)
	}
	if !strings.Contains(stderr, "commit-msg") {
		t.Errorf("stderr does not name the hook that refused: %s", stderr)
	}

	if head := testutil.Rev(t, dir, "HEAD"); head != baseSHA {
		t.Errorf("HEAD = %s, want the pre-commit tip %s: the rejected commit was created anyway", head, baseSHA)
	}
	if n := gitLog(t, dir, "HEAD"); n != countBefore {
		t.Errorf("commit count = %d, want %d (unchanged)", n, countBefore)
	}
}

// A pre-commit hook refusal exits the same way: one situation, one code.
func TestPreCommitHookRejectionExitsHookRejected(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	baseSHA := safegitCommit(t, dir, "base", "base.txt")

	installHook(t, dir, "pre-commit", "#!/bin/sh\nexit 1\n")

	testutil.WriteFile(t, dir, "blocked.txt", "blocked\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "blocked", "--", "blocked.txt")
	if code != exitcode.CommitHookRejected {
		t.Fatalf("exit code = %d, want %d\nstdout=%s\nstderr=%s",
			code, exitcode.CommitHookRejected, stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != baseSHA {
		t.Errorf("HEAD moved to %s despite the pre-commit refusal", head)
	}
}

// A commit-msg hook is allowed to REWRITE the message in place -- that is how
// issue-key and sign-off hooks work -- and the rewritten text is what gets
// committed. safegit's own session trailer goes on afterwards, so it survives
// on top of whatever the hook wrote.
func TestCommitMsgHookRewriteIsHonoredAndTrailerSurvives(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=commit-msg-rewrite-test"}

	installHook(t, dir, "commit-msg", "#!/bin/sh\nprintf 'rewritten by the hook\\n' > \"$1\"\n")

	testutil.WriteFile(t, dir, "rewritten.txt", "content\n")
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "original subject", "--", "rewritten.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	if subject := commitFormat(t, dir, "%s", "HEAD"); subject != "rewritten by the hook" {
		t.Errorf("subject = %q, want the hook's rewrite (full message:\n%s)", subject, commitMessage(t, dir, "HEAD"))
	}
	msg := commitMessage(t, dir, "HEAD")
	if strings.Contains(msg, "original subject") {
		t.Errorf("the message the hook replaced is still there:\n%s", msg)
	}
	if !strings.Contains(msg, "Claude-Code-Session-Id: commit-msg-rewrite-test") {
		t.Errorf("the session trailer did not survive the rewrite:\n%s", msg)
	}
}

// post-commit runs once per commit that really happened, and never for a
// preview -- a dry run performs no commit, so there is nothing to notify about.
func TestPostCommitHookRunsOncePerCommitAndNeverInDryRun(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(dir, "post-commit-ran.txt")
	installHook(t, dir, "post-commit", "#!/bin/sh\necho ran >> \""+marker+"\"\n")

	testutil.WriteFile(t, dir, "one.txt", "one\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "one", "--", "one.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	if got := hookMarkerLines(t, marker); len(got) != 1 {
		t.Fatalf("post-commit ran %d time(s) for one commit, want exactly 1", len(got))
	}

	testutil.WriteFile(t, dir, "two.txt", "two\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "--dry-run", "-m", "previewed", "--", "two.txt"); code != 0 {
		t.Fatalf("dry-run commit failed (code %d): %s", code, stderr)
	}
	if got := hookMarkerLines(t, marker); len(got) != 1 {
		t.Errorf("post-commit ran %d time(s) after a dry run, want the 1 from the real commit only", len(got))
	}
}

// A dry run runs no hook at all -- a hook is an arbitrary script whose effects
// safegit cannot preview -- and says which ones it did not run.
func TestDryRunRunsNoCommitHookAndSaysSo(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommit(t, dir, "base", "base.txt")

	marker := filepath.Join(dir, "hooks-ran.txt")
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit"} {
		installHook(t, dir, name, "#!/bin/sh\necho "+name+" >> \""+marker+"\"\nexit 1\n")
	}

	testutil.WriteFile(t, dir, "preview.txt", "preview\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "--dry-run", "-m", "preview", "--", "preview.txt")
	if code != 0 {
		t.Fatalf("the dry run hit a hook that should have been skipped (code %d)\nstdout=%s\nstderr=%s",
			code, stdout, stderr)
	}
	if got := hookMarkerLines(t, marker); len(got) != 0 {
		t.Errorf("a dry run ran hooks: %v", got)
	}
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("the preview does not say %s was skipped: %s", name, stderr)
		}
	}
}

// An amend is a commit as far as the repository's hooks are concerned: all
// three run, and a commit-msg rewrite is honored there too.
func TestAmendRunsNativeCommitHooks(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "amended.txt", "v1\n")
	safegitCommit(t, dir, "original", "amended.txt")

	marker := filepath.Join(dir, "amend-hooks.txt")
	installHook(t, dir, "pre-commit", "#!/bin/sh\necho pre-commit >> \""+marker+"\"\n")
	installHook(t, dir, "commit-msg", "#!/bin/sh\necho commit-msg >> \""+marker+"\"\nprintf 'amended by the hook\\n' > \"$1\"\n")
	installHook(t, dir, "post-commit", "#!/bin/sh\necho post-commit >> \""+marker+"\"\n")

	testutil.WriteFile(t, dir, "amended.txt", "v2\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "my subject", "--", "amended.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	ran := hookMarkerLines(t, marker)
	for _, want := range []string{"pre-commit", "commit-msg", "post-commit"} {
		if !testutil.Contains(ran, want) {
			t.Errorf("%s did not run on the amend; hooks that ran: %v", want, ran)
		}
	}
	if subject := commitFormat(t, dir, "%s", "HEAD"); subject != "amended by the hook" {
		t.Errorf("amend subject = %q, want the hook's rewrite", subject)
	}
}

// A reword stages nothing of its own, so its hooks read the tip's own tree --
// but they still run, and a commit-msg refusal still leaves the tip alone.
func TestRewordRunsNativeCommitHooks(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "reworded.txt", "content\n")
	safegitCommit(t, dir, "original", "reworded.txt")

	marker := filepath.Join(dir, "reword-hooks.txt")
	installHook(t, dir, "pre-commit", "#!/bin/sh\necho pre-commit >> \""+marker+"\"\n")
	installHook(t, dir, "commit-msg", "#!/bin/sh\necho commit-msg >> \""+marker+"\"\n")
	installHook(t, dir, "post-commit", "#!/bin/sh\necho post-commit >> \""+marker+"\"\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "reworded subject"); code != 0 {
		t.Fatalf("reword failed (code %d): %s", code, stderr)
	}

	ran := hookMarkerLines(t, marker)
	for _, want := range []string{"pre-commit", "commit-msg", "post-commit"} {
		if !testutil.Contains(ran, want) {
			t.Errorf("%s did not run on the reword; hooks that ran: %v", want, ran)
		}
	}

	tip := testutil.Rev(t, dir, "HEAD")
	installHook(t, dir, "commit-msg", "#!/bin/sh\nexit 1\n")
	if _, _, code := runSafegit(t, dir, "commit", "--amend", "-m", "never applied"); code != exitcode.CommitHookRejected {
		t.Errorf("reword exit code = %d, want %d for a commit-msg refusal", code, exitcode.CommitHookRejected)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused reword rewrote the tip anyway: %s, want %s", head, tip)
	}
}
