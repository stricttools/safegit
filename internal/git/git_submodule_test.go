package git

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// initRepoWithSubmodule creates a parent repo containing a submodule. The
// submodule has its own distinct commit and blob. Returns (parentDir,
// submoduleGitDir, submoduleWorkTree, submoduleHeadSHA).
func initRepoWithSubmodule(t *testing.T) (string, string, string, string) {
	t.Helper()

	// Create the repo that will become the submodule source.
	subSrc := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subSrc
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(subSrc, "sub.txt"), []byte("submodule content\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "sub.txt"},
		{"git", "commit", "-m", "submodule initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subSrc
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the submodule source HEAD.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = subSrc
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	subHeadSHA := strings.TrimSpace(string(out))

	// Create the parent repo and add the submodule.
	parent := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(parent, "parent.txt"), []byte("parent content\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "parent.txt"},
		{"git", "commit", "-m", "parent initial"},
		{"git", "config", "protocol.file.allow", "always"},
		{"git", "submodule", "add", subSrc, "mysub"},
		{"git", "commit", "-m", "add submodule"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Resolve the submodule's git dir. Modern git stores submodule git dirs
	// under parent/.git/modules/<name>/.
	subWorkTree := filepath.Join(parent, "mysub")
	cmd = exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = subWorkTree
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	subGitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(subGitDir) {
		subGitDir = filepath.Join(subWorkTree, subGitDir)
	}

	return parent, subGitDir, subWorkTree, subHeadSHA
}

func TestSubmoduleRunWithGitDirRevParse(t *testing.T) {
	parent, subGitDir, subWorkTree, subHeadSHA := initRepoWithSubmodule(t)
	// Set cwd to the parent repo -- RunWithGitDir must still target the submodule.
	testutil.Chdir(t, parent)
	ctx := context.Background()

	// Get the parent HEAD for comparison.
	parentHead, _, err := Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	parentHead = strings.TrimSpace(parentHead)

	// Run rev-parse HEAD against the submodule's git dir.
	stdout, _, err := RunWithGitDir(ctx, subGitDir, subWorkTree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout)

	if got != subHeadSHA {
		t.Errorf("RunWithGitDir rev-parse HEAD = %q, want submodule HEAD %q", got, subHeadSHA)
	}
	if got == parentHead {
		t.Errorf("RunWithGitDir returned the parent HEAD %q, should return submodule HEAD", parentHead)
	}
}

func TestSubmoduleRunWithGitDirCatFile(t *testing.T) {
	parent, subGitDir, subWorkTree, _ := initRepoWithSubmodule(t)
	testutil.Chdir(t, parent)
	ctx := context.Background()

	// The submodule has sub.txt; read its content via the submodule's git dir.
	stdout, _, err := RunWithGitDir(ctx, subGitDir, subWorkTree, "cat-file", "-p", "HEAD:sub.txt")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "submodule content\n" {
		t.Errorf("cat-file HEAD:sub.txt = %q, want %q", stdout, "submodule content\n")
	}

	// Verify that the parent's file is NOT accessible via the submodule's git dir.
	_, _, err = RunWithGitDir(ctx, subGitDir, subWorkTree, "cat-file", "-e", "HEAD:parent.txt")
	if err == nil {
		t.Error("expected error accessing parent.txt via submodule git dir, got nil")
	}
}

func TestSubmoduleCatFileBatchAllWithDir(t *testing.T) {
	parent, subGitDir, _, _ := initRepoWithSubmodule(t)
	testutil.Chdir(t, parent)
	ctx := context.Background()

	it, err := CatFileBatchAllWithDir(ctx, subGitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	var blobContents []string
	var commitCount int
	for {
		entry, err := it.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		switch entry.Type {
		case "blob":
			blobContents = append(blobContents, string(entry.Content))
		case "commit":
			commitCount++
		case "tree":
			t.Fatal("got tree entry, expected trees to be skipped")
		}
	}

	// The submodule has one commit and one blob ("submodule content\n").
	if commitCount < 1 {
		t.Errorf("commit count = %d, want >= 1", commitCount)
	}

	foundSubBlob := false
	for _, c := range blobContents {
		if c == "submodule content\n" {
			foundSubBlob = true
			break
		}
	}
	if !foundSubBlob {
		t.Errorf("submodule blob not found; blobs: %v", blobContents)
	}

	// Verify parent-only content is NOT in the submodule's object store.
	for _, c := range blobContents {
		if c == "parent content\n" {
			t.Error("found parent blob in submodule object store, expected isolation")
		}
	}
}

func TestSubmoduleRegularRunStillWorks(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	stdout, _, err := Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(stdout))
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("Run rev-parse --show-toplevel = %q, want %q", got, want)
	}
}
