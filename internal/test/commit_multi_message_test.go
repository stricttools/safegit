package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commitFormat returns `git log -1 --format=<format>` for the given ref, with
// the trailing newline the format itself emits stripped.
func commitFormat(t *testing.T, dir, format, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format="+format, ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log -1 --format=%s %s: %v", format, ref, err)
	}
	return strings.TrimRight(string(out), "\n")
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
