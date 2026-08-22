package git

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/gitversion"
	"github.com/smm-h/safegit/internal/testutil"
)

// conflictedRepo builds a repository stopped on a content conflict in f.txt and
// returns its working directory. It runs the real commands, so what the test
// reads afterwards is what git actually wrote.
func conflictedRepo(t *testing.T) string {
	t.Helper()
	dir := testutil.InitBareRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-m", "base")

	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "theirs")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours")

	if _, code := testutil.GitTry(t, dir, "merge", "feature"); code == 0 {
		t.Fatal("the fixture merge was expected to conflict")
	}
	return dir
}

func TestUnmergedStagesReportsAllThreeStages(t *testing.T) {
	dir := conflictedRepo(t)
	testutil.Chdir(t, dir)

	entries, err := UnmergedStages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("UnmergedStages returned %d entries, want 3: %+v", len(entries), entries)
	}
	for i, e := range entries {
		if e.Path != "f.txt" {
			t.Errorf("entry %d: path = %q, want f.txt", i, e.Path)
		}
		if e.Stage != i+1 {
			t.Errorf("entry %d: stage = %d, want %d", i, e.Stage, i+1)
		}
		if e.Mode != "100644" {
			t.Errorf("entry %d: mode = %q, want 100644", i, e.Mode)
		}
		if len(e.SHA) != 40 {
			t.Errorf("entry %d: sha = %q, want a full object name", i, e.SHA)
		}
	}
}

func TestUnmergedStagesReadsTheIndexItIsGiven(t *testing.T) {
	dir := conflictedRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// A clean index built from HEAD has no unmerged entries at all, so naming
	// it proves the reader is not silently answering from the shared index.
	other := filepath.Join(t.TempDir(), "index")
	if err := ReadTree(ctx, other, "HEAD"); err != nil {
		t.Fatal(err)
	}
	entries, err := UnmergedStages(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a clean index reported %d unmerged entries: %+v", len(entries), entries)
	}

	// And the shared index, read in the same process, still reports the
	// conflict.
	shared, err := UnmergedStages(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 3 {
		t.Fatalf("the shared index reported %d unmerged entries, want 3", len(shared))
	}
}

func TestUnmergedStagesHandlesAPathWithASpace(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	const name = "a file with spaces.txt"

	testutil.WriteFile(t, dir, name, "base\n")
	testutil.Git(t, dir, "add", name)
	testutil.Git(t, dir, "commit", "-m", "base")
	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, name, "theirs\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "theirs")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, name, "ours\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours")
	if _, code := testutil.GitTry(t, dir, "merge", "feature"); code == 0 {
		t.Fatal("the fixture merge was expected to conflict")
	}

	entries, err := UnmergedStages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no unmerged entries")
	}
	for _, e := range entries {
		if e.Path != name {
			t.Errorf("path = %q, want %q -- the NUL-delimited listing must arrive unquoted", e.Path, name)
		}
	}
}

func TestCheckAttrReadsTheWorkingTreeByDefault(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=12\n")

	got, err := CheckAttr(context.Background(), "", []string{"conflict-marker-size"}, []string{"f.txt", "f.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if v := got["f.txt"]["conflict-marker-size"]; v != "12" {
		t.Errorf("f.txt conflict-marker-size = %q, want 12", v)
	}
	if v := got["f.bin"]["conflict-marker-size"]; v != AttrUnspecified {
		t.Errorf("f.bin conflict-marker-size = %q, want %q", v, AttrUnspecified)
	}
}

func TestCheckAttrReadsANamedTreeInsteadOfTheWorkingTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=9\n")
	testutil.Git(t, dir, "add", ".gitattributes")
	testutil.Git(t, dir, "commit", "-m", "attributes")

	// The working tree now says something different from the commit.
	testutil.WriteFile(t, dir, ".gitattributes", "*.txt conflict-marker-size=40\n")

	ctx := context.Background()
	fromTree, err := CheckAttr(ctx, "HEAD", []string{"conflict-marker-size"}, []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if v := fromTree["f.txt"]["conflict-marker-size"]; v != "9" {
		t.Errorf("from HEAD: conflict-marker-size = %q, want 9 (the committed value)", v)
	}

	fromWorktree, err := CheckAttr(ctx, "", []string{"conflict-marker-size"}, []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if v := fromWorktree["f.txt"]["conflict-marker-size"]; v != "40" {
		t.Errorf("from the working tree: conflict-marker-size = %q, want 40", v)
	}
}

func TestCheckAttrRefusesWithNoAttributeNames(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	if _, err := CheckAttr(context.Background(), "", nil, []string{"f.txt"}); err == nil {
		t.Fatal("CheckAttr accepted a call naming no attribute")
	}
}

func TestCheckAttrWithNoPathsAsksGitNothing(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	got, err := CheckAttr(context.Background(), "", []string{"conflict-marker-size"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an empty path list produced %d answers", len(got))
	}
}

func TestMergeFileReproducesGitsOwnConflictedFile(t *testing.T) {
	dir := conflictedRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	sides := stageBlobs(t, ctx, dir, "f.txt")
	// git's default conflict style writes no base label at all, so only the two
	// side labels have to match. internal/conflict pins how git derives all
	// three, base label included.
	merged, conflicted, err := MergeFile(ctx, sides[2], sides[1], sides[3], MergeFileOptions{
		OursLabel:   "HEAD",
		TheirsLabel: "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conflicted {
		t.Error("MergeFile reported a clean merge for a conflicted path")
	}

	want := testutil.MustShow(t, dir, "AUTO_MERGE", "f.txt")
	if string(merged) != want {
		t.Errorf("reconstruction is not byte-identical to what git recorded in AUTO_MERGE\n got: %q\nwant: %q", merged, want)
	}
}

func TestMergeFileHonorsStyleAndMarkerSize(t *testing.T) {
	dir := conflictedRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()
	sides := stageBlobs(t, ctx, dir, "f.txt")

	diff3, _, err := MergeFile(ctx, sides[2], sides[1], sides[3], MergeFileOptions{
		OursLabel: "HEAD", BaseLabel: "base", TheirsLabel: "feature", Style: StyleDiff3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff3), "||||||| base") {
		t.Errorf("diff3 output carries no base region:\n%s", diff3)
	}

	big, _, err := MergeFile(ctx, sides[2], sides[1], sides[3], MergeFileOptions{
		OursLabel: "HEAD", BaseLabel: "base", TheirsLabel: "feature", MarkerSize: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(big), strings.Repeat("<", 12)+" HEAD") {
		t.Errorf("marker size 12 was not honored:\n%s", big)
	}
	if strings.Contains(string(big), strings.Repeat("<", 13)) {
		t.Errorf("marker is longer than the requested 12:\n%s", big)
	}
}

func TestMergeFileReportsACleanMergeAsClean(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	merged, conflicted, err := MergeFile(context.Background(),
		[]byte("OURS\nl2\nl3\n"), []byte("l1\nl2\nl3\n"), []byte("l1\nl2\nTHEIRS\n"),
		MergeFileOptions{OursLabel: "ours", BaseLabel: "base", TheirsLabel: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	if conflicted {
		t.Error("MergeFile reported a conflict for two non-overlapping edits")
	}
	if string(merged) != "OURS\nl2\nTHEIRS\n" {
		t.Errorf("merged = %q", merged)
	}
}

// TestMergeFileFailureIsStillAnError pins the other half of the exit-status
// reading: a conflict count is success, but a genuine failure to run is not
// swallowed into "the merge conflicted".
func TestMergeFileFailureIsStillAnError(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, conflicted, err := MergeFile(ctx, []byte("a\n"), []byte("b\n"), []byte("c\n"),
		MergeFileOptions{OursLabel: "o", BaseLabel: "b", TheirsLabel: "t"})
	if err == nil {
		t.Fatal("MergeFile reported success although git could not run")
	}
	if conflicted {
		t.Error("a failure to run must not be reported as a conflicted merge")
	}
}

func TestStripCommentsRemovesTheConflictsBlock(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	msg, err := StripComments(context.Background(), "Merge branch 'feature'\n\n# Conflicts:\n#\tf.txt\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "Conflicts") {
		t.Errorf("the comment block survived stripping: %q", msg)
	}
	if !strings.HasPrefix(msg, "Merge branch 'feature'") {
		t.Errorf("the message itself did not survive: %q", msg)
	}
}

func TestStripCommentsHonorsACustomCommentChar(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	testutil.Git(t, dir, "config", "core.commentChar", ";")

	msg, err := StripComments(context.Background(), "subject\n\n; a comment\n# not a comment\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "; a comment") {
		t.Errorf("the ;-comment survived, so core.commentChar was not honored: %q", msg)
	}
	if !strings.Contains(msg, "# not a comment") {
		t.Errorf("a # line was stripped although core.commentChar is ';': %q", msg)
	}
}

func TestConfigGetTellsAbsentApartFromEmpty(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	value, set, err := ConfigGet(ctx, "merge.conflictStyle")
	if err != nil {
		t.Fatal(err)
	}
	if set {
		t.Errorf("merge.conflictStyle reported as set (%q) in a fresh repository", value)
	}

	testutil.Git(t, dir, "config", "merge.conflictStyle", "diff3")
	value, set, err = ConfigGet(ctx, "merge.conflictStyle")
	if err != nil {
		t.Fatal(err)
	}
	if !set || value != "diff3" {
		t.Errorf("ConfigGet = (%q, %v), want (diff3, true)", value, set)
	}
}

func TestParseConflictStyle(t *testing.T) {
	for value, want := range map[string]ConflictStyle{
		"":       StyleMerge,
		"merge":  StyleMerge,
		"diff3":  StyleDiff3,
		"zdiff3": StyleZdiff3,
	} {
		got, err := ParseConflictStyle(value)
		if err != nil {
			t.Errorf("ParseConflictStyle(%q): %v", value, err)
			continue
		}
		if got != want {
			t.Errorf("ParseConflictStyle(%q) = %q, want %q", value, got, want)
		}
	}
	if _, err := ParseConflictStyle("ours"); err == nil {
		t.Error("an unrecognized merge.conflictStyle must be an error, never a silent default")
	}
}

// TestRunPassthroughWithEnvCarriesTheIndexFile pins the property the conclusion
// delegation rests on: an environment entry reaches the child, GIT_INDEX_FILE
// specifically is not one of the entries the boundary refuses, and the
// repository's shared index stays untouched while the named one is written.
func TestRunPassthroughWithEnvCarriesTheIndexFile(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "new.txt", "content\n")
	copyPath := filepath.Join(t.TempDir(), "index-copy")
	if err := ReadTree(ctx, copyPath, "HEAD"); err != nil {
		t.Fatal(err)
	}

	if err := RunPassthroughWithEnv(ctx, []string{"GIT_INDEX_FILE=" + copyPath},
		"update-index", "--add", "new.txt"); err != nil {
		t.Fatalf("RunPassthroughWithEnv: %v", err)
	}

	inCopy, _, err := RunWithEnv(ctx, []string{"GIT_INDEX_FILE=" + copyPath}, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inCopy, "new.txt") {
		t.Errorf("the named index does not hold the staged path: %q", inCopy)
	}

	shared, _, err := Run(ctx, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shared, "new.txt") {
		t.Errorf("the shared index was written: %q", shared)
	}
}

func TestRequireFeatureAcceptsTheInstalledGit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	// The repository's own floors: the suite cannot run at all on a git that
	// does not meet them, so this is a real check rather than a tautology.
	for _, f := range gitversion.Features() {
		if err := RequireFeature(context.Background(), f); err != nil {
			t.Errorf("the installed git does not satisfy a declared floor: %v", err)
		}
	}
}

// stageBlobs returns the unmerged stage contents of path, keyed by stage
// number. A stage the conflict does not have is absent from the map.
func stageBlobs(t *testing.T, ctx context.Context, dir, path string) map[int][]byte {
	t.Helper()
	entries, err := UnmergedStages(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[int][]byte{}
	for _, e := range entries {
		if e.Path != path {
			continue
		}
		blob, err := CatFileBlob(ctx, e.SHA)
		if err != nil {
			t.Fatal(err)
		}
		out[e.Stage] = blob
	}
	return out
}
