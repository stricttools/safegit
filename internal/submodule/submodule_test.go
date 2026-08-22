package submodule

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

func seedCommit(t *testing.T, dir, filename, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", msg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// createParentWithSubmodule creates a parent repo with an initialized
// submodule and returns (parentDir, parentGitDir, submoduleDir).
func createParentWithSubmodule(t *testing.T) (string, string, string) {
	t.Helper()
	rawBase := t.TempDir()
	// Resolve symlinks so expected paths match what git returns on systems
	// where temp dirs are symlinked (e.g. macOS /tmp -> /private/...).
	base, err := filepath.EvalSymlinks(rawBase)
	if err != nil {
		t.Fatalf("resolving temp dir symlinks: %v", err)
	}

	// Create the "remote" repo that the submodule points to.
	remote := filepath.Join(base, "remote")
	if err := os.Mkdir(remote, 0755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, remote)
	seedCommit(t, remote, "lib.txt", "library\n", "initial lib")

	// Create parent repo.
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, parent)
	seedCommit(t, parent, "root.txt", "root\n", "initial parent")

	// Add submodule.
	gitRun(t, parent, "config", "protocol.file.allow", "always")
	gitRun(t, parent, "submodule", "add", remote, "sub")
	gitRun(t, parent, "commit", "-m", "add submodule")

	subDir := filepath.Join(parent, "sub")
	parentGitDir := filepath.Join(parent, ".git")
	return parent, parentGitDir, subDir
}

func TestEnumerate_WithInitializedSubmodule(t *testing.T) {
	_, parentGitDir, subDir := createParentWithSubmodule(t)
	ctx := context.Background()

	infos, err := Enumerate(ctx, parentGitDir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 submodule, got %d", len(infos))
	}

	info := infos[0]
	if info.Name != "sub" {
		t.Errorf("Name = %q, want %q", info.Name, "sub")
	}
	if info.RelativePath != "sub" {
		t.Errorf("RelativePath = %q, want %q", info.RelativePath, "sub")
	}
	if info.WorkTreePath != subDir {
		t.Errorf("WorkTreePath = %q, want %q", info.WorkTreePath, subDir)
	}
	if !info.Initialized {
		t.Error("Initialized = false, want true")
	}
	if info.CommitSHA == "" {
		t.Error("CommitSHA is empty")
	}
	if len(info.CommitSHA) != 40 {
		t.Errorf("CommitSHA length = %d, want 40", len(info.CommitSHA))
	}

	expectedGitDir := filepath.Join(parentGitDir, "modules", "sub")
	if info.GitDir != expectedGitDir {
		t.Errorf("GitDir = %q, want %q", info.GitDir, expectedGitDir)
	}
	if info.SafegitDir != filepath.Join(expectedGitDir, "safegit") {
		t.Errorf("SafegitDir = %q, want %q", info.SafegitDir, filepath.Join(expectedGitDir, "safegit"))
	}
}

func TestEnumerate_NoSubmodules(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	seedCommit(t, dir, "file.txt", "content\n", "initial")

	ctx := context.Background()
	infos, err := Enumerate(ctx, filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 submodules, got %d", len(infos))
	}
}

func TestEnumerate_DeinitializedSubmodule(t *testing.T) {
	parent, parentGitDir, _ := createParentWithSubmodule(t)
	ctx := context.Background()

	// Deinit the submodule (removes working tree but keeps .git/modules/sub).
	gitRun(t, parent, "submodule", "deinit", "-f", "sub")

	infos, err := Enumerate(ctx, parentGitDir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 submodule, got %d", len(infos))
	}

	info := infos[0]
	if info.Name != "sub" {
		t.Errorf("Name = %q, want %q", info.Name, "sub")
	}
	if info.Initialized {
		t.Error("Initialized = true, want false")
	}
	if info.WorkTreePath != "" {
		t.Errorf("WorkTreePath = %q, want empty", info.WorkTreePath)
	}
	if info.CommitSHA != "" {
		t.Errorf("CommitSHA = %q, want empty", info.CommitSHA)
	}
	if info.GitDir == "" {
		t.Error("GitDir is empty for deinitialized submodule")
	}
}

func TestEnumerate_DeduplicatesInitializedOverDeinitialized(t *testing.T) {
	// An initialized submodule has both a working tree AND a .git/modules/
	// entry. Enumerate should return it once with Initialized=true.
	_, parentGitDir, _ := createParentWithSubmodule(t)
	ctx := context.Background()

	infos, err := Enumerate(ctx, parentGitDir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 (deduplicated), got %d", len(infos))
	}
	if !infos[0].Initialized {
		t.Error("deduplicated entry should be Initialized=true")
	}
}

func TestDetectParent_InsideSubmodule(t *testing.T) {
	_, parentGitDir, subDir := createParentWithSubmodule(t)

	old, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })

	ctx := context.Background()
	parent, ok := DetectParent(ctx)
	if !ok {
		t.Fatal("DetectParent returned ok=false inside a submodule")
	}
	if parent.GitDir != parentGitDir {
		t.Errorf("parent.GitDir = %q, want %q", parent.GitDir, parentGitDir)
	}
	if parent.SubmodulePath != "sub" {
		t.Errorf("parent.SubmodulePath = %q, want %q", parent.SubmodulePath, "sub")
	}
	// The work tree is carried because the parent's COMMITTED hooks live in
	// it: a cascade handed only the git dir could never find them.
	if want := filepath.Dir(parentGitDir); parent.WorkTree != want {
		t.Errorf("parent.WorkTree = %q, want %q", parent.WorkTree, want)
	}
}

func TestDetectParent_NotASubmodule(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	seedCommit(t, dir, "file.txt", "content\n", "initial")

	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })

	ctx := context.Background()
	_, ok := DetectParent(ctx)
	if ok {
		t.Error("DetectParent returned ok=true outside a submodule")
	}
}

func TestCheckNested_NoNesting(t *testing.T) {
	_, parentGitDir, _ := createParentWithSubmodule(t)
	ctx := context.Background()

	err := CheckNested(ctx, parentGitDir)
	if err != nil {
		t.Fatalf("CheckNested returned error for non-nested submodule: %v", err)
	}
}

func TestCheckNested_WithNesting(t *testing.T) {
	_, parentGitDir, subDir := createParentWithSubmodule(t)
	ctx := context.Background()

	// Create a .gitmodules file inside the submodule to simulate nesting.
	if err := os.WriteFile(filepath.Join(subDir, ".gitmodules"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	err := CheckNested(ctx, parentGitDir)
	if err == nil {
		t.Fatal("CheckNested returned nil, expected ErrNestedSubmodules")
	}
	if !errors.Is(err, ErrNestedSubmodules) {
		t.Fatalf("error does not wrap ErrNestedSubmodules: %v", err)
	}
	if !strings.Contains(err.Error(), "sub") {
		t.Errorf("error message should mention the submodule name: %v", err)
	}
}

func TestCheckNested_NoSubmodules(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	seedCommit(t, dir, "file.txt", "content\n", "initial")

	ctx := context.Background()
	err := CheckNested(ctx, filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf("CheckNested returned error for repo with no submodules: %v", err)
	}
}
