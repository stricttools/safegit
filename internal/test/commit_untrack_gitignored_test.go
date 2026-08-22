package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The cleanup workflow every git user knows -- add a pattern to .gitignore,
// `git rm -r --cached` the copies that are already tracked, commit both in one
// commit -- had no safegit-mediated form at all: naming the now-ignored path as
// an ordinary argument was refused precisely BECAUSE it was ignored, which is
// the reason it was being untracked.
//
// `--untrack <path>` is that form. It is not the same statement as a positional
// path and does not use the same spelling: a positional says "put this in the
// commit", and one naming an ignored file is still refused; --untrack says
// "take this out of the index and leave the file alone". The tests below pin
// the whole cleanup in one commit, and the fallback that commits only
// .gitignore, which still has to preserve a pre-staged removal.

// seedTrackedThenIgnoredFile builds the exact starting state of the report: a
// repo where dir/junk.txt is tracked and committed, .gitignore has just grown a
// `dir/` pattern (committed nowhere yet), and the operator has already run
// `git rm -r --cached dir` so the removal is staged while the file stays on
// disk.
func seedTrackedThenIgnoredFile(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/junk.txt", "build artifact\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add junk", "--", "dir/junk.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	// The pattern that makes the tracked file ignored from now on.
	testutil.WriteFile(t, dir, ".gitignore", "dir/\n")

	// The operator's own step 2: stage the removal, keep the file on disk.
	testutil.GitRaw(t, dir, "rm", "-r", "--cached", "dir")

	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Fatalf("dir/junk.txt must survive `git rm --cached` on disk: %v", err)
	}
	return dir
}

// TestCommitUntrackGitignoredPath is the headline case: one safegit commit that
// records both the new .gitignore pattern and the removal of the now-ignored
// file from tracking. The file is gitignored on purpose -- that is the point of
// the operation, not a mistake -- and it must stay on disk afterwards.
func TestCommitUntrackGitignoredPath(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "untrack",
		"--untrack", "dir/junk.txt", "--", ".gitignore")
	if code != 0 {
		t.Fatalf("safegit commit of the untrack cleanup failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	// The target IS gitignored, which is why it is being untracked, so the
	// "you probably want a .gitignore pattern" line must not fire.
	if strings.Contains(stderr, "not gitignored") {
		t.Errorf("untracking an already-ignored path must not suggest a .gitignore pattern; stderr:\n%s", stderr)
	}

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if !strings.Contains(diff, "\t.gitignore") {
		t.Errorf("expected the .gitignore change in the commit, got:\n%s", diff)
	}
	if !strings.Contains(diff, "D\tdir/junk.txt") {
		t.Errorf("expected dir/junk.txt to be deleted from the tree by the commit, got:\n%s", diff)
	}

	tree := testutil.GitRaw(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "dir/junk.txt") {
		t.Errorf("dir/junk.txt should no longer be tracked, HEAD tree:\n%s", tree)
	}

	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must remain on disk after being untracked: %v", err)
	}
}

// TestCommitUntrackGitignoredPathAppearsInThePayload: an untracked path is a
// tree change like any other, so it is in the payload's `files` -- which is the
// changed-path list read off the objects, never a copy of the argument list.
func TestCommitUntrackGitignoredPathAppearsInThePayload(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "-m", "untrack",
		"--untrack", "dir/junk.txt", "--", ".gitignore")
	if code != 0 {
		t.Fatalf("machine-mode untrack commit failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	var envelope struct {
		Payload struct {
			Files []string `json:"files"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("parsing the envelope: %v\nstdout=%s", err, stdout)
	}
	if !testutil.Contains(envelope.Payload.Files, "dir/junk.txt") {
		t.Errorf("the untracked path is missing from the payload's files: %v", envelope.Payload.Files)
	}
	if !testutil.Contains(envelope.Payload.Files, ".gitignore") {
		t.Errorf(".gitignore is missing from the payload's files: %v", envelope.Payload.Files)
	}
}

// TestCommitUntrackNonIgnoredPathWorksWithANotice: the flag's scope is general,
// not "only gitignored paths". Untracking a path nothing ignores is legal --
// and far more often than not the .gitignore edit that belongs with it was
// forgotten, so safegit says so instead of guessing.
func TestCommitUntrackNonIgnoredPathWorksWithANotice(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "other.txt", "other\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "stop tracking notes",
		"--untrack", "notes.txt", "--", "other.txt")
	if code != 0 {
		t.Fatalf("untracking a non-ignored path failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	tree := testutil.TreePaths(t, dir, "HEAD")
	if testutil.Contains(tree, "notes.txt") {
		t.Errorf("notes.txt should no longer be tracked, HEAD tree: %v", tree)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("notes.txt must remain on disk after being untracked: %v", err)
	}
	if !strings.Contains(stderr, "not gitignored") {
		t.Errorf("expected a notice that the untracked path is not gitignored, got stderr:\n%s", stderr)
	}
}

// TestCommitUntrackOfAnUntrackedPathIsAHardError: the flag's whole protection
// against a typo. Removing an index entry that is not there changes nothing, so
// a silent success would tell the caller a cleanup happened that did not.
func TestCommitUntrackOfAnUntrackedPathIsAHardError(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "kept.txt", "kept\n")
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "typo",
		"--untrack", "nevre-tracked.txt", "--", "kept.txt")
	if code != exitcode.PathMatchedNothing {
		t.Errorf("untracking a path that is not tracked exited %d, want %d (PathMatchedNothing); stderr: %s",
			code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "nevre-tracked.txt") {
		t.Errorf("the refusal must name the path the caller typed; stderr: %s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitGitignoredPathAsAPositionalIsStillRefused: --untrack is a different
// statement from a positional path, and only --untrack was exempted. Naming an
// ignored file as something to COMMIT is still refused, so the exemption cannot
// become a route for adding ignored content.
func TestCommitGitignoredPathAsAPositionalIsStillRefused(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "flagless form", "--", ".gitignore", "dir/junk.txt")
	if code == 0 {
		t.Fatalf("naming a gitignored path as a positional was accepted; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "gitignored") {
		t.Errorf("the refusal must say the path is gitignored; stderr: %s", stderr)
	}
}

// TestCommitGitignoreOnlyKeepsPreStagedRemoval is the fallback the report tried
// after the case above was refused: commit only the non-ignored path and hope
// the pre-staged removals survive for a later commit. They do -- the commit
// reconciles the shared index through the one authority that preserves every
// staged change the parent tip does not account for -- and this test pins that,
// so a regression to "the index is rebuilt from the new HEAD and the operator's
// staged removal is gone" is loud.
func TestCommitGitignoreOnlyKeepsPreStagedRemoval(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	before := testutil.GitRaw(t, dir, "diff", "--cached", "--name-status")
	t.Logf("staged before safegit commit:\n%s", before)
	if !strings.Contains(before, "D\tdir/junk.txt") {
		t.Fatalf("precondition: the removal must be staged before the commit, got:\n%s", before)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "ignore dir", "--", ".gitignore")
	if code != 0 {
		t.Fatalf("safegit commit of .gitignore alone failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	after := testutil.GitRaw(t, dir, "diff", "--cached", "--name-status")
	status := testutil.Git(t, dir, "status", "--porcelain")
	lsFiles, _ := testutil.GitTry(t, dir, "ls-files", "--", "dir/junk.txt")
	t.Logf("staged after safegit commit: %q", after)
	t.Logf("git status --porcelain after safegit commit: %q", status)
	t.Logf("git ls-files dir/junk.txt after safegit commit: %q", lsFiles)

	// The commit records only .gitignore, and the operator's pre-staged removal
	// is still staged afterwards: the shared index is reconciled against the
	// commit's PARENT, so a staged change the parent does not account for is
	// replayed over the new HEAD instead of being erased by it. The operator
	// can commit the removal in a second commit, which is the whole point of
	// the fallback.
	if !strings.Contains(after, "D\tdir/junk.txt") {
		t.Errorf("the pre-staged removal did not survive the commit -- another session's staged work was destroyed (staged: %q)", after)
	}
	if strings.TrimSpace(lsFiles) != "" {
		t.Errorf("dir/junk.txt is in the index again, so the staged removal was undone (ls-files: %q)", lsFiles)
	}
	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must stay on disk: the removal was staged with --cached: %v", err)
	}

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if strings.Contains(diff, "dir/junk.txt") {
		t.Errorf("the .gitignore-only commit should not touch dir/junk.txt, got:\n%s", diff)
	}
}
