package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommitMessageLongFlags: the commit message flags are spelled --message
// and --message-file, with -m and -F as their short forms.
func TestCommitMessageLongFlags(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "--message", "subject", "-m", "body", "--", "a.txt"); code != 0 {
		t.Fatalf("commit --message failed (code %d): %s", code, stderr)
	}
	if got := commitMessage(t, dir, "HEAD"); !strings.HasPrefix(got, "subject\n\nbody") {
		t.Fatalf("--message and -m did not join into one message: %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	msgPath := filepath.Join(dir, "msg.txt")
	if err := os.WriteFile(msgPath, []byte("from a file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "--message-file", msgPath, "--", "b.txt"); code != 0 {
		t.Fatalf("commit --message-file failed (code %d): %s", code, stderr)
	}
	if got := commitMessage(t, dir, "HEAD"); !strings.HasPrefix(got, "from a file") {
		t.Fatalf("--message-file did not supply the message: %q", got)
	}
}

// TestCommitMessageFlagsRefuseBoth: naming both message sources is refused,
// and the refusal names both flags by their long spelling.
func TestCommitMessageFlagsRefuseBoth(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	msgPath := filepath.Join(dir, "msg.txt")
	if err := os.WriteFile(msgPath, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "-F", msgPath, "--", "a.txt")
	if code == 0 {
		t.Fatal("commit with both -m and -F succeeded; want a refusal")
	}
	if !strings.Contains(stderr, "--message") || !strings.Contains(stderr, "--message-file") {
		t.Fatalf("refusal does not name --message and --message-file: %s", stderr)
	}
}

// TestMvMessageLongFlag: mv's required message flag is --message, -m for short.
func TestMvMessageLongFlag(t *testing.T) {
	dir := newRepo(t)
	if _, stderr, code := runSafegit(t, dir, "mv", "--message", "move seed", "seed.txt -> moved.txt"); code != 0 {
		t.Fatalf("mv --message failed (code %d): %s", code, stderr)
	}
	if got := commitMessage(t, dir, "HEAD"); !strings.HasPrefix(got, "move seed") {
		t.Fatalf("mv --message did not supply the message: %q", got)
	}
}

// TestSingleLetterLongMessageFlagsAreGone: the old long spellings --m and --F
// name no flag any more.
func TestSingleLetterLongMessageFlagsAreGone(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"commit", "--m", "x", "--", "a.txt"},
		{"commit", "--F", "msg.txt", "--", "a.txt"},
		{"merge-continue", "--m", "x"},
	} {
		if _, _, code := runSafegit(t, dir, args...); code == 0 {
			t.Errorf("%v succeeded; want the parser to refuse the old spelling", args)
		}
	}
}

// TestConclusionHelpNamesMessageFlag: the conclusion commands share the same
// message flag spelling.
func TestConclusionHelpNamesMessageFlag(t *testing.T) {
	dir := newRepo(t)
	for _, cmd := range []string{"merge-continue", "cherry-pick-continue", "revert-continue"} {
		stdout, stderr, code := runSafegit(t, dir, cmd, "--help")
		if code != 0 {
			t.Fatalf("%s --help failed (code %d): %s", cmd, code, stderr)
		}
		if !strings.Contains(stdout, "--message") {
			t.Errorf("%s --help does not list --message:\n%s", cmd, stdout)
		}
	}
}
