package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// envWithPath is the controlled environment with PATH replaced.
func envWithPath(t *testing.T, path string) []string {
	t.Helper()
	var env []string
	for _, kv := range controlledEnv(t) {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	return append(env, "PATH="+path)
}

// runWithPath runs safegit in dir with exactly the given PATH.
func runWithPath(t *testing.T, dir, path string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(safegitBin, args...)
	cmd.Dir = dir
	cmd.Env = envWithPath(t, path)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running safegit: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// TestCommandWithoutGitNamesTheRequirement: git is a declared runtime
// requirement, so a command run where git cannot be found is refused before it
// starts, naming git and how to install it -- and putting git back on PATH,
// which is what that names, clears the refusal.
func TestCommandWithoutGitNamesTheRequirement(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	empty := evalTempDir(t)
	_, stderr, code := runWithPath(t, dir, empty, "commit", "-m", "x", "--", "a.txt")
	if code != 1 {
		t.Errorf("exit = %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "error: command 'commit' needs git (") || !strings.Contains(stderr, "install it: ") {
		t.Errorf("the refusal does not name the git requirement and its install line: %s", stderr)
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runWithPath(t, dir, filepath.Dir(gitPath), "commit", "-m", "x", "--", "a.txt")
	if code != 0 || strings.Contains(stderr, "needs git") {
		t.Errorf("with git on PATH the commit must run, got exit %d: %s", code, stderr)
	}
}

// TestEveryCommandDeclaresTheGitRequirement: every safegit command runs git,
// so the help document lists git under each command's requires.
func TestEveryCommandDeclaresTheGitRequirement(t *testing.T) {
	dir := newRepo(t)
	stdout, stderr, code := runSafegit(t, dir, "help", "--json")
	if code != 0 {
		t.Fatalf("help --json failed (%d): %s", code, stderr)
	}
	type command struct {
		Requires []struct {
			Name string `json:"name"`
		} `json:"requires"`
	}
	type group struct {
		Commands map[string]command `json:"commands"`
	}
	var doc struct {
		Commands map[string]command `json:"commands"`
		Groups   map[string]group   `json:"groups"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("help document does not parse: %v", err)
	}
	check := func(name string, c command) {
		for _, r := range c.Requires {
			if r.Name == "git" {
				return
			}
		}
		t.Errorf("command %q does not declare the git requirement", name)
	}
	n := 0
	for name, c := range doc.Commands {
		check(name, c)
		n++
	}
	for gname, g := range doc.Groups {
		for name, c := range g.Commands {
			check(gname+" "+name, c)
			n++
		}
	}
	if n == 0 {
		t.Fatal("the help document lists no commands")
	}
}
