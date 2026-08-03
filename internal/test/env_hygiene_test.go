package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the environment hygiene contract of the subprocess spawn
// helpers: a spawned safegit must see a deliberately constructed environment,
// never whatever happens to be exported in the shell that runs `go test`.
// Ambient variables (a real CLAUDE_CODE_SESSION_ID, RLSBL_* handshakes,
// credentials) would otherwise silently change what the tests exercise.

func TestControlledEnvIsAllowlisted(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "ambient-poison-session")
	t.Setenv("RLSBL_DIST_DIR", "/ambient/dist")
	t.Setenv("GH_TOKEN", "ambient-token")

	env := controlledEnv(t)

	banned := []string{
		"CLAUDE_CODE_SESSION_ID=",
		"RLSBL_",
		"GH_TOKEN=",
	}
	for _, e := range env {
		for _, b := range banned {
			if strings.HasPrefix(e, b) {
				t.Errorf("controlled env leaked ambient variable %q", e)
			}
		}
	}

	required := []string{"PATH=", "HOME=", "GIT_TERMINAL_PROMPT="}
	for _, want := range required {
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("controlled env missing required variable prefix %q; got %v", want, env)
		}
	}

	// Explicit overrides must survive.
	withExtra := controlledEnv(t, "CLAUDE_CODE_SESSION_ID=explicit-session")
	found := false
	for _, e := range withExtra {
		if e == "CLAUDE_CODE_SESSION_ID=explicit-session" {
			found = true
		}
	}
	if !found {
		t.Errorf("controlled env dropped an explicit override; got %v", withExtra)
	}
}

func TestRunSafegitDoesNotInheritAmbientSessionID(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "ambient-poison-session")

	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "hygiene.txt"), []byte("hygiene\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "hygiene probe", "--", "hygiene.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if strings.Contains(msg, "ambient-poison-session") {
		t.Errorf("runSafegit inherited the ambient session ID; commit message:\n%s", msg)
	}
}

func TestRunSafegitEnvUsesOnlyExplicitOverrides(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "ambient-poison-session")

	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "hygiene-env.txt"), []byte("hygiene\n"), 0644); err != nil {
		t.Fatal(err)
	}

	env := []string{"CLAUDE_CODE_SESSION_ID=explicit-session"}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "explicit probe", "--", "hygiene-env.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if strings.Contains(msg, "ambient-poison-session") {
		t.Errorf("runSafegitEnv inherited the ambient session ID; commit message:\n%s", msg)
	}
	if !strings.Contains(msg, "explicit-session") {
		t.Errorf("runSafegitEnv dropped the explicit session ID; commit message:\n%s", msg)
	}
}
