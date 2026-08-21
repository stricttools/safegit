package test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A conflicted merge is the one repository state safegit cannot conclude.
//
// git's own model for finishing a merge is whole-index: MERGE_HEAD names the
// second parent, the index carries the merge's staged result, and `git commit`
// with no pathspec turns both into a two-parent commit. safegit's commit
// contract is the opposite -- pathspec-only, tmp index seeded from the parent
// commit's tree (internal/commit/commit.go:210), single parent handed to
// commit-tree (internal/commit/commit.go:271). Nothing in the pipeline reads
// MERGE_HEAD.
//
// The tests below are RED on purpose. They assert the behavior safegit must
// have once a merge-conclusion path exists, and every one of them fails today.
// Inverting them into a green suite means changing nothing in the assertions:
// they describe the end state, not the current one. The only edit a fix
// requires is registering whatever verb concludes the merge in
// mergeConclusionRoutes below.

// mergeConclusionRoute is one candidate command line for concluding a merge.
// The suite tries each and asserts that at least one produces a real merge
// commit. Adding a `merge-continue` verb (or teaching `commit` the merge case)
// means adding its argv here.
type mergeConclusionRoute struct {
	name string
	args []string
}

func mergeConclusionRoutes() []mergeConclusionRoute {
	return []mergeConclusionRoute{
		{"commit-no-pathspec", []string{"commit", "-m", "Merge branch 'feature'"}},
		{"commit-with-pathspec", []string{"commit", "-m", "Merge branch 'feature'", "--", "conflicted.txt"}},
	}
}

// A conflicted merge with its conflicts resolved must be concludable by some
// safegit command, producing a genuine two-parent merge commit that carries the
// merge's whole staged result.
//
// RED today: neither route works. `commit` with no pathspec is refused outright
// (commit.go:52, "no files specified", exit 2), and `commit` with a pathspec
// exits 0 while producing a single-parent commit -- see the corruption test
// below. There is no third route: `safegit --help` lists no merge-conclusion
// verb, and git's own `git commit` is unavailable to an agent under the
// git-add/git-commit blocking hooks.
func TestMergeCanBeConcludedThroughSafegit(t *testing.T) {
	var failures []string

	for _, route := range mergeConclusionRoutes() {
		fx := newConflictedMergeRepo(t, conflictedMergeOpts{cleanSideFile: true, resolveInTree: true})

		stdout, stderr, code := runSafegit(t, fx.dir, route.args...)
		if code != 0 {
			failures = append(failures, route.name+": refused with exit "+
				strconv.Itoa(code)+": "+oneLine(stderr))
			continue
		}

		var problems []string
		head := testutil.Git(t, fx.dir, "rev-parse", "HEAD")
		parents := testutil.Parents(t, fx.dir, head)
		if len(parents) != 2 {
			problems = append(problems, "commit has "+strconv.Itoa(len(parents))+" parent(s), want 2 ("+strings.Join(parents, ", ")+")")
		} else {
			if parents[0] != fx.mainSHA {
				problems = append(problems, "first parent = "+parents[0]+", want "+fx.mainSHA)
			}
			if parents[1] != fx.featureSHA {
				problems = append(problems, "second parent = "+parents[1]+", want "+fx.featureSHA)
			}
		}

		paths := testutil.TreePaths(t, fx.dir, head)
		if !testutil.Contains(paths, "feature-only.txt") {
			problems = append(problems, "tree lost feature-only.txt (has: "+strings.Join(paths, ", ")+")")
		}
		if blob := testutil.Git(t, fx.dir, "show", head+":conflicted.txt"); !strings.Contains(blob, "resolved") {
			problems = append(problems, "conflicted.txt does not carry the resolution: "+oneLine(blob))
		}
		if !testutil.MergeStateGone(t, fx.dir) {
			problems = append(problems, ".git/MERGE_HEAD survives; git still considers the repo mid-merge")
		}

		if len(problems) == 0 {
			return // A route concluded the merge correctly. Test passes.
		}
		failures = append(failures, route.name+": exit 0 but "+strings.Join(problems, "; ")+" (stdout: "+oneLine(stdout)+")")
	}

	t.Fatalf("no safegit command concludes a conflicted merge:\n  %s", strings.Join(failures, "\n  "))
}

// Concluding a merge is whole-index by construction, so `safegit commit` with a
// pathspec cannot express it. git refuses the equivalent outright:
//
//	$ git commit -m msg -- conflicted.txt
//	fatal: cannot do a partial commit during a merge.
//
// safegit must refuse too. It does not: the pipeline never consults MERGE_HEAD,
// seeds its tmp index from the parent commit's tree
// (internal/commit/commit.go:210) and hands commit-tree exactly one parent
// (internal/commit/commit.go:271), so it exits 0 having built a commit that
// drops the second parent AND every path the merge staged but the pathspec did
// not name.
//
// RED today: the command succeeds and the assertions below fire.
func TestCommitWithPathspecRefusedDuringMerge(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{cleanSideFile: true, resolveInTree: true})

	// git's own refusal, recorded so the test documents the contract safegit
	// is expected to match rather than merely asserting a message.
	gitOut, gitCode := testutil.GitTry(t, fx.dir, "commit", "-m", "merge", "--", "conflicted.txt")
	if gitCode == 0 {
		t.Fatalf("git commit -- <path> mid-merge succeeded; expected a partial-commit refusal\n%s", gitOut)
	}
	if !strings.Contains(gitOut, "partial commit") {
		t.Fatalf("git refused for an unexpected reason (code %d): %s", gitCode, oneLine(gitOut))
	}

	before := testutil.Git(t, fx.dir, "rev-parse", "HEAD")

	stdout, stderr, code := runSafegit(t, fx.dir, "commit", "-m", "merge", "--", "conflicted.txt")

	if code == 0 {
		head := testutil.Git(t, fx.dir, "rev-parse", "HEAD")
		parents := testutil.Parents(t, fx.dir, head)
		paths := testutil.TreePaths(t, fx.dir, head)
		t.Fatalf("safegit commit with a pathspec succeeded mid-merge (git refuses the same command).\n"+
			"  new commit: %s\n"+
			"  parents: %v (want a refusal, or 2 parents: %s, %s)\n"+
			"  tree: %v (feature-only.txt present: %t)\n"+
			"  MERGE_HEAD still present: %t\n"+
			"  stdout: %s",
			head, parents, fx.mainSHA, fx.featureSHA,
			paths, testutil.Contains(paths, "feature-only.txt"),
			!testutil.MergeStateGone(t, fx.dir), oneLine(stdout))
	}

	if head := testutil.Git(t, fx.dir, "rev-parse", "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
	}
	testutil.AssertMergeHead(t, fx.dir, fx.featureSHA, "the repository must still be mid-merge")

	// The refusal has to be actionable. Mid-merge is the one context where
	// commit.go:52's advice ("use -- file1 file2 ...") is impossible to follow,
	// so the message must name the merge and point at whatever concludes it.
	if !strings.Contains(strings.ToLower(stderr), "merge") {
		t.Errorf("refusal does not mention the merge: %s", oneLine(stderr))
	}
}

// --allow-empty is the third route into the pipeline and the most destructive:
// commit.go:51 waives the pathspec requirement when it is set, so
// `safegit commit -m msg --allow-empty` runs mid-merge with zero files, writes
// the parent's own tree back out, and discards the entire merge result while
// leaving MERGE_HEAD in place.
//
// RED today: the command succeeds.
func TestCommitAllowEmptyRefusedDuringMerge(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{cleanSideFile: true, resolveInTree: true})
	before := testutil.Git(t, fx.dir, "rev-parse", "HEAD")

	stdout, stderr, code := runSafegit(t, fx.dir, "commit", "-m", "merge", "--allow-empty")

	if code == 0 {
		head := testutil.Git(t, fx.dir, "rev-parse", "HEAD")
		t.Fatalf("safegit commit --allow-empty succeeded mid-merge, discarding the merge.\n"+
			"  new commit: %s\n"+
			"  parents: %v (want a refusal)\n"+
			"  tree: %v\n"+
			"  conflicted.txt content: %s\n"+
			"  MERGE_HEAD still present: %t\n"+
			"  stdout: %s",
			head, testutil.Parents(t, fx.dir, head), testutil.TreePaths(t, fx.dir, head),
			oneLine(testutil.Git(t, fx.dir, "show", head+":conflicted.txt")),
			!testutil.MergeStateGone(t, fx.dir), oneLine(stdout))
	}

	if head := testutil.Git(t, fx.dir, "rev-parse", "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
	}
	testutil.AssertMergeHead(t, fx.dir, fx.featureSHA, "the repository must still be mid-merge")
	if !strings.Contains(strings.ToLower(stderr), "merge") {
		t.Errorf("refusal does not mention the merge: %s", oneLine(stderr))
	}
}

// `safegit merge` relays git's "fix conflicts and then commit the result" and
// exits 1. That instruction has no safegit-conformant execution, so safegit
// owes the operator its own next step on the conflict path.
//
// RED today: safegit adds nothing to git's streamed output (coord_cmd.go:186
// returns 1 the moment git fails, before any safegit-authored message).
func TestMergeConflictTellsOperatorHowToConclude(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{cleanSideFile: true, resolveInTree: true})
	testutil.AssertMergeHead(t, fx.dir, fx.featureSHA, "the repository must still be mid-merge")

	// Re-run the merge path in a fresh repo to capture the conflict output
	// without the fixture's own assertions consuming it.
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "base", "--", "c.txt"); code != 0 {
		t.Fatalf("base commit failed (code %d): %s", code, stderr)
	}
	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "side")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("side\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "side", "--", "c.txt"); code != 0 {
		t.Fatalf("side commit failed (code %d): %s", code, stderr)
	}
	testutil.Git(t, dir, "switch", "main")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("trunk\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "trunk", "--", "c.txt"); code != 0 {
		t.Fatalf("trunk commit failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "merge", "side")
	if code == 0 {
		t.Fatalf("expected a conflict; safegit merge succeeded\nstdout=%s stderr=%s", stdout, stderr)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "safegit") {
		t.Errorf("merge conflict output carries no safegit-authored next step, only git's "+
			"\"commit the result\" advice that no safegit command can follow:\n%s", combined)
	}
}

// oneLine collapses multi-line command output into a single readable line so
// failure messages stay scannable.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
