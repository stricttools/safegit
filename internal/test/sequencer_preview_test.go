package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Honest previews for merge, cherry-pick and revert.
//
// `--dry-run` on these three recorded the invocation and said nothing about its
// outcome, which left the only question an operator asks -- "will this
// conflict, and where?" -- unanswered. It is now computed with `git merge-tree
// --write-tree`, git's own merge engine, run under the preview object
// quarantine because merge-tree writes real objects.
//
// The tests below assert the computed answer in both directions, the
// criterion-based refusal for command lines whose tree computation the preview
// cannot reproduce, and the property that makes the whole thing a preview at
// all: the object store is byte-identical afterwards.

// previewFixture is a repository with a side branch carrying one commit that
// conflicts with main and one that applies cleanly.
type previewFixture struct {
	dir string
	// conflicting is the side commit that touches the same line main touched.
	conflicting string
	// clean is the side commit that adds a file main has never seen.
	clean string
}

func newPreviewRepo(t *testing.T) previewFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommit(t, dir, "base", "c.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
	safegitCommit(t, dir, "main change", "c.txt")

	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "c.txt", "l1\nside\nl3\n")
	conflicting := safegitCommit(t, dir, "side change", "c.txt")
	testutil.WriteFile(t, dir, "added.txt", "added\n")
	clean := safegitCommit(t, dir, "a clean addition", "added.txt")
	testutil.Git(t, dir, "switch", "main")

	return previewFixture{dir: dir, conflicting: conflicting, clean: clean}
}

// TestPreviewReportsACleanOutcome: an operation that would apply cleanly says
// so, for each of the three verbs.
func TestPreviewReportsACleanOutcome(t *testing.T) {
	fx := newPreviewRepo(t)

	cases := []struct {
		name string
		args []string
		verb string
	}{
		{"cherry-pick", []string{"--dry-run", "cherry-pick", fx.clean}, "cherry-pick"},
		{"revert", []string{"--dry-run", "revert", "--no-edit", "HEAD"}, "revert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, fx.dir, tc.args...)
			if code != 0 {
				t.Fatalf("%v failed (code %d): %s", tc.args, code, stderr)
			}
			if !strings.Contains(stdout, "would "+tc.verb) || !strings.Contains(stdout, "cleanly") {
				t.Errorf("the preview does not report a clean outcome:\n%s", stdout)
			}
			if !strings.Contains(stdout, "resulting tree:") {
				t.Errorf("the preview does not report the tree it computed:\n%s", stdout)
			}
		})
	}

	// A merge of a branch that is strictly ahead is a FAST-FORWARD, not a
	// merge, and the preview says which -- the two have different outcomes and
	// reporting a fast-forward as a clean merge would be wrong.
	t.Run("merge fast-forward", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "f.txt", "one\n")
		safegitCommit(t, dir, "one", "f.txt")
		testutil.Git(t, dir, "branch", "ahead")
		testutil.Git(t, dir, "switch", "ahead")
		testutil.WriteFile(t, dir, "f.txt", "two\n")
		safegitCommit(t, dir, "two", "f.txt")
		testutil.Git(t, dir, "switch", "main")

		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "merge", "ahead")
		if code != 0 {
			t.Fatalf("merge --dry-run failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "fast-forward") {
			t.Errorf("the preview does not report a fast-forward:\n%s", stdout)
		}
	})
}

// TestPreviewReportsAConflictWithItsPaths: the answer that costs an operator
// the most to be wrong about, for each verb, with the paths named.
func TestPreviewReportsAConflictWithItsPaths(t *testing.T) {
	fx := newPreviewRepo(t)

	cases := []struct {
		name string
		args []string
	}{
		{"merge", []string{"--dry-run", "merge", "side"}},
		{"cherry-pick", []string{"--dry-run", "cherry-pick", fx.conflicting}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, fx.dir, tc.args...)
			if code != 0 {
				t.Fatalf("%v failed (code %d): %s", tc.args, code, stderr)
			}
			if !strings.Contains(stdout, "CONFLICT") {
				t.Errorf("the preview does not report the conflict:\n%s", stdout)
			}
			if !strings.Contains(stdout, "c.txt") {
				t.Errorf("the preview does not name the conflicted path:\n%s", stdout)
			}
		})
	}

	// A revert that conflicts: an earlier commit's change has been edited since.
	t.Run("revert", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "r.txt", "before\n")
		safegitCommit(t, dir, "before", "r.txt")
		testutil.WriteFile(t, dir, "r.txt", "the change\n")
		target := safegitCommit(t, dir, "the change", "r.txt")
		testutil.WriteFile(t, dir, "r.txt", "a later edit\n")
		safegitCommit(t, dir, "a later edit", "r.txt")

		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "revert", "--no-edit", target)
		if code != 0 {
			t.Fatalf("revert --dry-run failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "CONFLICT") || !strings.Contains(stdout, "r.txt") {
			t.Errorf("the preview does not report the conflicted revert:\n%s", stdout)
		}
	})
}

// TestPreviewOfAnFfOnlyMergeReportsGitsOwnRefusal: --ff-only is a REFUSAL of
// the whole merge when the branches have diverged, and git decides it before it
// merges anything. So does the preview.
//
// The conflicted case is the one that was wrong: the ff-only verdict was
// reached only for an unconflicted merge, so a diverged merge that would also
// conflict previewed as "CONFLICT ... the operation would stop here for you to
// resolve them" -- advice about resolving a conflict git never gets far enough
// to produce ("fatal: Not possible to fast-forward, aborting").
func TestPreviewOfAnFfOnlyMergeReportsGitsOwnRefusal(t *testing.T) {
	fx := newPreviewRepo(t)

	stdout, stderr, code := runSafegit(t, fx.dir, "--dry-run", "merge", "--ff-only", "side")
	if code != 0 {
		t.Fatalf("merge --ff-only --dry-run failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "REFUSED") || !strings.Contains(stdout, "not a fast-forward") {
		t.Errorf("the preview does not report git's outright refusal:\n%s", stdout)
	}
	if strings.Contains(stdout, "CONFLICT") || strings.Contains(stdout, "resolve them") {
		t.Errorf("the preview offers conflict resolution for a merge git refuses to start:\n%s", stdout)
	}

	// The recorded fact the verdict reproduces: git itself refuses this exact
	// command line, and says why, without mentioning any conflict.
	out, gitCode := testutil.GitTry(t, fx.dir, "merge", "--ff-only", "side")
	if gitCode == 0 {
		t.Fatalf("git accepted an --ff-only merge of diverged branches; the fixture no longer produces the case:\n%s", out)
	}
	if !strings.Contains(out, "Not possible to fast-forward") {
		t.Errorf("git refused for an unexpected reason, so the preview's wording needs re-checking:\n%s", out)
	}
}

// TestPreviewMatchesTheRealOperation is the assertion that makes the preview
// worth anything: the outcome it reports is the outcome the operation actually
// has, and the paths it names are the paths git actually leaves unmerged.
//
// It is also the check on the merge-tree argv, which is the one piece of this
// that cannot be reasoned out and had to be probed: a cherry-pick of C is
// `--merge-base=C^ HEAD C` and a revert of C is `--merge-base=C HEAD C^`. Get
// either backwards and the preview reports the mirror image of the truth --
// which this test would catch, because it compares against the real operation
// in the same repository.
func TestPreviewMatchesTheRealOperation(t *testing.T) {
	cases := []struct {
		name    string
		argv    func(fx previewFixture) []string
		conflct bool
	}{
		{"a conflicting cherry-pick", func(fx previewFixture) []string { return []string{"cherry-pick", fx.conflicting} }, true},
		{"a clean cherry-pick", func(fx previewFixture) []string { return []string{"cherry-pick", fx.clean} }, false},
		{"a conflicting merge", func(fx previewFixture) []string { return []string{"merge", "side"} }, true},
		{"a clean revert", func(fx previewFixture) []string { return []string{"revert", "--no-edit", "HEAD"} }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPreviewRepo(t)
			argv := tc.argv(fx)

			stdout, stderr, code := runSafegit(t, fx.dir, append([]string{"--dry-run"}, argv...)...)
			if code != 0 {
				t.Fatalf("the preview failed (code %d): %s", code, stderr)
			}
			predictedConflict := strings.Contains(stdout, "CONFLICT")

			// Now do it for real, in the same repository, and compare.
			_, realErr, realCode := runSafegit(t, fx.dir, argv...)
			realConflict := realCode != 0

			if predictedConflict != realConflict {
				t.Fatalf("the preview said conflict=%v, the real run said conflict=%v\n  preview: %s\n  real: %s",
					predictedConflict, realConflict, stdout, realErr)
			}
			if predictedConflict != tc.conflct {
				t.Fatalf("the fixture is wrong: expected conflict=%v, got %v", tc.conflct, predictedConflict)
			}
			if !realConflict {
				// The TREE is what discriminates the merge-base mapping. A
				// clean/conflicted verdict does not: replaying a revert with
				// the cherry-pick sides conflicts in exactly the fixtures where
				// the revert conflicts, and is clean where it is clean -- only
				// the tree it computes is the mirror image. Comparing the
				// preview's tree with the tree the real operation committed is
				// therefore the assertion that would fail if the two mappings
				// were ever swapped.
				predicted := previewTree(t, stdout)
				if predicted == "" {
					t.Fatalf("the preview reported no tree:\n%s", stdout)
				}
				if actual := strings.TrimSpace(testutil.Git(t, fx.dir, "rev-parse", "HEAD^{tree}")); actual != predicted {
					t.Errorf("the preview computed tree %s, the real run produced %s", predicted, actual)
				}
				return
			}
			// The paths too: every path git left unmerged must be named.
			unmerged := testutil.Git(t, fx.dir, "diff", "--name-only", "--diff-filter=U")
			for _, path := range strings.Split(strings.TrimSpace(unmerged), "\n") {
				if path = strings.TrimSpace(path); path == "" {
					continue
				}
				if !strings.Contains(stdout, path) {
					t.Errorf("the preview did not name %s, which the real run left unmerged:\n%s", path, stdout)
				}
			}
		})
	}
}

// datedEnv pins a spawned safegit's author and committer dates, so a fixture
// can put commits in a deliberate chronological order rather than the
// same-second ties a fast test otherwise produces.
func datedEnv(when string) []string {
	return []string{"GIT_AUTHOR_DATE=" + when, "GIT_COMMITTER_DATE=" + when}
}

// previewTree reads the tree object name a clean preview reported.
func previewTree(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if _, tree, found := strings.Cut(line, "resulting tree:"); found {
			return strings.TrimSpace(tree)
		}
	}
	return ""
}

// TestPreviewRefusesWhatItCannotCompute: the criterion in action. A command
// line whose outcome safegit's merge-tree computation cannot REPRODUCE is
// refused with its reason, rather than previewed under rules the real run would
// not use.
//
// The strategy-SELECTION and --squash rows this table used to carry moved to
// the commands' own subset refusals: safegit's merge, cherry-pick and revert do
// not implement those options at all, so the refusal an operator meets is the
// command's rather than the preview's, and it applies to the real run too (see
// TestMergeSubsetRefusalsApplyToAPreviewToo and
// TestPickAndRevertSubsetRefusalsApplyToAPreviewToo). Strategy OPTIONS left the
// table by the other door: they are honored, and the preview FORWARDS them to
// the merge it computes (see TestThePreviewForwardsStrategyOptions), so there
// is nothing to refuse -- except on a git below the 2.43 floor merge-tree's own
// `-X` carries, which is the one refusal of this family left.
//
// What is left to the criterion here is the command line that names no
// operation to compute at all: a state-control form acts on what git already
// has in flight, which is a different question from what this command line
// would do.
func TestPreviewRefusesWhatItCannotCompute(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a state-control form",
			args: []string{"--dry-run", "merge", "--abort"},
			want: "operates on an operation git already has in flight",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPreviewRepo(t)
			stdout, stderr, code := runSafegit(t, fx.dir, tc.args...)
			if code == 0 {
				t.Fatalf("%v must be refused, got exit 0:\n%s", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("the refusal does not state its reason (%q):\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "merge-tree") {
				t.Errorf("the refusal does not say what computes the preview:\n%s", stderr)
			}
			// A refusal previews nothing, so it must not also record the
			// invocation as something it would run. (The framework prints the
			// would-do HEADER whenever --dry-run was passed, with or without
			// records under it, so the assertion is on the record line.)
			if strings.Contains(stdout, "run: git") {
				t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
			}
		})
	}
}

// TestPreviewAcceptsOptionsThatChangeNoTree is the other half of the criterion:
// an option that only affects how the COMMIT is created is not refused, because
// the tree the preview computes is the same tree either way.
func TestPreviewAcceptsOptionsThatChangeNoTree(t *testing.T) {
	fx := newPreviewRepo(t)

	for _, args := range [][]string{
		{"--dry-run", "merge", "--no-ff", "side"},
		{"--dry-run", "merge", "-m", "my merge message", "side"},
		{"--dry-run", "cherry-pick", "--signoff", fx.clean},
		{"--dry-run", "revert", "--no-edit", "HEAD"},
	} {
		stdout, stderr, code := runSafegit(t, fx.dir, args...)
		if code != 0 {
			t.Errorf("%v was refused (code %d): %s", args, code, stderr)
			continue
		}
		if !strings.Contains(stdout, "would ") {
			t.Errorf("%v produced no preview:\n%s", args, stdout)
		}
	}
}

// TestPreviewLeavesObjectStoreUntouched: merge-tree writes a real tree, and for
// a conflicted merge real blobs, so the whole computation runs under the
// preview object quarantine. The promise a --dry-run makes is that the
// repository is byte-identical afterwards, and this asserts exactly that.
func TestPreviewLeavesObjectStoreUntouched(t *testing.T) {
	fx := newPreviewRepo(t)

	for _, args := range [][]string{
		{"--dry-run", "merge", "side"},
		{"--dry-run", "cherry-pick", fx.conflicting},
		{"--dry-run", "cherry-pick", fx.clean},
		{"--dry-run", "revert", "--no-edit", "HEAD"},
	} {
		dryPurityAssertUntouched(t, fx.dir, "safegit "+strings.Join(args, " "), func() {
			stdout, stderr, code := runSafegit(t, fx.dir, args...)
			if code != 0 {
				t.Fatalf("%v failed (code %d): stdout=%s stderr=%s", args, code, stdout, stderr)
			}
		})
	}
}

// TestMultiCommitAndRangePreviewsAreRefusedLikeTheRun: a preview of a command
// line safegit does not implement is not a preview of anything.
//
// `safegit cherry-pick` and `safegit revert` each take exactly one commit,
// named as a commit. Several commits are refused, and so is a range or any
// other revision-set spelling -- a range hands the operation to git's own
// sequencer even where it holds one commit, so refusing rev-set OPERATORS
// rather than counting argv tokens is what makes the boundary hold.
//
// These rows used to be the queue-preview suite: the preview replayed a
// multi-commit operation step by step and named the commit git would stop on.
// The restructure removed the command lines that could reach it, so what is
// pinned here now is the refusal, and that it arrives identically with and
// without --dry-run -- nothing is recorded in the would-do log for a command
// safegit just declined.
func TestMultiCommitAndRangePreviewsAreRefusedLikeTheRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv func(fx previewFixture) []string
		says string
	}{
		{"two picked commits", func(fx previewFixture) []string {
			return []string{"cherry-pick", fx.conflicting, fx.clean}
		}, "one commit"},
		{"a picked range", func(fx previewFixture) []string {
			return []string{"cherry-pick", "main..side"}
		}, "range"},
		{"two reverted commits", func(fx previewFixture) []string {
			return []string{"revert", "--no-edit", "HEAD", "HEAD~1"}
		}, "one commit"},
		{"a reverted range", func(fx previewFixture) []string {
			return []string{"revert", "--no-edit", "HEAD~2..HEAD"}
		}, "range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPreviewRepo(t)
			argv := tc.argv(fx)

			for _, prefix := range [][]string{{"--dry-run"}, nil} {
				full := append(append([]string{}, prefix...), argv...)
				stdout, stderr, code := runSafegit(t, fx.dir, full...)
				if code != exitcode.Usage {
					t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
						strings.Join(full, " "), code, exitcode.Usage, stdout, stderr)
				}
				if !strings.Contains(stderr, tc.says) {
					t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
				}
				if strings.Contains(stdout, "run: git") {
					t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
				}
			}
		})
	}
}
