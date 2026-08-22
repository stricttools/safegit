package conflict_test

import (
	"context"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/testutil"
)

func TestResolveDefaultsToGitsOwnMarkerSizeAndStyle(t *testing.T) {
	dir := conflictedMerge(t)
	testutil.Chdir(t, dir)

	got, err := conflict.Resolve(context.Background(), "", []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	attrs := got["f.txt"]
	if attrs.MarkerSize != conflict.DefaultMarkerSize {
		t.Errorf("MarkerSize = %d, want %d", attrs.MarkerSize, conflict.DefaultMarkerSize)
	}
	if attrs.Style != git.StyleMerge {
		t.Errorf("Style = %q, want %q", attrs.Style, git.StyleMerge)
	}
}

func TestResolveReadsThePerPathMarkerSize(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=12\n")

	got, err := conflict.Resolve(context.Background(), "", []string{"f.txt", "f.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if got["f.txt"].MarkerSize != 12 {
		t.Errorf("f.txt MarkerSize = %d, want 12", got["f.txt"].MarkerSize)
	}
	if got["f.bin"].MarkerSize != conflict.DefaultMarkerSize {
		t.Errorf("f.bin MarkerSize = %d, want the default %d", got["f.bin"].MarkerSize, conflict.DefaultMarkerSize)
	}
}

func TestResolveTreatsAnUnusableMarkerSizeTheWayGitDoes(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)
	// git ignores a marker size it cannot use and writes default-length
	// markers; the resolver has to agree, because its answer is measured
	// against the bytes git actually wrote.
	testutil.WriteFile(t, dir, ".gitattributes", "junk.txt conflict-marker-size=abc\nunset.txt -conflict-marker-size\nbare.txt conflict-marker-size\n")

	got, err := conflict.Resolve(context.Background(), "", []string{"junk.txt", "unset.txt", "bare.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"junk.txt", "unset.txt", "bare.txt"} {
		if got[p].MarkerSize != conflict.DefaultMarkerSize {
			t.Errorf("%s MarkerSize = %d, want the default %d", p, got[p].MarkerSize, conflict.DefaultMarkerSize)
		}
	}
}

// TestResolveReadsAttributesFromTheFirstParentTree is the case the whole
// --attr-source floor exists for: .gitattributes ITSELF is conflicted, so the
// working-tree copy is a marker-laden file that says nothing usable, and the
// answer has to come from a tree that necessarily predates the conflict.
func TestResolveReadsAttributesFromTheFirstParentTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=9\n")
	testutil.Git(t, dir, "add", "f.txt", ".gitattributes")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")

	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\n")
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=30\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "theirs")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\n")
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=11\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours")

	mustConflict(t, dir, "merge", "feature")

	// Sanity: the working-tree .gitattributes really is conflicted now.
	if !strings.Contains(worktreeFile(t, dir, ".gitattributes"), "<<<<<<<") {
		t.Fatal("the fixture did not leave .gitattributes conflicted")
	}

	// The first parent of the merge being concluded is HEAD.
	fromParent, err := conflict.Resolve(ctx, "HEAD", []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fromParent["f.txt"].MarkerSize; got != 11 {
		t.Errorf("resolved from the first parent's tree: MarkerSize = %d, want 11 (the committed value)", got)
	}

	// And reading the working tree instead gives the conflicted file's answer,
	// which is why the tree-sourced read is the one a conclusion uses.
	fromWorktree, err := conflict.Resolve(ctx, "", []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fromWorktree["f.txt"].MarkerSize; got == 11 {
		t.Errorf("the working-tree read returned the committed answer %d, so this test proves nothing", got)
	}
}

func TestStyleReadsTheConfiguredConflictStyle(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	got, err := conflict.Style(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != git.StyleMerge {
		t.Errorf("an unconfigured repository reported style %q, want %q", got, git.StyleMerge)
	}

	testutil.Git(t, dir, "config", "merge.conflictStyle", "diff3")
	got, err = conflict.Style(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != git.StyleDiff3 {
		t.Errorf("style = %q, want %q", got, git.StyleDiff3)
	}

	testutil.Git(t, dir, "config", "merge.conflictStyle", "nonsense")
	if _, err := conflict.Style(ctx); err == nil {
		t.Error("an unrecognized merge.conflictStyle must be an error, never a silent default")
	}
}

func TestStagesGroupsThreeSidesPerPath(t *testing.T) {
	dir := conflictedMerge(t)
	testutil.Chdir(t, dir)

	stages, err := conflict.Stages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	sides, ok := stages["f.txt"]
	if !ok {
		t.Fatal("f.txt is not reported as conflicted")
	}
	if sides.Base == nil || sides.Ours == nil || sides.Theirs == nil {
		t.Fatalf("a content conflict must carry all three stages: %+v", sides)
	}
	if !sides.ContentConflict() {
		t.Error("ContentConflict() = false for a conflict with both sides present")
	}
	if sides.Base.Stage != 1 || sides.Ours.Stage != 2 || sides.Theirs.Stage != 3 {
		t.Errorf("stages are mis-assigned: %+v", sides)
	}
}

func TestStagesReportsAnAbsentSideAsAbsent(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)

	// add/add: the path exists on both sides and in no common ancestor, so
	// there is no stage 1.
	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, "new.txt", "theirs\n")
	testutil.Git(t, dir, "add", "new.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "theirs adds")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "new.txt", "ours\n")
	testutil.Git(t, dir, "add", "new.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "ours adds")
	mustConflict(t, dir, "merge", "feature")

	stages, err := conflict.Stages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	sides := stages["new.txt"]
	if sides.Base != nil {
		t.Errorf("an add/add conflict must have no merge-base stage, got %+v", sides.Base)
	}
	if sides.Ours == nil || sides.Theirs == nil {
		t.Fatalf("both sides must be present in an add/add conflict: %+v", sides)
	}
	if !sides.ContentConflict() {
		t.Error("ContentConflict() = false although both sides are present")
	}
}

func TestStagesReportsADeleteModifyConflictAsOneSided(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)

	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.Git(t, dir, "rm", "-q", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "theirs deletes")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "ours changed\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours modifies")
	mustConflict(t, dir, "merge", "feature")

	stages, err := conflict.Stages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	sides := stages["f.txt"]
	if sides.Theirs != nil {
		t.Errorf("the deleting side must have no stage, got %+v", sides.Theirs)
	}
	if sides.Ours == nil || sides.Base == nil {
		t.Fatalf("the modifying side and the base must both be present: %+v", sides)
	}
	if sides.ContentConflict() {
		t.Error("ContentConflict() = true for a delete/modify conflict, which has no marked-up region at all")
	}
}

func TestAutoMergeHoldsWhatGitWroteIntoTheWorkingTree(t *testing.T) {
	dir := conflictedMerge(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	tree, present, err := conflict.AutoMergeTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("an ordinary conflicted merge must record an AUTO_MERGE tree")
	}
	if len(tree) != 40 {
		t.Errorf("AutoMergeTree returned %q, want a full tree object name", tree)
	}

	blob, ok, err := conflict.AutoMergeBlob(ctx, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("AUTO_MERGE does not carry the conflicted path")
	}
	if got, want := string(blob), worktreeFile(t, dir, "f.txt"); got != want {
		t.Errorf("AUTO_MERGE content differs from the working-tree file\n got: %q\nwant: %q", got, want)
	}
}

func TestAutoMergeIsAbsentWithNoOperationInFlight(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)

	_, present, err := conflict.AutoMergeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("a quiet repository reported an AUTO_MERGE tree")
	}
}

// TestReconstructIsByteIdenticalToAutoMerge is the fidelity property the marker
// verification is built on: given the stages and the attributes, git's own
// merge-file reproduces the recorded conflicted file exactly.
func TestReconstructIsByteIdenticalToAutoMerge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		style      string
		markerSize string
	}{
		{name: "default style"},
		{name: "diff3", style: "diff3"},
		{name: "zdiff3", style: "zdiff3"},
		{name: "custom marker size", markerSize: "12"},
		{name: "diff3 with a custom marker size", style: "diff3", markerSize: "20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			testutil.Chdir(t, dir)
			ctx := context.Background()

			if tc.style != "" {
				testutil.Git(t, dir, "config", "merge.conflictStyle", tc.style)
			}
			if tc.markerSize != "" {
				testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size="+tc.markerSize+"\n")
				testutil.Git(t, dir, "add", ".gitattributes")
				testutil.Git(t, dir, "commit", "-q", "-m", "attributes")
			}

			testutil.Git(t, dir, "switch", "-q", "-c", "feature")
			testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\nshared\n")
			testutil.Git(t, dir, "commit", "-q", "-am", "theirs")
			testutil.Git(t, dir, "switch", "-q", "main")
			testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\nshared\n")
			testutil.Git(t, dir, "commit", "-q", "-am", "ours")
			mustConflict(t, dir, "merge", "feature")

			stages, err := conflict.Stages(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			attrs, err := conflict.Resolve(ctx, "HEAD", []string{"f.txt"})
			if err != nil {
				t.Fatal(err)
			}
			labels, err := conflict.MergeLabels(ctx, "feature", mergeBases(t, dir))
			if err != nil {
				t.Fatal(err)
			}

			got, conflicted, err := conflict.Reconstruct(ctx, stages["f.txt"], attrs["f.txt"], labels)
			if err != nil {
				t.Fatal(err)
			}
			if !conflicted {
				t.Error("the reconstruction of a conflicted path reported a clean merge")
			}

			want, ok, err := conflict.AutoMergeBlob(ctx, "f.txt")
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("no AUTO_MERGE content to compare against")
			}
			if string(got) != string(want) {
				t.Errorf("reconstruction is not byte-identical to AUTO_MERGE\n got: %q\nwant: %q", got, want)
			}
			// AUTO_MERGE and the working-tree file agree, so this also pins the
			// reconstruction against what the operator is editing.
			if string(got) != worktreeFile(t, dir, "f.txt") {
				t.Errorf("reconstruction differs from the working-tree file\n got: %q\nwant: %q", got, worktreeFile(t, dir, "f.txt"))
			}
		})
	}
}

func TestMergeLabelsNameTheBaseTheWayGitDoes(t *testing.T) {
	dir := conflictedMerge(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	bases := mergeBases(t, dir)
	if len(bases) != 1 {
		t.Fatalf("the fixture has %d merge bases, want 1", len(bases))
	}
	labels, err := conflict.MergeLabels(ctx, "feature", bases)
	if err != nil {
		t.Fatal(err)
	}
	short := strings.TrimSpace(testutil.GitOut(t, dir, "rev-parse", "--short", bases[0]))
	if labels.Ours != "HEAD" || labels.Theirs != "feature" || labels.Base != short {
		t.Errorf("labels = %+v, want {HEAD %s feature}", labels, short)
	}

	several, err := conflict.MergeLabels(ctx, "feature", []string{bases[0], bases[0]})
	if err != nil {
		t.Fatal(err)
	}
	if several.Base != "merged common ancestors" {
		t.Errorf("with several merge bases, Base = %q", several.Base)
	}

	none, err := conflict.MergeLabels(ctx, "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if none.Base != "empty tree" {
		t.Errorf("with no merge base, Base = %q", none.Base)
	}
}

// TestPickAndRevertLabelsMatchWhatTheSequencerWrote pins the two operation
// label shapes against real conflicts, including the direction that is the
// classic revert confusion: the incoming side of a revert is the commit's
// PARENT.
func TestPickAndRevertLabelsMatchWhatTheSequencerWrote(t *testing.T) {
	t.Run("cherry-pick", func(t *testing.T) {
		dir := newRepo(t)
		testutil.Chdir(t, dir)
		ctx := context.Background()
		testutil.Git(t, dir, "config", "merge.conflictStyle", "diff3")

		testutil.Git(t, dir, "switch", "-q", "-c", "feature")
		testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\n")
		testutil.Git(t, dir, "commit", "-q", "-am", "theirs subject")
		source := testutil.Rev(t, dir, "HEAD")
		testutil.Git(t, dir, "switch", "-q", "main")
		testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\n")
		testutil.Git(t, dir, "commit", "-q", "-am", "ours")
		mustConflict(t, dir, "cherry-pick", source)

		labels, err := conflict.PickLabels(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		assertLabelsInFile(t, worktreeFile(t, dir, "f.txt"), labels)
	})

	t.Run("revert", func(t *testing.T) {
		dir := newRepo(t)
		testutil.Chdir(t, dir)
		ctx := context.Background()
		testutil.Git(t, dir, "config", "merge.conflictStyle", "diff3")

		testutil.WriteFile(t, dir, "f.txt", "l1\nMID\nl3\n")
		testutil.Git(t, dir, "commit", "-q", "-am", "mid subject")
		source := testutil.Rev(t, dir, "HEAD")
		testutil.WriteFile(t, dir, "f.txt", "l1\nLATER\nl3\n")
		testutil.Git(t, dir, "commit", "-q", "-am", "later")
		mustConflict(t, dir, "revert", "--no-edit", source)

		labels, err := conflict.RevertLabels(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		assertLabelsInFile(t, worktreeFile(t, dir, "f.txt"), labels)
	})
}

// TestReconstructReproducesASequencerConflict runs the whole chain for a
// cherry-pick, where the labels come from the commit being applied rather than
// from a branch name.
func TestReconstructReproducesASequencerConflict(t *testing.T) {
	dir := newRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()
	testutil.Git(t, dir, "config", "merge.conflictStyle", "diff3")

	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "theirs subject")
	source := testutil.Rev(t, dir, "HEAD")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours")
	mustConflict(t, dir, "cherry-pick", source)

	stages, err := conflict.Stages(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := conflict.Resolve(ctx, "HEAD", []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	labels, err := conflict.PickLabels(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := conflict.Reconstruct(ctx, stages["f.txt"], attrs["f.txt"], labels)
	if err != nil {
		t.Fatal(err)
	}
	if want := worktreeFile(t, dir, "f.txt"); string(got) != want {
		t.Errorf("reconstruction of a cherry-pick conflict is not byte-identical\n got: %q\nwant: %q", got, want)
	}
}

// mergeBases returns the merge bases of HEAD and MERGE_HEAD, which is what a
// conclusion has to ask to label a diff3 region.
func mergeBases(t *testing.T, dir string) []string {
	t.Helper()
	return testutil.SplitLines(testutil.GitOut(t, dir, "merge-base", "--all", "HEAD", "MERGE_HEAD"))
}

// assertLabelsInFile checks that the three derived labels are the ones git
// wrote onto the marker lines of a diff3-style conflict.
func assertLabelsInFile(t *testing.T, content string, labels conflict.Labels) {
	t.Helper()
	for marker, want := range map[string]string{
		"<<<<<<< ": labels.Ours,
		"||||||| ": labels.Base,
		">>>>>>> ": labels.Theirs,
	} {
		found := false
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, marker) {
				found = true
				if got := strings.TrimPrefix(line, marker); got != want {
					t.Errorf("marker line %q carries %q, want %q", marker, got, want)
				}
			}
		}
		if !found {
			t.Errorf("no %q line in:\n%s", marker, content)
		}
	}
}
