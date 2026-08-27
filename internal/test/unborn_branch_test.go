package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// An UNBORN branch is the state a repository is in between `git init` and its
// first commit: HEAD names a branch no ref exists for, `git rev-parse HEAD`
// fails, and every git command that takes HEAD as a treeish is fatal. It is
// also the state `safegit undo` of a root commit leaves behind, and the state a
// fresh clone-by-fetch sits in before the first branch is checked out.
//
// Every guarded command used to refuse there, and not deliberately: the
// coordination check ran `git diff HEAD`, which is fatal, so switch, merge,
// pick, pull, reset and revert all died with git's "ambiguous argument 'HEAD'"
// before their own handlers decided anything.
//
// These tests pin what an unborn branch supports now, and what it deliberately
// refuses. The fixture shape is deliberate too: `git init` in an empty
// directory and then `git fetch <src> main:side`, which leaves HEAD unborn AND
// a branch present with commits on it. `git checkout --orphan` would NOT
// produce it -- its index keeps the old checkout's content, so the tree is
// dirty from the start.

// unbornFixture is a repository whose HEAD is unborn, holding a fetched branch
// `side` with two commits on it.
type unbornFixture struct {
	dir string
	// firstSHA creates file.txt; secondSHA modifies it. Picking the SECOND onto
	// the unborn head is a MODIFY/DELETE conflict, which is the only conflict an
	// unborn pick can have (an add-only pick applies cleanly onto nothing).
	firstSHA, secondSHA string
}

// newUnbornRepo builds the fixture. The source repository is thrown away except
// for the objects the fetch copied.
func newUnbornRepo(t *testing.T) unbornFixture {
	t.Helper()

	src := newRepo(t)
	testutil.WriteFile(t, src, "file.txt", "one\n")
	firstSHA := safegitCommit(t, src, "create file", "file.txt")
	testutil.WriteFile(t, src, "file.txt", "two\n")
	secondSHA := safegitCommit(t, src, "modify file", "file.txt")

	dir := evalTempDir(t)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"fetch", src, "main:side"},
	} {
		testutil.Git(t, dir, args...)
	}

	if _, ok := testutil.GitTryOut(t, dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
		t.Fatalf("fixture is not unborn: HEAD resolves")
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("fixture is not clean: %q", status)
	}
	return unbornFixture{dir: dir, firstSHA: firstSHA, secondSHA: secondSHA}
}

// readWorktreeFile reads a working-tree file as a string.
func readWorktreeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// assertOplogOutcome checks that the newest entry for op carries the outcome.
func assertOplogOutcome(t *testing.T, dir, op, want string) {
	t.Helper()
	entries := oplogEntries(t, dir, op)
	if len(entries) == 0 {
		t.Fatalf("no oplog entry for op %q", op)
	}
	last := entries[len(entries)-1]
	extra, _ := last["extra"].(map[string]interface{})
	if extra["outcome"] != want {
		t.Errorf("oplog outcome = %v, want %q (entry: %v)", extra["outcome"], want, last)
	}
}

func TestUnbornMergeFastForwards(t *testing.T) {
	f := newUnbornRepo(t)

	stdout, stderr, code := runSafegit(t, f.dir, "merge", "side")
	if code != 0 {
		t.Fatalf("safegit merge side on an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "fast-forwarded") {
		t.Errorf("merge did not report a fast-forward.\nstdout=%s", stdout)
	}
	if head := testutil.Rev(t, f.dir, "HEAD"); head != f.secondSHA {
		t.Errorf("HEAD = %s, want the incoming tip %s", head, f.secondSHA)
	}
	// The sync is the half a bare ref move leaves undone: without it git reports
	// the incoming file as a staged deletion and it is missing from disk.
	if status := testutil.Git(t, f.dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("working tree not in step with the new tip: %q", status)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "file.txt")); err != nil {
		t.Errorf("file.txt missing from the working tree after the fast-forward: %v", err)
	}
	assertOplogOutcome(t, f.dir, "merge", "fast-forward")
}

func TestUnbornSwitchBothForms(t *testing.T) {
	t.Run("onto an existing branch", func(t *testing.T) {
		f := newUnbornRepo(t)
		if _, stderr, code := runSafegit(t, f.dir, "switch", "side"); code != 0 {
			t.Fatalf("safegit switch side from an unborn HEAD: code %d\n%s", code, stderr)
		}
		if br := strings.TrimSpace(testutil.Git(t, f.dir, "symbolic-ref", "--short", "HEAD")); br != "side" {
			t.Errorf("on branch %q, want side", br)
		}
	})

	t.Run("creating a branch", func(t *testing.T) {
		f := newUnbornRepo(t)
		if _, stderr, code := runSafegit(t, f.dir, "switch", "-c", "fresh"); code != 0 {
			t.Fatalf("safegit switch -c fresh on an unborn HEAD: code %d\n%s", code, stderr)
		}
		if br := strings.TrimSpace(testutil.Git(t, f.dir, "symbolic-ref", "--short", "HEAD")); br != "fresh" {
			t.Errorf("on branch %q, want fresh", br)
		}
		// Still unborn: a branch created where there is no commit has no ref yet.
		if _, ok := testutil.GitTryOut(t, f.dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
			t.Errorf("switch -c on an unborn HEAD created a commit")
		}
	})
}

func TestUnbornDirtyTreeRefusesWithBothPathsListed(t *testing.T) {
	f := newUnbornRepo(t)
	testutil.WriteFile(t, f.dir, "staged.txt", "staged\n")
	testutil.Git(t, f.dir, "add", "staged.txt")
	testutil.WriteFile(t, f.dir, "untracked.txt", "untracked\n")

	_, stderr, code := runSafegit(t, f.dir, "merge", "side")
	if code != 5 {
		t.Fatalf("dirty unborn merge exited %d, want 5 (CoordinationBusy)\n%s", code, stderr)
	}
	for _, want := range []string{"staged.txt", "untracked.txt", "working tree is not clean"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
}

func TestUnbornCommitStillRoots(t *testing.T) {
	f := newUnbornRepo(t)
	testutil.WriteFile(t, f.dir, "new.txt", "new\n")

	if _, stderr, code := runSafegit(t, f.dir, "commit", "-m", "root", "--", "new.txt"); code != 0 {
		t.Fatalf("safegit commit on an unborn branch: code %d\n%s", code, stderr)
	}
	if n := gitLog(t, f.dir, "HEAD"); n != 1 {
		t.Errorf("commit count = %d, want 1 root commit", n)
	}
}

func TestUnbornCherryPickRootsWithTheAuthorPreserved(t *testing.T) {
	f := newUnbornRepo(t)

	stdout, stderr, code := runSafegit(t, f.dir, "cherry-pick", f.firstSHA)
	if code != 0 {
		t.Fatalf("safegit cherry-pick onto an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if n := gitLog(t, f.dir, "HEAD"); n != 1 {
		t.Errorf("commit count = %d, want 1 (the pick is a ROOT commit here)", n)
	}
	author := strings.TrimSpace(testutil.Git(t, f.dir, "log", "-1", "--format=%an <%ae>"))
	sourceAuthor := strings.TrimSpace(testutil.Git(t, f.dir, "log", "-1", "--format=%an <%ae>", f.firstSHA))
	if author != sourceAuthor {
		t.Errorf("author = %q, want the source's %q", author, sourceAuthor)
	}
	if got := readWorktreeFile(t, f.dir, "file.txt"); got != "one\n" {
		t.Errorf("file.txt = %q, want the picked content", got)
	}
	assertNoSequencerResidue(t, f.dir, "after a clean unborn pick")
}

// The one path that exercises the substituted first-parent uses in marker
// verification: a conflicted pick onto an unborn head, concluded through
// safegit. The conflict must be MODIFY/DELETE, because an add-only pick onto
// nothing applies cleanly -- so the index holds stages 1 and 3 and NO stage 2,
// which is what the resolution declaration has to be written against.
func TestUnbornConflictedCherryPickParksAndConcludes(t *testing.T) {
	f := newUnbornRepo(t)

	_, stderr, code := runSafegit(t, f.dir, "cherry-pick", f.secondSHA)
	if code == 0 {
		t.Fatalf("cherry-pick of the modifying commit onto an unborn head succeeded; the fixture needs a conflict\n%s", stderr)
	}
	stages := strings.TrimSpace(testutil.Git(t, f.dir, "ls-files", "-u", "file.txt"))
	if stages == "" {
		t.Fatalf("no unmerged stages for file.txt; the pick did not park a conflict")
	}
	for _, line := range strings.Split(stages, "\n") {
		if strings.Contains(line, "\tfile.txt") && strings.Contains(line, " 2\t") {
			t.Errorf("unexpected stage 2 in a modify/delete conflict:\n%s", stages)
		}
	}

	// theirs is the picked content: keeping it is what an operator concluding
	// this conflict means by "apply the pick".
	stdout, stderr, code := runSafegit(t, f.dir, "cherry-pick-continue", "--resolve", "file.txt=theirs")
	if code != 0 {
		t.Fatalf("cherry-pick-continue on an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if n := gitLog(t, f.dir, "HEAD"); n != 1 {
		t.Errorf("commit count = %d, want 1 (a conclusion onto an unborn head is a ROOT commit)", n)
	}
	if got := readWorktreeFile(t, f.dir, "file.txt"); got != "two\n" {
		t.Errorf("file.txt = %q, want the picked content", got)
	}
	assertNoSequencerResidue(t, f.dir, "after concluding an unborn pick")
}

func TestUnbornPullFastForwards(t *testing.T) {
	f := newUnbornRepo(t)
	// A remote whose branch is the one the fixture fetched: a pull is the merge
	// path with FETCH_HEAD as the incoming side.
	remote := newRepo(t)
	testutil.WriteFile(t, remote, "file.txt", "one\n")
	safegitCommit(t, remote, "create file", "file.txt")
	remoteTip := testutil.Rev(t, remote, "HEAD")
	testutil.Git(t, f.dir, "remote", "add", "origin", remote)

	stdout, stderr, code := runSafegit(t, f.dir, "pull", "--merge-strategy", "ff", "origin", "main")
	if code != 0 {
		t.Fatalf("safegit pull onto an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if head := testutil.Rev(t, f.dir, "HEAD"); head != remoteTip {
		t.Errorf("HEAD = %s, want the fetched tip %s", head, remoteTip)
	}
	if status := testutil.Git(t, f.dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("working tree not in step with the pulled tip: %q", status)
	}
}

func TestUnbornResetHardWorks(t *testing.T) {
	f := newUnbornRepo(t)
	if _, stderr, code := runSafegit(t, f.dir, "reset", "--hard", "side"); code != 0 {
		t.Fatalf("safegit reset --hard side on an unborn branch: code %d\n%s", code, stderr)
	}
	if head := testutil.Rev(t, f.dir, "HEAD"); head != f.secondSHA {
		t.Errorf("HEAD = %s, want %s", head, f.secondSHA)
	}
}

// A revert onto an unborn head reverts nothing: there is no parent tree to put
// back, so the result is the empty tree and the standing empty-result refusal
// is the honest answer. Before the root-path comparison it minted a nonsense
// empty root commit instead.
func TestUnbornRevertRefusesAsEmpty(t *testing.T) {
	f := newUnbornRepo(t)

	stdout, stderr, code := runSafegit(t, f.dir, "revert", f.firstSHA)
	if code == 0 {
		t.Fatalf("revert onto an unborn head succeeded; it should refuse as empty\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "no change") {
		t.Errorf("refusal does not say the revert produced no change:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if _, ok := testutil.GitTryOut(t, f.dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
		t.Errorf("the refused revert created a commit")
	}
	assertNoSequencerResidue(t, f.dir, "after a refused unborn revert")
}

// A preview of a plain unborn merge answers without computing anything:
// merge-tree needs two commits and there is only one, and the answer is known
// by definition.
func TestUnbornMergePreviewShortCircuits(t *testing.T) {
	f := newUnbornRepo(t)

	stdout, stderr, code := runSafegit(t, f.dir, "--dry-run", "merge", "side")
	if code != 0 {
		t.Fatalf("--dry-run merge side on an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "fast-forward") {
		t.Errorf("preview does not report a fast-forward:\n%s", stdout)
	}
	if _, ok := testutil.GitTryOut(t, f.dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
		t.Errorf("the preview created a commit")
	}
}

func TestUnbornCherryPickPreviewComputes(t *testing.T) {
	f := newUnbornRepo(t)

	stdout, stderr, code := runSafegit(t, f.dir, "--dry-run", "cherry-pick", f.firstSHA)
	if code != 0 {
		t.Fatalf("--dry-run cherry-pick on an unborn branch: code %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "would cherry-pick") {
		t.Errorf("preview said nothing about the pick:\n%s", stdout)
	}
	if _, ok := testutil.GitTryOut(t, f.dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
		t.Errorf("the preview created a commit")
	}
}
