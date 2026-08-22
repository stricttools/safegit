package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Every route into the commit pipeline that reads git's IN-FLIGHT OPERATION
// STATE, exercised from a repository SUBDIRECTORY.
//
// The pipeline resolves the git directory on the repository-root-pinned context,
// so git answers `.git` -- a path relative to the directory git ran in, which is
// the repository root. Everything the pipeline then does with that answer is a
// Go filesystem call, and Go resolves a relative path against the PROCESS
// working directory. From a subdirectory the two disagree, and the disagreement
// is silent in both directions:
//
//   - the mid-operation refusal reads <subdir>/.git/MERGE_HEAD, finds nothing,
//     and lets a commit through that git considers mid-merge -- a single-parent
//     commit that drops MERGE_HEAD on the floor;
//   - the shared-index base reads <subdir>/.git/index, finds nothing, and an
//     absent index file is git's EMPTY index, so a conclusion commits a tree
//     holding only the paths its resolutions named.
//
// The two are one defect with one fix (an absolute git-directory resolution),
// which is why they are pinned in one file. Each test asserts the whole outcome
// rather than the symptom that is easiest to reach: the merge conclusion checks
// the full committed tree, because a guard fixed without the index base would
// pass a parent check and still truncate the commit.

// subdirOf creates and returns a subdirectory of a repository, to invoke safegit
// from.
func subdirOf(t *testing.T, dir string) string {
	t.Helper()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return sub
}

// TestMergeConclusionFromASubdirectory: `safegit merge-continue` run from a
// subdirectory produces the same commit it produces from the root -- both
// parents, and the merge's WHOLE staged result rather than the resolved path
// alone.
func TestMergeConclusionFromASubdirectory(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	sub := subdirOf(t, fx.dir)

	stdout, stderr, code := runSafegitEnv(t, sub, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs")
	if code != 0 {
		t.Fatalf("merge-continue from a subdirectory: exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	head := testutil.Rev(t, fx.dir, "HEAD")
	parents := testutil.Parents(t, fx.dir, head)
	want := []string{fx.mainSHA, fx.featureSHA}
	if len(parents) != len(want) {
		t.Fatalf("the conclusion has %d parent(s), want %d: %v", len(parents), len(want), parents)
	}
	for i := range want {
		if parents[i] != want[i] {
			t.Errorf("parent %d = %s, want %s", i, parents[i], want[i])
		}
	}

	// The whole tree, not just the resolved path: a conclusion seeded from an
	// EMPTY index would carry conflicted.txt alone and nothing else.
	paths := testutil.TreePaths(t, fx.dir, head)
	for _, name := range []string{"seed.txt", "conflicted.txt", "feature-only.txt"} {
		if !testutil.Contains(paths, name) {
			t.Errorf("the conclusion dropped %s (tree: %v)", name, paths)
		}
	}
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "conflicted.txt"); !strings.Contains(blob, "feature") {
		t.Errorf("theirs on a merge is the merged-in side; got %q", blob)
	}
	assertNoSequencerResidue(t, fx.dir, "merge conclusion from a subdirectory")
}

// TestMidMergeCommitFromASubdirectoryIsRefused is the corrupt-commit case: an
// ordinary `safegit commit` taken while git is mid-merge must be refused
// wherever it is run from. Letting it through produces a single-parent commit
// that silently discards MERGE_HEAD and every path the pathspec does not name.
func TestMidMergeCommitFromASubdirectoryIsRefused(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession})
	sub := subdirOf(t, fx.dir)
	if err := os.WriteFile(filepath.Join(sub, "unrelated.txt"), []byte("unrelated work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := testutil.Rev(t, fx.dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, sub, conclusionSession,
		"commit", "-m", "an unrelated file", "--", "unrelated.txt")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("mid-merge commit from a subdirectory: exit %d, want %d\nstdout: %s\nstderr: %s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "merge") {
		t.Errorf("the refusal should name the merge in progress; stderr: %s", stderr)
	}
	if after := testutil.Rev(t, fx.dir, "HEAD"); after != before {
		t.Errorf("the branch moved despite the refusal: %s -> %s", before, after)
	}
	testutil.AssertMergeHead(t, fx.dir, fx.featureSHA, "the merge must still be in flight after the refusal")
}

// TestSingleRevertFromASubdirectory: the restructured single-commit revert
// declares itself the conclusion of the revert IT started, so it depends on the
// same in-flight resolution. Run from a subdirectory it must complete end to
// end, leaving no state behind for the next commit to trip over.
func TestSingleRevertFromASubdirectory(t *testing.T) {
	dir := newRepo(t)
	sub := subdirOf(t, dir)

	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommitEnv(t, dir, conclusionSession, "first", "a.txt")
	testutil.WriteFile(t, dir, "a.txt", "two\n")
	target := safegitCommitEnv(t, dir, conclusionSession, "second", "a.txt")

	stdout, stderr, code := runSafegitEnv(t, sub, conclusionSession, "revert", "--no-edit", target)
	if code != 0 {
		t.Fatalf("revert from a subdirectory: exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if blob := testutil.MustShow(t, dir, "HEAD", "a.txt"); blob != "one\n" {
		t.Errorf("the revert did not undo the second commit: a.txt = %q", blob)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "one\n" {
		t.Errorf("the working tree was not put back: %q (err %v)", got, err)
	}
	assertNoSequencerResidue(t, dir, "single revert from a subdirectory")
}
