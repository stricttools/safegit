package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type SubmoduleRepo struct {
	ParentDir    string
	ParentGitDir string
	SubDir       string
	SubGitDir    string
	SubOriginDir string
	SubName      string
}

func initOriginRepo(t *testing.T, filename string) string {
	t.Helper()
	dir := evalTempDir(t)

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, filename), []byte(filename+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}

func initParentRepo(t *testing.T) string {
	t.Helper()
	dir := evalTempDir(t)

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "protocol.file.allow", "always"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}

func addSubmodule(t *testing.T, parentDir, originDir, name string) SubmoduleRepo {
	t.Helper()

	for _, args := range [][]string{
		{"git", "submodule", "add", originDir, name},
		{"git", "commit", "-m", "add submodule " + name},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	subDir := filepath.Join(parentDir, name)

	// The submodule's .git is a file pointing to the real git dir under
	// .git/modules/<name>. Read and resolve it.
	dotGit := filepath.Join(subDir, ".git")
	content, err := os.ReadFile(dotGit)
	if err != nil {
		t.Fatalf("reading submodule .git file: %v", err)
	}
	rel := strings.TrimSpace(strings.TrimPrefix(string(content), "gitdir: "))
	subGitDir, err := filepath.Abs(filepath.Join(subDir, rel))
	if err != nil {
		t.Fatalf("resolving submodule git dir: %v", err)
	}

	return SubmoduleRepo{
		ParentDir:    parentDir,
		ParentGitDir: filepath.Join(parentDir, ".git"),
		SubDir:       subDir,
		SubGitDir:    subGitDir,
		SubOriginDir: originDir,
		SubName:      name,
	}
}

func InitRepoWithSubmodule(t *testing.T) SubmoduleRepo {
	t.Helper()

	originDir := initOriginRepo(t, "sub-file.txt")
	parentDir := initParentRepo(t)
	return addSubmodule(t, parentDir, originDir, "mysub")
}

func InitRepoWithTwoSubmodules(t *testing.T) (SubmoduleRepo, SubmoduleRepo) {
	t.Helper()

	origin1 := initOriginRepo(t, "sub-file.txt")
	origin2 := initOriginRepo(t, "sub2-file.txt")
	parentDir := initParentRepo(t)

	sub1 := addSubmodule(t, parentDir, origin1, "mysub")
	sub2 := addSubmodule(t, parentDir, origin2, "mysub2")

	return sub1, sub2
}
