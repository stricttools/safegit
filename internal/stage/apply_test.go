package stage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/testutil"
)

// ApplyPatch used to answer a failed `git apply --cached` by silently running
// the same patch again with --3way and reporting success if that second attempt
// worked. The two attempts do not stage the same thing: the first stages exactly
// the hunks the caller selected, while the three-way retry MERGES the patch into
// whatever the index holds, so a caller who asked for one hunk could end up with
// a merge result nobody named, and nothing in the output said a retry had
// happened at all.
//
// The retry is deleted. A patch that does not apply is a hard error, and the
// repo-relative path is attached by the caller (internal/commit's
// stagingHunksError), which is the only place that spelling exists -- the stage
// package is handed the absolute path.
//
// The fixture is the cleanest case where the two attempts differ: the index
// already holds the patch's post-image, so the direct apply fails ("patch does
// not apply") while the three-way merge of an already-applied patch succeeds and
// changes nothing. Before the deletion this test failed with a nil error.
func TestApplyPatchFailureIsNotRescuedByAThreeWayRetry(t *testing.T) {
	dir, sgDir := initStageTestRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	seedPath := filepath.Join(dir, "seed.txt")
	lines := strings.Split(strings.TrimSuffix(readFile(t, seedPath), "\n"), "\n")
	lines[9] = "line 10 CHANGED"
	if err := os.WriteFile(seedPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(ctx, sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	header, hunks, err := ExtractHunks(ctx, tmpIdx.IndexPath, "seed.txt")
	if err != nil {
		t.Fatalf("ExtractHunks: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("the fixture must produce exactly one hunk, got %d", len(hunks))
	}
	patch, err := BuildPatch(header, hunks, []int{1})
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}

	// Put the patch's post-image into the index, so the patch no longer applies
	// to it and only a three-way merge could still "succeed".
	if err := git.AddFile(ctx, tmpIdx.IndexPath, seedPath); err != nil {
		t.Fatalf("staging the post-image: %v", err)
	}

	err = ApplyPatch(ctx, tmpIdx.IndexPath, patch)
	if err == nil {
		t.Fatal("a patch that does not apply was reported as staged; the three-way retry rescued it")
	}
	if strings.Contains(err.Error(), "3way") {
		t.Errorf("the failure still names the deleted retry: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
