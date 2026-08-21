package test

import (
	"os"
	"path/filepath"
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

// mergeFixture is a repository parked in a conflicted merge whose conflict has
// been resolved in the working tree but not concluded.
type mergeFixture struct {
	dir string
	// mainSHA is the pre-merge tip of main: the first parent a correct merge
	// commit must carry.
	mainSHA string
	// featureSHA is what MERGE_HEAD names: the second parent a correct merge
	// commit must carry, and the one safegit silently drops today.
	featureSHA string
}

// newConflictedMergeRepo builds a repo whose main and feature branches both
// edited conflicted.txt, merges feature into main (conflict), and resolves the
// conflict in the working tree without staging it.
//
// feature also adds feature-only.txt, which the merge staged cleanly. It is the
// canary for whole-index conclusion: any conclusion route that rebuilds the tree
// from HEAD instead of from the merge's index drops that file on the floor.
func newConflictedMergeRepo(t *testing.T) mergeFixture {
	t.Helper()
	dir := newRepo(t)

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Base revision of the conflicted file, on main.
	write("conflicted.txt", "line1\nbase\nline3\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "base", "--", "conflicted.txt"); code != 0 {
		t.Fatalf("base commit failed (code %d): %s", code, stderr)
	}
	baseSHA := testutil.Git(t, dir, "rev-parse", "HEAD")

	// feature: conflicting edit plus one clean addition.
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	write("conflicted.txt", "line1\nfeature\nline3\n")
	write("feature-only.txt", "only on feature\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "feature edit", "--", "conflicted.txt", "feature-only.txt"); code != 0 {
		t.Fatalf("feature commit failed (code %d): %s", code, stderr)
	}
	featureSHA := testutil.Git(t, dir, "rev-parse", "HEAD")

	// main: the conflicting edit.
	testutil.Git(t, dir, "switch", "main")
	if head := testutil.Git(t, dir, "rev-parse", "HEAD"); head != baseSHA {
		t.Fatalf("main tip = %s, want %s after switching back", head, baseSHA)
	}
	write("conflicted.txt", "line1\nmain\nline3\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "main edit", "--", "conflicted.txt"); code != 0 {
		t.Fatalf("main commit failed (code %d): %s", code, stderr)
	}
	mainSHA := testutil.Git(t, dir, "rev-parse", "HEAD")

	// The merge itself, through safegit. It exits 1 and relays git's own
	// "fix conflicts and then commit the result" -- advice no safegit command
	// can act on, which is what this suite is about.
	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code == 0 {
		t.Fatalf("safegit merge feature succeeded; the fixture needs a conflict.\nstdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("safegit merge feature did not report a conflict (code %d)\nstdout=%s stderr=%s", code, stdout, stderr)
	}

	assertMerging(t, dir, featureSHA)

	// Resolve the conflict in the working tree, leaving the index unmerged --
	// exactly the state an agent reaches after editing every conflicted file.
	write("conflicted.txt", "line1\nresolved\nline3\n")

	return mergeFixture{dir: dir, mainSHA: mainSHA, featureSHA: featureSHA}
}

// assertMerging fails unless the repo is mid-merge with MERGE_HEAD naming want.
func assertMerging(t *testing.T, dir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil {
		t.Fatalf("reading .git/MERGE_HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("MERGE_HEAD = %s, want %s", got, want)
	}
}

// mergeStateGone reports whether git considers the merge concluded.
func mergeStateGone(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return os.IsNotExist(err)
}

// parentsOf returns the parent SHAs of a commit, in order.
func parentsOf(t *testing.T, dir, ref string) []string {
	t.Helper()
	out := testutil.Git(t, dir, "rev-list", "--parents", "-n", "1", ref)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		t.Fatalf("rev-list --parents produced nothing for %s", ref)
	}
	return fields[1:]
}

// treePaths returns every path in a commit's tree.
func treePaths(t *testing.T, dir, ref string) []string {
	t.Helper()
	out := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", ref)
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
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
		fx := newConflictedMergeRepo(t)

		stdout, stderr, code := runSafegit(t, fx.dir, route.args...)
		if code != 0 {
			failures = append(failures, route.name+": refused with exit "+
				strings.TrimSpace(itoa(code))+": "+oneLine(stderr))
			continue
		}

		var problems []string
		head := testutil.Git(t, fx.dir, "rev-parse", "HEAD")
		parents := parentsOf(t, fx.dir, head)
		if len(parents) != 2 {
			problems = append(problems, "commit has "+itoa(len(parents))+" parent(s), want 2 ("+strings.Join(parents, ", ")+")")
		} else {
			if parents[0] != fx.mainSHA {
				problems = append(problems, "first parent = "+parents[0]+", want "+fx.mainSHA)
			}
			if parents[1] != fx.featureSHA {
				problems = append(problems, "second parent = "+parents[1]+", want "+fx.featureSHA)
			}
		}

		paths := treePaths(t, fx.dir, head)
		if !contains(paths, "feature-only.txt") {
			problems = append(problems, "tree lost feature-only.txt (has: "+strings.Join(paths, ", ")+")")
		}
		if blob := testutil.Git(t, fx.dir, "show", head+":conflicted.txt"); !strings.Contains(blob, "resolved") {
			problems = append(problems, "conflicted.txt does not carry the resolution: "+oneLine(blob))
		}
		if !mergeStateGone(t, fx.dir) {
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
	fx := newConflictedMergeRepo(t)

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
		parents := parentsOf(t, fx.dir, head)
		paths := treePaths(t, fx.dir, head)
		t.Fatalf("safegit commit with a pathspec succeeded mid-merge (git refuses the same command).\n"+
			"  new commit: %s\n"+
			"  parents: %v (want a refusal, or 2 parents: %s, %s)\n"+
			"  tree: %v (feature-only.txt present: %t)\n"+
			"  MERGE_HEAD still present: %t\n"+
			"  stdout: %s",
			head, parents, fx.mainSHA, fx.featureSHA,
			paths, contains(paths, "feature-only.txt"),
			!mergeStateGone(t, fx.dir), oneLine(stdout))
	}

	if head := testutil.Git(t, fx.dir, "rev-parse", "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
	}
	assertMerging(t, fx.dir, fx.featureSHA)

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
	fx := newConflictedMergeRepo(t)
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
			head, parentsOf(t, fx.dir, head), treePaths(t, fx.dir, head),
			oneLine(testutil.Git(t, fx.dir, "show", head+":conflicted.txt")),
			!mergeStateGone(t, fx.dir), oneLine(stdout))
	}

	if head := testutil.Git(t, fx.dir, "rev-parse", "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
	}
	assertMerging(t, fx.dir, fx.featureSHA)
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
	fx := newConflictedMergeRepo(t)
	assertMerging(t, fx.dir, fx.featureSHA)

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

// itoa avoids pulling strconv in for two call sites' worth of formatting.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// oneLine collapses multi-line command output into a single readable line so
// failure messages stay scannable.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
