package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These tests are the cannot-happen guards for the cross-session failure mode
// that automatic move DETECTION used to produce, and that its deletion closed
// structurally.
//
// The shape: session A deletes a tracked file from its working tree, intending
// to commit that deletion later. Session B, sharing the same worktree, commits
// an unrelated NEW file whose content happens to hash to the same blob as A's
// deleted file -- trivially so for empty files, and easily so for any copied
// boilerplate. Move detection classified A's deleted path as the "source" of
// B's "move" and STAGED THAT DELETION into B's commit, which never named it.
//
// What closed it is that a commit contains the paths its caller named and
// nothing else. That is unchanged and is what these tests pin: B's commit
// contains B's file, and A's deletion stays A's to commit.
//
// safegit does now MINT A RECORD for a move a commit's own delta witnesses --
// but the two are different acts, and the difference is exactly what makes the
// failure above unreachable. A record is a line in a message; it stages
// nothing, adopts nothing, and can only speak about paths the commit ALREADY
// contains on both sides. A's deleted path is not in B's commit at all, so
// nothing about it can be paired, recorded, or swept in. The fixtures stay,
// because a colliding blob is not rare and a future shortcut that reached back
// into another session's pending deletion would have to make one of these fail.

// crossSessCommitPaths returns the sorted set of paths touched by the given
// commit, each prefixed with its status letter (e.g. "A todo/b.txt",
// "D notes/a.txt"). Rename detection is deliberately OFF: this reports the raw
// tree delta, which is what a reviewer of the published commit sees when the
// two paths are unrelated.
func crossSessCommitPaths(t *testing.T, repoDir, rev string) []string {
	t.Helper()
	out := testutil.GitRaw(t, repoDir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", rev)
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		paths = append(paths, fields[0]+" "+fields[len(fields)-1])
	}
	sort.Strings(paths)
	return paths
}

// crossSessMkdirAll creates a directory tree inside the repo.
func crossSessMkdirAll(t *testing.T, repoDir, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repoDir, rel), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
}

// crossSessRemove deletes a file from the working tree WITHOUT staging the
// deletion -- exactly what a concurrent session does when it removes a file it
// intends to commit later.
func crossSessRemove(t *testing.T, repoDir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repoDir, rel)); err != nil {
		t.Fatalf("remove %s: %v", rel, err)
	}
}

// TestCrossSessionNoAdoption_EmptyFileCollision is the trivial-collision
// case: every empty file in a repository shares the same blob
// (e69de29bb2d1d6434b8b29ae775ad8c2e48c5391). Session A deletes a tracked
// empty file; session B commits an unrelated new empty file. B's commit
// contains only B's file.
func TestCrossSessionNoAdoption_EmptyFileCollision(t *testing.T) {
	dir := newRepo(t)

	// Session A's file: tracked, empty, in its own directory.
	crossSessMkdirAll(t, dir, "notes")
	testutil.WriteFile(t, dir, "notes/a.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds notes/a.txt", "--", "notes/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	// Session A deletes it from disk, intending to commit the deletion later.
	crossSessRemove(t, dir, "notes/a.txt")

	// Session B, unaware of A, adds a brand-new empty file and commits ONLY it.
	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", "")
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}

	// B's commit holds one path, so nothing in it can be paired with anything:
	// the record engine reads B's OWN delta, and A's pending deletion is not in
	// it. No record, and above all no staged deletion.
	assertInferredPairs(t, dir)

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD"))
	t.Logf("session B stderr: %q", stderr)

	want := []string{"A todo/b.txt"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("session B's commit adopted paths it never named.\n  want: %v\n  got:  %v\nstderr from B's commit: %q",
			want, got, stderr)
	}

	// Session A's deletion must still be A's to commit.
	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "notes/a.txt") {
		t.Fatalf("expected notes/a.txt to remain an uncommitted deletion in session A's working tree, got status:\n%s", status)
	}
}

// TestCrossSessionNoAdoption_IdenticalContentCollision is the same shape with
// non-empty content: two unrelated files that happen to hold byte-identical
// text (a license header, a stub, a copied boilerplate config).
func TestCrossSessionNoAdoption_IdenticalContentCollision(t *testing.T) {
	dir := newRepo(t)

	const shared = "# TODO\n\n- [ ] fill this in\n"

	crossSessMkdirAll(t, dir, "notes")
	testutil.WriteFile(t, dir, "notes/a.txt", shared)
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds notes/a.txt", "--", "notes/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	crossSessRemove(t, dir, "notes/a.txt")

	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", shared)
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}

	// B's commit holds one path, so nothing in it can be paired with anything:
	// the record engine reads B's OWN delta, and A's pending deletion is not in
	// it. No record, and above all no staged deletion.
	assertInferredPairs(t, dir)

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD"))
	t.Logf("session B stderr: %q", stderr)

	want := []string{"A todo/b.txt"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("session B's commit adopted paths it never named.\n  want: %v\n  got:  %v\nstderr from B's commit: %q",
			want, got, stderr)
	}

	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "notes/a.txt") {
		t.Fatalf("expected notes/a.txt to remain an uncommitted deletion, got status:\n%s", status)
	}
}

// TestCrossSessionNoAdoption_UnderQuiet closes the observability half of the
// old problem. The notice that disclosed an adopted deletion was suppressed by
// --quiet, so a quiet session had no signal at all that another session's
// deletion had been folded into its commit. Nothing is adopted under --quiet
// either, so there is nothing left for a suppressed notice to hide.
func TestCrossSessionNoAdoption_UnderQuiet(t *testing.T) {
	dir := newRepo(t)

	crossSessMkdirAll(t, dir, "notes")
	testutil.WriteFile(t, dir, "notes/a.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds notes/a.txt", "--", "notes/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	crossSessRemove(t, dir, "notes/a.txt")

	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", "")
	stdout, stderr, code := runSafegit(t, dir, "--quiet", "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B quiet commit failed (code %d): %s", code, stderr)
	}

	// B's commit holds one path, so nothing in it can be paired with anything:
	// the record engine reads B's OWN delta, and A's pending deletion is not in
	// it. No record, and above all no staged deletion.
	assertInferredPairs(t, dir)

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD"))
	t.Logf("session B stdout=%q stderr=%q", stdout, stderr)

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("under --quiet, session B's commit adopted %v with no notice on stdout (%q) or stderr (%q)",
			got, stdout, stderr)
	}
}

// TestCrossSessionNoAdoption_UnderMachineMode is the machine-mode half of the
// same observability question. globalFlags.silent() is `quiet || json`, so
// --json suppressed the stderr notice too, and a tool driving safegit received
// no representation of the adopted deletion at all. Machine mode adopts nothing
// either -- and the envelope's payload now says, positively, which paths the
// commit holds.
func TestCrossSessionNoAdoption_UnderMachineMode(t *testing.T) {
	dir := newRepo(t)

	crossSessMkdirAll(t, dir, "notes")
	testutil.WriteFile(t, dir, "notes/a.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds notes/a.txt", "--", "notes/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	crossSessRemove(t, dir, "notes/a.txt")

	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", "")
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B json commit failed (code %d): stdout=%q stderr=%q", code, stdout, stderr)
	}

	// B's commit holds one path, so nothing in it can be paired with anything:
	// the record engine reads B's OWN delta, and A's pending deletion is not in
	// it. No record, and above all no staged deletion.
	assertInferredPairs(t, dir)

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD"))
	t.Logf("session B --json stdout=%q stderr=%q", stdout, stderr)

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("under --json, session B's commit adopted %v; envelope on stdout was %q and stderr was %q, "+
			"neither naming the adopted path", got, stdout, stderr)
	}

	var doc commitPayloadDoc
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
		t.Fatalf("commit payload does not decode: %v\nstdout: %s", err, stdout)
	}
	if strings.Join(doc.Files, ",") != "todo/b.txt" {
		t.Errorf("the payload reports files %v; session B named only todo/b.txt", doc.Files)
	}
}

// TestCrossSessionNoAdoption_MultipleDeletedShareBlob is the tie-break variant.
// Session A deletes TWO tracked files that share a blob with session B's new
// file. The old code picked exactly one by path similarity, falling back to
// lexicographic order on a tie, so which of A's files got swept into B's commit
// was decided by alphabetical accident. Neither was an acceptable answer, and
// now neither is picked.
func TestCrossSessionNoAdoption_MultipleDeletedShareBlob(t *testing.T) {
	dir := newRepo(t)

	crossSessMkdirAll(t, dir, "notes")
	crossSessMkdirAll(t, dir, "archive")
	testutil.WriteFile(t, dir, "notes/a.txt", "")
	testutil.WriteFile(t, dir, "archive/a.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds two empty files", "--",
		"notes/a.txt", "archive/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	crossSessRemove(t, dir, "notes/a.txt")
	crossSessRemove(t, dir, "archive/a.txt")

	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", "")
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}

	// B's commit holds one path, so nothing in it can be paired with anything:
	// the record engine reads B's OWN delta, and A's pending deletion is not in
	// it. No record, and above all no staged deletion.
	assertInferredPairs(t, dir)

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD"))
	t.Logf("session B stderr: %q", stderr)
	t.Logf("working tree after B's commit:\n%s", testutil.Git(t, dir, "status", "--porcelain"))

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("with two blob-identical deleted files present, session B's commit contains %v; "+
			"one of session A's deletions was swept in, which is a choice between two paths "+
			"B never named. stderr: %q", got, stderr)
	}
}

// TestCrossSessionNoAdoption_VictimCommitsItsOwnDeletion follows the old damage
// downstream: once session B's commit had silently absorbed A's deletion, A's
// own attempt to commit it had nothing left to record. A's deletion is still
// A's, so A commits it.
func TestCrossSessionNoAdoption_VictimCommitsItsOwnDeletion(t *testing.T) {
	dir := newRepo(t)

	crossSessMkdirAll(t, dir, "notes")
	testutil.WriteFile(t, dir, "notes/a.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "session A adds notes/a.txt", "--", "notes/a.txt")
	if code != 0 {
		t.Fatalf("seeding commit failed (code %d): %s", code, stderr)
	}

	crossSessRemove(t, dir, "notes/a.txt")

	crossSessMkdirAll(t, dir, "todo")
	testutil.WriteFile(t, dir, "todo/b.txt", "")
	_, bStderr, code := runSafegit(t, dir, "commit", "-m", "session B adds todo/b.txt", "--", "todo/b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}
	bHead := testutil.Git(t, dir, "show", "--name-status", "--format=commit %H%n%s", "HEAD")
	// Same property from B's side: B's commit holds B's path alone, so it
	// carries no record and adopted nothing.
	assertInferredPairs(t, dir)

	// Now session A commits the deletion it has been holding.
	aStdout, aStderr, aCode := runSafegit(t, dir, "commit", "-m", "session A removes notes/a.txt", "--", "notes/a.txt")
	t.Logf("session B commit was:\n%s\n(stderr %q)", bHead, bStderr)
	t.Logf("session A deletion commit: code=%d stdout=%q stderr=%q", aCode, aStdout, aStderr)

	if aCode != 0 {
		t.Fatalf("session A could not commit its own deletion of notes/a.txt (code %d): stdout=%q stderr=%q\n"+
			"session B's commit had already absorbed it:\n%s", aCode, aStdout, aStderr, bHead)
	}

	aPaths := crossSessCommitPaths(t, dir, "HEAD")
	if len(aPaths) != 1 || aPaths[0] != "D notes/a.txt" {
		t.Fatalf("session A's deletion commit records %v, expected exactly [D notes/a.txt]; "+
			"the deletion was already taken by session B's commit:\n%s", aPaths, bHead)
	}
}
