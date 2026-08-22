package index

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/testutil"
)

// These tests exercise NewFromFile in the shape the conclusion flows use it:
// copy the repository's shared index -- unmerged stage entries and all -- stage
// the resolution into the COPY, and write a tree from the copy, while the
// shared .git/index is only ever read.

// conflictedRepo builds a repository stopped on a content conflict in f.txt and
// returns its working directory, its git directory and its safegit directory.
func conflictedRepo(t *testing.T) (repoDir, gitDir, sgDir string) {
	t.Helper()
	repoDir = testutil.InitBareRepo(t)
	gitDir = filepath.Join(repoDir, ".git")
	sgDir = filepath.Join(gitDir, "safegit")
	if err := os.MkdirAll(filepath.Join(sgDir, "tmp"), 0755); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, repoDir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, repoDir, "add", "f.txt")
	testutil.Git(t, repoDir, "commit", "-m", "base")

	testutil.Git(t, repoDir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, repoDir, "f.txt", "l1\nTHEIRS\nl3\n")
	testutil.Git(t, repoDir, "commit", "-q", "-am", "theirs")

	testutil.Git(t, repoDir, "switch", "-q", "main")
	testutil.WriteFile(t, repoDir, "f.txt", "l1\nOURS\nl3\n")
	testutil.Git(t, repoDir, "commit", "-q", "-am", "ours")

	if _, code := testutil.GitTry(t, repoDir, "merge", "feature"); code == 0 {
		t.Fatal("the fixture merge was expected to conflict")
	}
	return repoDir, gitDir, sgDir
}

func TestNewFromFileCopiesTheSharedIndexVerbatim(t *testing.T) {
	repoDir, gitDir, sgDir := conflictedRepo(t)
	testutil.Chdir(t, repoDir)

	sharedPath := filepath.Join(gitDir, "index")
	before, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := NewFromFile(sgDir, sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Cleanup()

	copied, err := os.ReadFile(idx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, copied) {
		t.Error("the copy is not byte-identical to the shared index")
	}

	// The copy carries the unmerged entries, which is the whole point: a
	// conclusion resolves them there.
	unmerged, err := git.UnmergedStages(context.Background(), idx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(unmerged) != 3 {
		t.Fatalf("the copy holds %d unmerged entries, want 3", len(unmerged))
	}
}

func TestNewFromFileTakesTheResolutionWithoutTouchingTheSharedIndex(t *testing.T) {
	repoDir, gitDir, sgDir := conflictedRepo(t)
	testutil.Chdir(t, repoDir)
	ctx := context.Background()

	sharedPath := filepath.Join(gitDir, "index")
	before, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := NewFromFile(sgDir, sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Cleanup()

	// A conflicted index cannot produce a tree, and git's own refusal is the
	// completeness check a conclusion leans on.
	if _, err := git.WriteTree(ctx, idx.IndexPath); err == nil {
		t.Fatal("write-tree produced a tree from an index with unmerged entries")
	}

	// Stage the resolution into the COPY.
	testutil.WriteFile(t, repoDir, "f.txt", "l1\nRESOLVED\nl3\n")
	if err := git.AddFile(ctx, idx.IndexPath, "f.txt"); err != nil {
		t.Fatal(err)
	}

	unmerged, err := git.UnmergedStages(ctx, idx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(unmerged) != 0 {
		t.Fatalf("the copy still holds %d unmerged entries after staging the resolution", len(unmerged))
	}

	tree, err := git.WriteTree(ctx, idx.IndexPath)
	if err != nil {
		t.Fatalf("write-tree on the resolved copy: %v", err)
	}
	content, err := git.CatFileBlob(ctx, tree+":f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "l1\nRESOLVED\nl3\n" {
		t.Errorf("the written tree holds %q", content)
	}

	// The shared index is exactly as it was: still conflicted, byte for byte.
	after, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the shared index changed while a conclusion staged into its copy")
	}
	stillUnmerged, err := git.UnmergedStages(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stillUnmerged) != 3 {
		t.Errorf("the shared index reports %d unmerged entries, want the original 3", len(stillUnmerged))
	}
}

func TestNewFromFileTreatsAnAbsentIndexAsEmpty(t *testing.T) {
	gitDir := initIndexTestRepo(t)
	sgDir := filepath.Join(gitDir, "safegit")
	testutil.Chdir(t, filepath.Dir(gitDir))

	idx, err := NewFromFile(sgDir, filepath.Join(gitDir, "no-such-index"))
	if err != nil {
		t.Fatalf("an absent index file must read as git's empty index, not as a failure: %v", err)
	}
	defer idx.Cleanup()

	if _, err := os.Stat(idx.IndexPath); !os.IsNotExist(err) {
		t.Error("NewFromFile invented an index file where the source had none")
	}
	tree, err := git.WriteTree(context.Background(), idx.IndexPath)
	if err != nil {
		t.Fatalf("write-tree on an empty index: %v", err)
	}
	if strings.TrimSpace(tree) == "" {
		t.Error("write-tree produced no tree for the empty index")
	}
}

func TestNewFromFileLandsUnderTheSafegitTmpDirectory(t *testing.T) {
	repoDir, gitDir, sgDir := conflictedRepo(t)
	testutil.Chdir(t, repoDir)

	idx, err := NewFromFile(sgDir, filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Cleanup()

	rel, err := filepath.Rel(filepath.Join(sgDir, "tmp"), idx.Dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("the copy lives at %s, which is not under %s", idx.Dir, filepath.Join(sgDir, "tmp"))
	}
	if _, ok := parsePIDFromDirName(filepath.Base(idx.Dir)); !ok {
		t.Errorf("the directory name %q does not carry the owning PID, so garbage collection cannot claim it", filepath.Base(idx.Dir))
	}
}
