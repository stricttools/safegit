package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// commitFormat and commitMessage are the suite's two ways of reading a commit's
// own message back out of git, and both run the same `git log -1 --format=...`
// through testutil.GitOut -- stdout only, so nothing git writes to stderr can
// end up inside a message the test then asserts on.
//
// commitFormat strips the trailing newline the format itself emits, because its
// callers compare a subject or body against an exact string. commitMessage
// keeps git's output verbatim, because its callers print or search the whole
// message and the trailing newline is part of it.

// commitFormat returns `git log -1 --format=<format>` for the given ref, with
// the trailing newline the format itself emits stripped.
func commitFormat(t *testing.T, dir, format, ref string) string {
	t.Helper()
	return strings.TrimRight(testutil.GitOut(t, dir, "log", "-1", "--format="+format, ref), "\n")
}

// commitMessage returns the full commit message of the given ref, verbatim.
func commitMessage(t *testing.T, dir, ref string) string {
	t.Helper()
	return testutil.GitOut(t, dir, "log", "-1", "--format=%B", ref)
}

// Repeated -m values must be joined with a blank line between them, matching
// `git commit -m a -m b`: the first value is the subject, the second is the
// body. Joining with a single newline makes git treat the whole thing as one
// subject, so `git log --oneline` prints every paragraph on one line.
func TestCommitMultipleMessagesSeparatedByBlankLine(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "multi.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "subject line", "-m", "body paragraph", "--", "multi.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	if subject := commitFormat(t, dir, "%s", "HEAD"); subject != "subject line" {
		t.Errorf("subject = %q, want %q (full message:\n%s)", subject, "subject line", commitMessage(t, dir, "HEAD"))
	}
	if body := commitFormat(t, dir, "%b", "HEAD"); body != "body paragraph" {
		t.Errorf("body = %q, want %q (full message:\n%s)", body, "body paragraph", commitMessage(t, dir, "HEAD"))
	}
}

// The same shape for --amend with files and for the reword path (--amend with
// no files): each has its own copy of the join, so each needs its own case.
func TestAmendMultipleMessagesSeparatedByBlankLine(t *testing.T) {
	dir := newRepo(t)

	path := filepath.Join(dir, "amend-multi.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "original", "--", "amend-multi.txt"); code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	if err := os.WriteFile(path, []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "amended subject", "-m", "amended body", "--", "amend-multi.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	if subject := commitFormat(t, dir, "%s", "HEAD"); subject != "amended subject" {
		t.Errorf("amend subject = %q, want %q (full message:\n%s)", subject, "amended subject", commitMessage(t, dir, "HEAD"))
	}
	if body := commitFormat(t, dir, "%b", "HEAD"); body != "amended body" {
		t.Errorf("amend body = %q, want %q (full message:\n%s)", body, "amended body", commitMessage(t, dir, "HEAD"))
	}
}

func TestRewordMultipleMessagesSeparatedByBlankLine(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "reword-multi.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "original", "--", "reword-multi.txt"); code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "reworded subject", "-m", "reworded body")
	if code != 0 {
		t.Fatalf("reword failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	if subject := commitFormat(t, dir, "%s", "HEAD"); subject != "reworded subject" {
		t.Errorf("reword subject = %q, want %q (full message:\n%s)", subject, "reworded subject", commitMessage(t, dir, "HEAD"))
	}
	if body := commitFormat(t, dir, "%b", "HEAD"); body != "reworded body" {
		t.Errorf("reword body = %q, want %q (full message:\n%s)", body, "reworded body", commitMessage(t, dir, "HEAD"))
	}
}
