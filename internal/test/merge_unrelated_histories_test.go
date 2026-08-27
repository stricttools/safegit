package test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Merging two histories that share no commit at all.
//
// git refuses this by default and offers `--allow-unrelated-histories` to
// override. safegit refuses it TWICE and offers nothing: the flag is outside
// the subset, and a merge whose two sides have no merge base is refused before
// anything computes even when no flag was typed -- because the way an operator
// usually reaches this state is a mistake (a wrong remote, a wrong branch, a
// repository re-initialized over another), and git's own words for it name
// nothing they can act on.
//
// The legitimate case -- importing another project's history, once in a
// repository's lifetime -- is not blocked, only routed: raw git computes the
// merge with --no-commit, and safegit's own conclusion commits it.

// newUnrelatedHistoriesRepo builds a repository with TWO ROOT commits: `main`
// and `other` share no commit, so a merge between them has no merge base.
func newUnrelatedHistoriesRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommit(t, dir, "the main side", "main.txt")

	// An ORPHAN branch is git's own way to start a second root.
	testutil.Git(t, dir, "switch", "--orphan", "other")
	testutil.WriteFile(t, dir, "other.txt", "other\n")
	safegitCommit(t, dir, "the other root", "other.txt")

	testutil.Git(t, dir, "switch", "main")
	return dir
}

// TestTheUnrelatedHistoriesFlagIsRefused: the override git offers is not part
// of safegit's subset, so an operator cannot elect the merge with a flag.
func TestTheUnrelatedHistoriesFlagIsRefused(t *testing.T) {
	dir := newUnrelatedHistoriesRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stderr := assertSubsetRefusal(t, dir, "merge", "--allow-unrelated-histories", "other")
	if !strings.Contains(stderr, "merge-continue") {
		t.Errorf("the refusal does not name the import route:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
}

// TestMergeRefusesWhenThereIsNoMergeBase: the pre-flight, which fires with no
// flag typed at all. It is the whole point of the pair -- the flag refusal
// alone would leave an operator meeting git's bare fatal.
func TestMergeRefusesWhenThereIsNoMergeBase(t *testing.T) {
	dir := newUnrelatedHistoriesRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "merge", "other")
	if code != exitcode.General {
		t.Fatalf("safegit merge exited %d, want %d (General)\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "no commit") && !strings.Contains(stderr, "merge base") {
		t.Errorf("the refusal does not say what is wrong with the two sides:\n%s", stderr)
	}
	if !strings.Contains(stderr, "merge-continue") {
		t.Errorf("the refusal does not name the import route:\n%s", stderr)
	}
	// The DIAGNOSIS, pinned: in an ordinary repository no merge base really does
	// mean the two sides were never connected, and the message says so. The
	// shallow-clone tests below pin the other branch of the same refusal.
	if !strings.Contains(stderr, "were never connected") {
		t.Errorf("the refusal does not state the never-connected diagnosis:\n%s", stderr)
	}
	if strings.Contains(stderr, "shallow") {
		t.Errorf("the refusal blames a shallow clone in a repository that is not one:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	assertNoSequencerResidue(t, dir, "refused merge")

	// It is a fact about where the branches stand rather than about what was
	// typed, so it is recorded -- exactly like the --ff-only refusal beside it.
	entries := oplogEntries(t, dir, "merge")
	if len(entries) != 1 {
		t.Fatalf("expected one merge oplog entry for the refusal, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if outcome, _ := extra["outcome"].(string); outcome != "failed" {
		t.Errorf("the refusal entry records outcome %q, want failed; extra=%v", outcome, extra)
	}
}

// The preview refuses identically, because a preview of a command that cannot
// run is not a preview of anything -- and it records nothing.
func TestAPreviewOfAnUnrelatedMergeRefusesToo(t *testing.T) {
	dir := newUnrelatedHistoriesRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "merge", "other")
	if code != exitcode.General {
		t.Fatalf("the preview exited %d, want %d (General)\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "merge-continue") {
		t.Errorf("the preview's refusal does not name the import route:\n%s", stderr)
	}
	if strings.Contains(stdout, "run: git") {
		t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
	}
	// A preview writes no oplog entry, refusal or not.
	if n := len(oplogEntries(t, dir, "merge")); n != 0 {
		t.Errorf("the previewed refusal wrote %d oplog entry/entries", n)
	}
}

// newShallowCloneWithHiddenBase builds the OTHER repository `git merge-base`
// answers "no base" for: a shallow clone whose two branches really do share a
// base, one that lies BELOW the fetch depth and is therefore not in this
// object store at all. Nothing here is unrelated -- only unfetched.
//
// The clone is made over file:// on purpose: a local-path clone hardlinks the
// whole object store and --depth is ignored, so the shallow shape only exists
// over a transport.
func newShallowCloneWithHiddenBase(t *testing.T) string {
	t.Helper()
	source := newRepo(t)

	// The base both branches descend from, and then a commit on each side, so
	// the base sits one commit below each tip and a depth-1 fetch misses it.
	testutil.WriteFile(t, source, "base.txt", "base\n")
	safegitCommit(t, source, "the shared base", "base.txt")
	testutil.Git(t, source, "branch", "other")

	testutil.WriteFile(t, source, "main.txt", "main\n")
	safegitCommit(t, source, "the main side", "main.txt")

	testutil.Git(t, source, "switch", "other")
	testutil.WriteFile(t, source, "other.txt", "other\n")
	safegitCommit(t, source, "the other side", "other.txt")
	testutil.Git(t, source, "switch", "main")

	clone := filepath.Join(evalTempDir(t), "clone")
	cmd := exec.Command("git", "clone", "--quiet", "--depth", "1", "--no-single-branch",
		"file://"+source, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v\n%s", err, out)
	}
	testutil.Git(t, clone, "config", "user.email", "test@test.com")
	testutil.Git(t, clone, "config", "user.name", "Test")

	// The fixture is only a fixture if git really made it shallow, and if
	// merge-base really reports no base across the boundary.
	if out := testutil.Git(t, clone, "rev-parse", "--is-shallow-repository"); out != "true" {
		t.Fatalf("the clone is not shallow: rev-parse --is-shallow-repository said %q", out)
	}
	cmd = exec.Command("git", "merge-base", "HEAD", "origin/other")
	cmd.Dir = clone
	if err := cmd.Run(); err == nil {
		t.Fatal("merge-base found a base in the shallow clone: the fixture no longer hides it")
	}
	return clone
}

// TestMergeInAShallowCloneDoesNotCallTheHistoriesUnrelated: the refusal itself
// is right -- raw git refuses this merge too -- but the DIAGNOSIS the ordinary
// branch gives is false here. The histories are connected; the connection is
// simply below the fetch depth, and the way forward is to deepen the clone, not
// to import a foreign project.
func TestMergeInAShallowCloneDoesNotCallTheHistoriesUnrelated(t *testing.T) {
	dir := newShallowCloneWithHiddenBase(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "merge", "origin/other")
	if code != exitcode.General {
		t.Fatalf("safegit merge exited %d, want %d (General)\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "shallow") {
		t.Errorf("the refusal does not say the repository is shallow:\n%s", stderr)
	}
	if !strings.Contains(stderr, "git fetch --unshallow") {
		t.Errorf("the refusal does not name the way forward:\n%s", stderr)
	}
	if strings.Contains(stderr, "were never connected") {
		t.Errorf("the refusal states the never-connected diagnosis in a shallow clone:\n%s", stderr)
	}
	if strings.Contains(stderr, "merge-continue") {
		t.Errorf("the refusal offers the import route in a shallow clone:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	assertNoSequencerResidue(t, dir, "refused merge")

	// Same refusal, so the same record: one entry, failed.
	entries := oplogEntries(t, dir, "merge")
	if len(entries) != 1 {
		t.Fatalf("expected one merge oplog entry for the refusal, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if outcome, _ := extra["outcome"].(string); outcome != "failed" {
		t.Errorf("the refusal entry records outcome %q, want failed; extra=%v", outcome, extra)
	}
}

// The preview says the same thing, because the second pre-flight site reads the
// same message -- and it still records nothing.
func TestAPreviewOfAShallowMergeSaysTheSameThing(t *testing.T) {
	dir := newShallowCloneWithHiddenBase(t)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "merge", "origin/other")
	if code != exitcode.General {
		t.Fatalf("the preview exited %d, want %d (General)\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "shallow") || !strings.Contains(stderr, "git fetch --unshallow") {
		t.Errorf("the preview's refusal is not the shallow-aware one:\n%s", stderr)
	}
	if strings.Contains(stderr, "were never connected") {
		t.Errorf("the preview states the never-connected diagnosis in a shallow clone:\n%s", stderr)
	}
	if strings.Contains(stdout, "run: git") {
		t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
	}
	// A fresh clone has no oplog file at all until something writes one, which is
	// itself the assertion: a preview writes no entry, refusal or not.
	if n := len(oplogEntriesIfAny(t, dir, "merge")); n != 0 {
		t.Errorf("the previewed refusal wrote %d oplog entry/entries", n)
	}
}

// TestPullInheritsTheNoMergeBaseRefusal: the pre-flight sits in the merge step
// both commands share, so a pull of an unrelated remote history meets it too.
func TestPullInheritsTheNoMergeBaseRefusal(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "local.txt", "local\n")
	safegitCommitEnv(t, dir, pullSession, "the local side", "local.txt")

	// A second root, pushed to origin as the branch the pull will name.
	testutil.Git(t, dir, "switch", "--orphan", "imported")
	testutil.WriteFile(t, dir, "imported.txt", "imported\n")
	safegitCommitEnv(t, dir, pullSession, "the imported root", "imported.txt")
	testutil.Git(t, dir, "push", "origin", "imported:main")
	testutil.Git(t, dir, "switch", "main")
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession, "pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.General {
		t.Fatalf("safegit pull exited %d, want %d (General)\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "merge-continue") {
		t.Errorf("the pull's refusal does not name the import route:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pull moved HEAD to %s (was %s)", head, tip)
	}
}

// TestAnUnbornMergeIsNotRefusedForWantOfAMergeBase is the seam the predicate
// exists for. An unborn branch has no HEAD to take a merge base FROM, and a
// merge into one is a plain fast-forward that must keep working: a predicate
// written as "merge-base failed" rather than "HEAD resolves and merge-base
// reports no base" would refuse every one of them.
func TestAnUnbornMergeIsNotRefusedForWantOfAMergeBase(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "--orphan", "fresh")

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code != exitcode.OK {
		t.Fatalf("a merge into an unborn branch exited %d, want 0\nstdout=%s\nstderr=%s",
			code, stdout, stderr)
	}
	if testutil.Rev(t, dir, "HEAD") != testutil.Rev(t, dir, "feature") {
		t.Errorf("the merge did not fast-forward the unborn branch onto feature")
	}
}

// TestTheImportRouteTheRefusalNamesWorks: the way out is documented in the
// refusal, so it has to be real. Raw git computes the merge with --no-commit,
// and safegit's own conclusion commits it -- pipeline-authored, two parents.
func TestTheImportRouteTheRefusalNamesWorks(t *testing.T) {
	dir := newUnrelatedHistoriesRepo(t)
	main := testutil.Rev(t, dir, "HEAD")
	other := testutil.Rev(t, dir, "other")

	testutil.Git(t, dir, "merge", "--no-commit", "--allow-unrelated-histories", "other")
	if _, stderr, code := runSafegit(t, dir, "merge-continue"); code != 0 {
		t.Fatalf("merge-continue could not conclude the imported merge (code %d): %s", code, stderr)
	}

	parents := testutil.Parents(t, dir, "HEAD")
	if len(parents) != 2 || parents[0] != main || parents[1] != other {
		t.Errorf("the concluded import records parents %v, want [%s %s]", parents, main, other)
	}
	assertNoSequencerResidue(t, dir, "concluded import")
}
