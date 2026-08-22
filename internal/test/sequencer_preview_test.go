package test

import (
	"strings"
	"testing"

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

// TestPreviewRefusesWhatItCannotCompute: the criterion in action. An option
// that changes how the TREE is computed and that safegit's merge-tree
// invocation does not carry is refused with its reason, rather than previewed
// under rules the real run would not use.
func TestPreviewRefusesWhatItCannotCompute(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a non-ort strategy",
			args: []string{"--dry-run", "merge", "-s", "resolve", "side"},
			want: "merge strategy other than ort",
		},
		{
			name: "a strategy option",
			args: []string{"--dry-run", "merge", "-X", "ours", "side"},
			want: "changes how the merge resolves",
		},
		{
			name: "an attached strategy option",
			args: []string{"--dry-run", "cherry-pick", "-Xtheirs", "side"},
			want: "changes how the merge resolves",
		},
		{
			name: "a squashed merge",
			args: []string{"--dry-run", "merge", "--squash", "side"},
			want: "stages the merge's result",
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
		{"--dry-run", "cherry-pick", fx.conflicting, fx.clean},
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

// TestPreviewOfAQueueStopsWhereGitWouldStop: a multi-commit cherry-pick is
// replayed step by step, each on top of the previous result, so the preview
// names the commit the operation would actually stop on rather than reporting
// only the first step.
func TestPreviewOfAQueueStopsWhereGitWouldStop(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "base\n")
	testutil.WriteFile(t, dir, "b.txt", "base\n")
	safegitCommit(t, dir, "base", "a.txt", "b.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "b.txt", "main\n")
	safegitCommit(t, dir, "main edits b", "b.txt")

	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "a.txt", "side a\n")
	first := safegitCommit(t, dir, "side edits a", "a.txt")
	testutil.WriteFile(t, dir, "b.txt", "side b\n")
	second := safegitCommit(t, dir, "side edits b", "b.txt")
	testutil.Git(t, dir, "switch", "main")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "cherry-pick", first, second)
	if code != 0 {
		t.Fatalf("the queue preview failed (code %d): %s", code, stderr)
	}
	// The first command applies cleanly; the second conflicts on b.txt.
	if !strings.Contains(stdout, "CONFLICT") || !strings.Contains(stdout, "b.txt") {
		t.Errorf("the preview does not report where the queue would stop:\n%s", stdout)
	}
	if strings.Contains(stdout, "a.txt") {
		t.Errorf("the preview reports a conflict on a path that applies cleanly:\n%s", stdout)
	}
	if !strings.Contains(stdout, "side edits b") {
		t.Errorf("the preview does not name the commit the queue would stop on:\n%s", stdout)
	}

	// And the real run agrees.
	if _, stderr, code := runSafegit(t, dir, "cherry-pick", first, second); code == 0 {
		t.Fatalf("the real queue did not stop where the preview said it would: %s", stderr)
	}
	if !strings.Contains(testutil.Git(t, dir, "diff", "--name-only", "--diff-filter=U"), "b.txt") {
		t.Error("the real run stopped on a different path than the preview predicted")
	}
}
