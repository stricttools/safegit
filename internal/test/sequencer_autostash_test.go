package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The two pieces of merge state a conclusion must handle but did not: the
// AUTOSTASH git set aside before the merge began, and the rerere resolution
// index.
//
// `git merge --autostash` (and merge.autoStash, and `git pull --autostash`)
// takes the operator's uncommitted work out of the way, records the
// stash-shaped commit in .git/MERGE_AUTOSTASH, and puts it back when the merge
// ends -- `git merge --continue` prints "Applied autostash." and removes the
// file. A conclusion that committed and walked away left the file behind and
// never restored the work, so the operator's uncommitted changes silently
// reverted to committed content with nothing on screen to say so.
//
// safegit's own `merge` cannot produce this state (its coordination guard
// refuses a dirty working tree outright), which is exactly why the fixture uses
// raw git: the operator reached the conflicted state through git, and
// `safegit merge-continue` is what safegit tells them to run next.

// autostashFixture is a repository parked in a conflicted merge that carries an
// autostash.
type autostashFixture struct {
	dir string
	// stashedPath is the file whose uncommitted content git set aside.
	stashedPath string
	// stashedContent is what that file held before git stashed it.
	stashedContent string
	// mainSHA and featureSHA are the two parents a correct conclusion carries.
	mainSHA    string
	featureSHA string
}

// newAutostashMergeRepo builds the fixture. sideOnFeature decides whether the
// merge itself changes the file the autostash touches, which is what makes the
// difference between an autostash that applies cleanly and one that conflicts.
func newAutostashMergeRepo(t *testing.T, sideOnFeature bool) autostashFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nbase\nl3\n")
	testutil.WriteFile(t, dir, "side.txt", "side base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "conflicted.txt", "side.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nfeature\nl3\n")
	featurePaths := []string{"conflicted.txt"}
	if sideOnFeature {
		testutil.WriteFile(t, dir, "side.txt", "side from feature\n")
		featurePaths = append(featurePaths, "side.txt")
	}
	featureSHA := safegitCommitEnv(t, dir, conclusionSession, "feature edit", featurePaths...)

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nmain\nl3\n")
	mainSHA := safegitCommitEnv(t, dir, conclusionSession, "main edit", "conflicted.txt")

	// The operator's uncommitted work, which the autostash sets aside.
	const uncommitted = "uncommitted work that exists nowhere else\n"
	testutil.WriteFile(t, dir, "side.txt", uncommitted)

	// Raw git: safegit's merge refuses a dirty working tree, so this is the only
	// door an autostash comes through.
	out, code := testutil.GitTry(t, dir, "merge", "--autostash", "feature")
	if code == 0 {
		t.Fatalf("git merge --autostash feature succeeded; the fixture needs a conflict:\n%s", out)
	}
	if !strings.Contains(out, "CONFLICT") {
		t.Fatalf("git merge --autostash did not report a conflict (code %d):\n%s", code, out)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")) {
		t.Fatalf("the fixture must carry an autostash; git wrote no MERGE_AUTOSTASH:\n%s", out)
	}
	// The autostash really did take the work out of the tree.
	if got := readWorktree(t, dir, "side.txt"); got == uncommitted {
		t.Fatalf("git did not stash the uncommitted change; side.txt still holds %q", got)
	}

	return autostashFixture{
		dir:            dir,
		stashedPath:    "side.txt",
		stashedContent: uncommitted,
		mainSHA:        mainSHA,
		featureSHA:     featureSHA,
	}
}

// readWorktree returns a repo-relative file's content from disk.
func readWorktree(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// TestMergeConclusionAppliesTheAutostash: the conclusion puts the operator's
// uncommitted work back, says so, and removes the file -- git's own semantics
// for `merge --continue`.
func TestMergeConclusionAppliesTheAutostash(t *testing.T) {
	fx := newAutostashMergeRepo(t, false)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs")
	if code != 0 {
		t.Fatalf("merge-continue on an autostashed merge: exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if got := readWorktree(t, fx.dir, fx.stashedPath); got != fx.stashedContent {
		t.Errorf("the autostashed work was not restored: %s holds %q, want %q", fx.stashedPath, got, fx.stashedContent)
	}
	if testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("MERGE_AUTOSTASH survived the conclusion")
	}
	if !strings.Contains(stdout+stderr, "autostash") {
		t.Errorf("the conclusion must say it applied the autostash\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	// The commit itself is still the correct merge commit.
	head := testutil.Rev(t, fx.dir, "HEAD")
	parents := testutil.Parents(t, fx.dir, head)
	if len(parents) != 2 || parents[0] != fx.mainSHA || parents[1] != fx.featureSHA {
		t.Errorf("parents are %v, want [%s %s]", parents, fx.mainSHA, fx.featureSHA)
	}
	assertNoSequencerResidue(t, fx.dir, "autostashed merge")
}

// TestMergeConclusionKeepsAConflictingAutostashRecoverable: when the stash
// cannot be applied over the merge result, the commit STANDS, the work is
// parked somewhere the operator can reach it, the output names that place, and
// the exit code is nonzero so the partial outcome is not read as clean.
func TestMergeConclusionKeepsAConflictingAutostashRecoverable(t *testing.T) {
	fx := newAutostashMergeRepo(t, true)
	before := testutil.Rev(t, fx.dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs")
	if code == 0 {
		t.Fatalf("a conflicting autostash apply must not exit 0\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	// The commit stands.
	head := testutil.Rev(t, fx.dir, "HEAD")
	if head == before {
		t.Fatalf("the conclusion commit was not made: HEAD is still %s", before)
	}
	parents := testutil.Parents(t, fx.dir, head)
	if len(parents) != 2 {
		t.Errorf("the conclusion has %d parent(s), want 2: %v", len(parents), parents)
	}

	// The work is recoverable, and the output says where it is.
	stashList := testutil.Git(t, fx.dir, "stash", "list")
	if strings.TrimSpace(stashList) == "" {
		t.Fatalf("the autostashed work is not recoverable: the stash list is empty\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// Named exactly, not merely mentioned: the operator has to be able to read
	// the recovery off the message.
	for _, want := range []string{"stash@{0}", "git stash pop"} {
		if !strings.Contains(stdout+stderr, want) {
			t.Errorf("the output must name %q so the work can be recovered\nstdout: %s\nstderr: %s", want, stdout, stderr)
		}
	}
	// The stash really holds the operator's content.
	stashed := testutil.Git(t, fx.dir, "show", "stash@{0}:"+fx.stashedPath)
	if !strings.Contains(stashed, "uncommitted work") {
		t.Errorf("the stash entry does not hold the operator's work: %q", stashed)
	}
	if testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("MERGE_AUTOSTASH survived alongside a real stash entry: the work is now recorded twice")
	}
}

// TestMergeConclusionDryRunLeavesTheAutostashAlone: a preview changes nothing,
// the autostash included, and states the apply it would perform.
func TestMergeConclusionDryRunLeavesTheAutostashAlone(t *testing.T) {
	fx := newAutostashMergeRepo(t, false)
	stashedBefore := readWorktree(t, fx.dir, fx.stashedPath)
	before := testutil.Rev(t, fx.dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--dry-run", "--resolve", "conflicted.txt=theirs")
	if code != 0 {
		t.Fatalf("merge-continue --dry-run: exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("the preview consumed MERGE_AUTOSTASH")
	}
	if got := readWorktree(t, fx.dir, fx.stashedPath); got != stashedBefore {
		t.Errorf("the preview wrote the working tree: %s = %q, want %q", fx.stashedPath, got, stashedBefore)
	}
	if after := testutil.Rev(t, fx.dir, "HEAD"); after != before {
		t.Errorf("the preview moved the branch: %s -> %s", before, after)
	}
	if !strings.Contains(stdout, "autostash") {
		t.Errorf("the preview must state that the autostash would be applied\nstdout: %s", stdout)
	}
}

// TestMergeConclusionRemovesMergeRR: rerere's resolution index is part of the
// state a concluded merge leaves behind, and git removes it when git commits.
func TestMergeConclusionRemovesMergeRR(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "config", "rerere.enabled", "true")

	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "conflicted.txt")
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nfeature\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "feature edit", "conflicted.txt")
	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edit", "conflicted.txt")

	if _, code := testutil.GitTry(t, dir, "merge", "feature"); code == 0 {
		t.Fatal("the fixture needs a conflict")
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_RR")) {
		t.Skip("this git wrote no MERGE_RR for the conflicted merge; there is nothing to clean up")
	}

	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}
	assertNoSequencerResidue(t, dir, "merge concluded with rerere enabled")
}
