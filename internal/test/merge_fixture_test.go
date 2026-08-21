package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The conflicted-merge fixture three investigations share: merge conclusion
// (commit_merge_state_test.go), amend parity (amend_parity_test.go) and undo's
// index sync (undo_index_sync_test.go) all need a repository parked mid-merge,
// and each used to build its own. It lives in package test rather than
// internal/testutil because every commit in it goes through the safegit binary,
// which only this package has.

// conflictedMergeFixture is a repository parked in a conflicted merge.
type conflictedMergeFixture struct {
	dir string
	// mainSHA is the pre-merge tip of main: the first parent a correct merge
	// commit must carry.
	mainSHA string
	// featureSHA is what MERGE_HEAD names: the second parent a correct merge
	// commit must carry, and the one safegit silently drops today.
	featureSHA string
}

// conflictedMergeOpts selects the variations the three investigations need.
type conflictedMergeOpts struct {
	// env is the environment for the safegit invocations the fixture makes.
	// nil means the plain controlled test environment, which carries no
	// session handshake.
	env []string
	// cleanSideFile adds feature-only.txt on the feature branch, a path the
	// merge stages cleanly. It is the marker for whole-index conclusion: any
	// conclusion route that rebuilds the tree from HEAD instead of from the
	// merge's index drops that file on the floor.
	cleanSideFile bool
	// resolveInTree rewrites conflicted.txt with a resolution, leaving the
	// index unmerged -- the state an agent reaches after editing every
	// conflicted file. Without it the conflict markers stay on disk.
	resolveInTree bool
}

// newConflictedMergeRepo builds a repo whose main and feature branches both
// edited conflicted.txt and merges feature into main, which conflicts.
func newConflictedMergeRepo(t *testing.T, opts conflictedMergeOpts) conflictedMergeFixture {
	t.Helper()
	dir := newRepo(t)

	commit := func(msg string, paths ...string) string {
		t.Helper()
		args := append([]string{"commit", "-m", msg, "--"}, paths...)
		if _, stderr, code := runSafegitEnv(t, dir, opts.env, args...); code != 0 {
			t.Fatalf("fixture commit %q failed (code %d): %s", msg, code, stderr)
		}
		return testutil.Rev(t, dir, "HEAD")
	}

	// Base revision of the conflicted file, on main.
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nbase\nline3\n")
	baseSHA := commit("base", "conflicted.txt")

	// feature: the conflicting edit, plus optionally one clean addition.
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nfeature\nline3\n")
	featurePaths := []string{"conflicted.txt"}
	if opts.cleanSideFile {
		testutil.WriteFile(t, dir, "feature-only.txt", "only on feature\n")
		featurePaths = append(featurePaths, "feature-only.txt")
	}
	featureSHA := commit("feature edit", featurePaths...)

	// main: the conflicting edit.
	testutil.Git(t, dir, "switch", "main")
	if head := testutil.Rev(t, dir, "HEAD"); head != baseSHA {
		t.Fatalf("main tip = %s, want %s after switching back", head, baseSHA)
	}
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nmain\nline3\n")
	mainSHA := commit("main edit", "conflicted.txt")

	// The merge itself, through safegit. It exits nonzero and relays git's own
	// "fix conflicts and then commit the result" -- advice no safegit command
	// can act on, which is what these investigations are about.
	stdout, stderr, code := runSafegitEnv(t, dir, opts.env, "merge", "feature")
	if code == 0 {
		t.Fatalf("safegit merge feature succeeded; the fixture needs a conflict.\nstdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("safegit merge feature did not report a conflict (code %d)\nstdout=%s stderr=%s", code, stdout, stderr)
	}
	testutil.AssertMergeHead(t, dir, featureSHA, "the fixture must be parked mid-merge")

	if opts.resolveInTree {
		testutil.WriteFile(t, dir, "conflicted.txt", "line1\nresolved\nline3\n")
	}

	return conflictedMergeFixture{dir: dir, mainSHA: mainSHA, featureSHA: featureSHA}
}
