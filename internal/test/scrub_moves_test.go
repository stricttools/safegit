package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A move record is a REFERENCE to a path. When a rewrite erases that path from
// history, the record still pointing at it is one more copy of the thing being
// erased -- in the one place a tree rewrite does not reach, the commit message.
//
// So `scrub file --delete` removes the records naming the path IN THE SAME
// REWRITE, and declares the message change so the rewrite's own verification
// accounts for it. `--replace-with` does not: replacing a file's contents keeps
// the path, so a record naming it is still true.

// seedMovedSecret builds a history whose move record names the path a scrub is
// about to reach for, and returns the repository and the record's id.
func seedMovedSecret(t *testing.T) (string, string) {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "secret.env", "TOKEN=hunter2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed the secret", "--", "secret.env"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move the secret into config",
		"secret.env -> config/secret.env"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 1 {
		t.Fatalf("the fixture declared no record: %v", records)
	}
	return dir, records[0][0]
}

// moveCommitMessage finds the rewritten commit whose subject is the move's and
// returns its message.
func moveCommitMessage(t *testing.T, dir string) string {
	t.Helper()
	sha := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "move the secret into config"))
	if sha == "" {
		t.Fatal("the move commit is not in the rewritten history")
	}
	return commitMessageOf(t, dir, sha)
}

func TestScrubFileDeleteRemovesTheRecordsNamingThePath(t *testing.T) {
	dir, id := seedMovedSecret(t)

	stdout, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--delete", "--entire-history", "--reason", "the token leaked", "config/secret.env")
	if code != 0 {
		t.Fatalf("scrub file --delete failed (code %d): %s\n%s", code, stderr, stdout)
	}

	msg := moveCommitMessage(t, dir)
	if strings.Contains(msg, "Moved:") {
		t.Errorf("the record naming the erased path survived the rewrite:\n%s", msg)
	}
	if strings.Contains(msg, id) {
		t.Errorf("the erased record's id survived:\n%s", msg)
	}
	if !strings.HasPrefix(msg, "move the secret into config") {
		t.Errorf("the rewrite changed more of the message than the record:\n%s", msg)
	}
	// The rewrite's own verification accounts for the message change rather
	// than refusing it as a change nothing asked for.
	if strings.Contains(stderr, "no operation asked for") {
		t.Errorf("verification treated the record removal as undeclared: %s", stderr)
	}
	if !strings.Contains(stdout, "commit message") {
		t.Errorf("the summary does not report the message change:\n%s", stdout)
	}
}

func TestScrubFileReplaceWithKeepsTheRecords(t *testing.T) {
	dir, _ := seedMovedSecret(t)

	sanitized := filepath.Join(t.TempDir(), "sanitized.env")
	testutil.WriteFileAt(t, sanitized, "TOKEN=redacted\n")

	if _, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--replace-with", sanitized, "--entire-history", "--reason", "the token leaked",
		"config/secret.env"); code != 0 {
		t.Fatalf("scrub file --replace-with failed (code %d): %s", code, stderr)
	}

	msg := moveCommitMessage(t, dir)
	// The path is still there, holding sanitized content: a record naming it is
	// still a true statement about where the content went.
	if !strings.Contains(msg, "Moved:") {
		t.Errorf("a content replacement removed a record that is still true:\n%s", msg)
	}
	if got := testutil.Git(t, dir, "show", "HEAD:config/secret.env"); got != "TOKEN=redacted" {
		t.Errorf("the replacement content is %q", got)
	}
}

func TestScrubFileDeleteLeavesUnrelatedRecordsAlone(t *testing.T) {
	dir, _ := seedMovedSecret(t)

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "rename the notes", "notes.txt -> README.txt"); code != 0 {
		t.Fatalf("second mv failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--delete", "--entire-history", "--reason", "the token leaked", "config/secret.env"); code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	notes := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "rename the notes"))
	if notes == "" {
		t.Fatal("the unrelated move commit is not in the rewritten history")
	}
	if records := movedRecordsIn(t, commitMessageOf(t, dir, notes)); len(records) != 1 {
		t.Errorf("an unrelated record was removed: %v", records)
	}
}
