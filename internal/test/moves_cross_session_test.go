package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These tests cover the cross-session failure mode of automatic move
// detection: session A deletes a tracked file from its working tree, intending
// to commit that deletion later. Session B, sharing the same worktree, commits
// an unrelated NEW file whose content happens to hash to the same blob as A's
// deleted file. detectMoves (internal/commit/moves.go) then classifies A's
// deleted path as the "source" of B's "move" and auto-stages the deletion into
// B's commit, which never named that path.
//
// The existing TestMoveDetection_UnrelatedDeletion in moves_test.go does NOT
// cover this: its unrelated deletion (a.txt, "content A") has a different blob
// from the renamed file (b.txt -> c.txt, "content B"), so the blob lookup in
// moves.go never produces a candidate. These tests exercise the case where the
// blobs collide -- trivially for empty files, and deliberately for identical
// non-empty content.

// crossSessNameStatus returns `git show --name-status --format=%s` for the
// given rev as a trimmed string, for evidence in failure messages.
func crossSessNameStatus(t *testing.T, repoDir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "show", "--name-status", "--format=commit %H%n%s", rev)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show --name-status %s failed: %v\n%s", rev, err, out)
	}
	return strings.TrimSpace(string(out))
}

// crossSessCommitPaths returns the sorted set of paths touched by the given
// commit, each prefixed with its status letter (e.g. "A todo/b.txt",
// "D notes/a.txt"). Rename detection is deliberately OFF: this reports the raw
// tree delta, which is what a reviewer of the published commit sees when the
// two paths are unrelated.
func crossSessCommitPaths(t *testing.T, repoDir, rev string) []string {
	t.Helper()
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", rev)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff-tree %s failed: %v\n%s", rev, err, out)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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

// TestCrossSessionMoveDetection_EmptyFileCollision is the trivial-collision
// case: every empty file in a repository shares the same blob
// (e69de29bb2d1d6434b8b29ae775ad8c2e48c5391). Session A deletes a tracked
// empty file; session B commits an unrelated new empty file. B's commit must
// contain only B's file.
func TestCrossSessionMoveDetection_EmptyFileCollision(t *testing.T) {
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

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", crossSessNameStatus(t, dir, "HEAD"))
	t.Logf("session B stderr: %q", stderr)

	want := []string{"A todo/b.txt"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("session B's commit adopted paths it never named.\n  want: %v\n  got:  %v\nstderr from B's commit: %q",
			want, got, stderr)
	}

	// Session A's deletion must still be A's to commit.
	status := gitStatusPorcelain(t, dir)
	if !strings.Contains(status, "notes/a.txt") {
		t.Fatalf("expected notes/a.txt to remain an uncommitted deletion in session A's working tree, got status:\n%s", status)
	}
}

// TestCrossSessionMoveDetection_IdenticalContentCollision is the same shape
// with non-empty content: two unrelated files that happen to hold byte-identical
// text (a license header, a stub, a copied boilerplate config).
func TestCrossSessionMoveDetection_IdenticalContentCollision(t *testing.T) {
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

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", crossSessNameStatus(t, dir, "HEAD"))
	t.Logf("session B stderr: %q", stderr)

	want := []string{"A todo/b.txt"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("session B's commit adopted paths it never named.\n  want: %v\n  got:  %v\nstderr from B's commit: %q",
			want, got, stderr)
	}

	status := gitStatusPorcelain(t, dir)
	if !strings.Contains(status, "notes/a.txt") {
		t.Fatalf("expected notes/a.txt to remain an uncommitted deletion, got status:\n%s", status)
	}
}

// TestCrossSessionMoveDetection_QuietIsSilentAdoption pins the observability
// half of the problem: with --quiet the auto-staged-deletion notice is not
// printed at all, so session B has no signal whatsoever that another session's
// deletion was folded into its commit.
func TestCrossSessionMoveDetection_QuietIsSilentAdoption(t *testing.T) {
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

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", crossSessNameStatus(t, dir, "HEAD"))
	t.Logf("session B stdout=%q stderr=%q", stdout, stderr)

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("under --quiet, session B's commit adopted %v with no notice on stdout (%q) or stderr (%q)",
			got, stdout, stderr)
	}
}

// TestCrossSessionMoveDetection_JSONModeIsSilentAdoption is the machine-mode
// half of the same observability question. globalFlags.silent() is
// `quiet || json` (main.go:84), so `--json` suppresses the stderr notice too,
// and `commit` declares no PayloadSchema -- so the envelope carries no payload
// in which the adopted deletion could appear. A tool driving safegit in machine
// mode therefore receives no representation of it at all.
func TestCrossSessionMoveDetection_JSONModeIsSilentAdoption(t *testing.T) {
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

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", crossSessNameStatus(t, dir, "HEAD"))
	t.Logf("session B --json stdout=%q stderr=%q", stdout, stderr)

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("under --json, session B's commit adopted %v; envelope on stdout was %q and stderr was %q, "+
			"neither naming the adopted path", got, stdout, stderr)
	}
}

// TestCrossSessionMoveDetection_MultipleDeletedShareBlob is the tie-break
// variant. Session A deletes TWO tracked files that share a blob with session
// B's new file. moves.go picks exactly one by pathSimilarity, falling back to
// lexicographic order on a tie -- so which of A's files gets swept into B's
// commit is decided by alphabetical accident. Neither is an acceptable answer:
// B named neither path.
func TestCrossSessionMoveDetection_MultipleDeletedShareBlob(t *testing.T) {
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

	got := crossSessCommitPaths(t, dir, "HEAD")
	t.Logf("session B commit contents:\n%s", crossSessNameStatus(t, dir, "HEAD"))
	t.Logf("session B stderr: %q", stderr)
	t.Logf("working tree after B's commit:\n%s", gitStatusPorcelain(t, dir))

	if len(got) != 1 || got[0] != "A todo/b.txt" {
		t.Fatalf("with two blob-identical deleted files present, session B's commit contains %v; "+
			"exactly one of session A's deletions was picked by path-similarity tie-break "+
			"(lexicographic on an equal score), which is an arbitrary choice between two paths B never named. stderr: %q",
			got, stderr)
	}
}

// TestCrossSessionMoveDetection_VictimCommitBecomesEmpty follows the damage
// downstream: after session B's commit silently absorbed A's deletion, A's own
// attempt to commit that deletion has nothing left to record.
func TestCrossSessionMoveDetection_VictimCommitBecomesEmpty(t *testing.T) {
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
	bHead := crossSessNameStatus(t, dir, "HEAD")

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
