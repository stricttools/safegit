package conflict_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/testutil"
)

// What an octopus merge does to conflict reading, recorded here because the
// marker verification built on this package has to scope itself around it.
//
// Three facts, each asserted below against a real conflicted octopus:
//
//  1. Git writes NO AUTO_MERGE. The recorded-content path does not exist for an
//     octopus, so anything that requires it must either fall back to
//     reconstruction or refuse explicitly.
//  2. The index's stages describe only the LAST PAIRWISE STEP. The octopus
//     strategy merges heads one at a time, so stage 2 is the intermediate
//     result of the earlier steps -- a blob that exists in no commit at all --
//     and stage 3 is the last head. An N-way conflict is not representable in
//     three stages, and merge-file cannot express one either.
//  3. The marker LABELS are random temporary file names (".merge_file_XXXXXX"),
//     because the strategy shells out to git's file-level merge with temporary
//     files. They are unpredictable by construction, so a byte-identical
//     reconstruction of an octopus conflict is impossible; only the region
//     CONTENT can be reproduced, and only for that last pairwise step.

// conflictedOctopus merges two branches into main at once, conflicting on
// f.txt, and returns the working directory.
func conflictedOctopus(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.Git(t, dir, "switch", "-q", "-c", "b1")
	testutil.WriteFile(t, dir, "f.txt", "B1\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "b1")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.Git(t, dir, "switch", "-q", "-c", "b2")
	testutil.WriteFile(t, dir, "f.txt", "B2\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "b2")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nMAIN\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main")

	mustConflict(t, dir, "merge", "b1", "b2")
	return dir
}

func TestOctopusMergeRecordsNoAutoMerge(t *testing.T) {
	dir := conflictedOctopus(t)
	testutil.Chdir(t, dir)

	_, present, err := conflict.AutoMergeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("an octopus merge recorded an AUTO_MERGE tree; the scoping this fact forces can be revisited")
	}
	// The merge really is in flight and really is conflicted, so the absence
	// above is about the octopus and not about there being no merge.
	if len(testutil.SplitLines(testutil.GitOut(t, dir, "ls-files", "-u"))) == 0 {
		t.Fatal("the fixture left no unmerged entries")
	}
	if heads := testutil.SplitLines(worktreeFile(t, dir, ".git/MERGE_HEAD")); len(heads) != 2 {
		t.Fatalf("MERGE_HEAD holds %d heads, want 2", len(heads))
	}
}

func TestOctopusStagesDescribeOnlyTheLastPairwiseStep(t *testing.T) {
	dir := conflictedOctopus(t)
	testutil.Chdir(t, dir)

	stages, err := conflict.Stages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	sides := stages["f.txt"]
	if !sides.ContentConflict() {
		t.Fatalf("f.txt is not reported as a content conflict: %+v", sides)
	}

	// Stage 3 is the LAST head merged, not the first.
	if got, want := sides.Theirs.SHA, testutil.Rev(t, dir, "b2:f.txt"); got != want {
		t.Errorf("stage 3 = %s, want b2's blob %s", got, want)
	}
	// Stage 2 is the intermediate result of merging the earlier head into
	// HEAD: it matches no commit's blob, which is exactly why an octopus
	// conflict cannot be described as "ours versus theirs".
	for _, rev := range []string{"HEAD:f.txt", "b1:f.txt", "b2:f.txt"} {
		if sides.Ours.SHA == testutil.Rev(t, dir, rev) {
			t.Errorf("stage 2 equals %s; the fixture no longer produces a pairwise intermediate", rev)
		}
	}
}

func TestOctopusMarkersCarryUnpredictableLabels(t *testing.T) {
	dir := conflictedOctopus(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	content := worktreeFile(t, dir, "f.txt")
	oursLabel, theirsLabel := markerLabels(t, content)
	tempName := regexp.MustCompile(`^\.merge_file_\w+$`)
	if !tempName.MatchString(oursLabel) || !tempName.MatchString(theirsLabel) {
		t.Fatalf("octopus marker labels are %q and %q; the recorded fact was a pair of temporary file names", oursLabel, theirsLabel)
	}

	stages, err := conflict.Stages(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := conflict.Resolve(ctx, "HEAD", []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}

	// With the labels git happened to use, the reconstruction is exact: the
	// REGION CONTENT of the last pairwise step is fully reproducible.
	exact, _, err := conflict.Reconstruct(ctx, stages["f.txt"], attrs["f.txt"],
		conflict.Labels{Ours: oursLabel, Theirs: theirsLabel})
	if err != nil {
		t.Fatal(err)
	}
	if string(exact) != content {
		t.Errorf("reconstruction with the observed labels is not byte-identical\n got: %q\nwant: %q", exact, content)
	}

	// With the labels a merge conclusion would derive, it is not -- and cannot
	// be made to be, because the real ones are random.
	derived, err := conflict.MergeLabels(ctx, "b2", nil)
	if err != nil {
		t.Fatal(err)
	}
	guessed, _, err := conflict.Reconstruct(ctx, stages["f.txt"], attrs["f.txt"], derived)
	if err != nil {
		t.Fatal(err)
	}
	if string(guessed) == content {
		t.Error("derived labels reproduced the octopus file exactly; the unpredictable-label finding no longer holds")
	}
	if stripMarkerLines(string(guessed)) != stripMarkerLines(content) {
		t.Errorf("everything but the marker lines must still match\n got: %q\nwant: %q", guessed, content)
	}
}

// markerLabels returns the ours and theirs labels of the first conflicted
// region in content.
func markerLabels(t *testing.T, content string) (ours, theirs string) {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< ") && ours == "":
			ours = strings.TrimPrefix(line, "<<<<<<< ")
		case strings.HasPrefix(line, ">>>>>>> ") && theirs == "":
			theirs = strings.TrimPrefix(line, ">>>>>>> ")
		}
	}
	if ours == "" || theirs == "" {
		t.Fatalf("no conflict markers in:\n%s", content)
	}
	return ours, theirs
}

// stripMarkerLines drops every conflict marker line, leaving the content the
// regions carry.
func stripMarkerLines(content string) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, ">>>>>>>") ||
			strings.HasPrefix(line, "|||||||") || line == "=======" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
