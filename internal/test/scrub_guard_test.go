package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newRlsblRepo creates a repo that looks rlsbl-managed (has a committed
// .rlsbl/ directory) with a secret file ready to scrub.
func newRlsblRepo(t *testing.T) (dir, initialSHA string) {
	t.Helper()
	dir = newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".rlsbl"), 0755); err != nil {
		t.Fatal(err)
	}
	commitFileEnv(t, dir, scrubEnv, ".rlsbl/config.json", "{}\n", "add rlsbl config")

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA = revListReverse(t, dir)[0]
	// Replacement content committed so the tree is clean for scrub file.
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")
	return dir, initialSHA
}

// TestScrubGuardBlocksFileInRlsblRepo: a destructive scrub file in an
// rlsbl-managed repo without orchestration must die with exit code 1 and a
// message pointing at the release tooling.
func TestScrubGuardBlocksFileInRlsblRepo(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "guard test", "secret.txt")
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "rlsbl release scrub") {
		t.Errorf("guard message should point at 'rlsbl release scrub', got: %s", stderr)
	}
	if strings.Contains(stderr, "RLSBL_SCRUB_ORCHESTRATED") {
		t.Errorf("guard message must not advertise the bypass env var, got: %s", stderr)
	}

	// No rewrite happened: secret content is still in history.
	shas := revListReverse(t, dir)
	content, ok := gitShow(t, dir, shas[2], "secret.txt")
	if !ok || content != "hunter2\n" {
		t.Errorf("history should be unchanged after blocked scrub, got %q ok=%v", content, ok)
	}
}

// TestScrubGuardBlocksMatchAndRun: the guard applies to scrub match and
// scrub run too.
func TestScrubGuardBlocksMatchAndRun(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "guard test", "--entire-history")
	if code != 1 {
		t.Fatalf("scrub match: expected exit code 1, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "rlsbl release scrub") {
		t.Errorf("scrub match guard message wrong: %s", stderr)
	}

	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"hunter2\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "run",
		"--reason", "guard test", "--entire-history", "recipe.toml")
	if code != 1 {
		t.Fatalf("scrub run: expected exit code 1, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "rlsbl release scrub") {
		t.Errorf("scrub run guard message wrong: %s", stderr)
	}
}

// TestScrubGuardEnvVarAllowsOrchestratedScrub: with RLSBL_SCRUB_ORCHESTRATED=1
// the scrub proceeds normally.
func TestScrubGuardEnvVarAllowsOrchestratedScrub(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	env := append([]string{}, scrubEnv...)
	env = append(env, "RLSBL_SCRUB_ORCHESTRATED=1")
	_, stderr, code := runSafegitEnv(t, dir, env, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "guard test orchestrated", "secret.txt")
	if code != 0 {
		t.Fatalf("orchestrated scrub should succeed, got code %d: %s", code, stderr)
	}

	shas := revListReverse(t, dir)
	for i := 1; i < len(shas); i++ {
		content, ok := gitShow(t, dir, shas[i], "secret.txt")
		if ok && content != "REDACTED\n" {
			t.Errorf("commit %d: secret.txt = %q, want REDACTED", i, content)
		}
	}
}

// TestScrubGuardEnvVarWrongValueStillBlocks: only the exact value "1" lifts
// the guard.
func TestScrubGuardEnvVarWrongValueStillBlocks(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	env := append([]string{}, scrubEnv...)
	env = append(env, "RLSBL_SCRUB_ORCHESTRATED=0")
	_, stderr, code := runSafegitEnv(t, dir, env, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "guard test", "secret.txt")
	if code != 1 {
		t.Fatalf("expected exit code 1 with wrong env value, got %d (stderr: %s)", code, stderr)
	}
}

// TestScrubGuardDryRunAndDiffAllowed: read-only previews work in rlsbl repos
// without orchestration.
func TestScrubGuardDryRunAndDiffAllowed(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--dry-run", "scrub", "file",
		"--from", initialSHA, "--reason", "guard test dry run", "secret.txt")
	if code != 0 {
		t.Fatalf("dry-run scrub file should succeed, got code %d: %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "--dry-run", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "guard test", "--entire-history")
	if code != 0 {
		t.Fatalf("dry-run scrub match should succeed, got code %d: %s", code, stderr)
	}

	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"hunter2\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "run",
		"--diff", "--reason", "guard test diff", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run --diff should succeed, got code %d: %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "--dry-run", "scrub", "run",
		"--reason", "guard test dry run", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run --dry-run should succeed, got code %d: %s", code, stderr)
	}
}

// authorNames returns the distinct author names across all commits.
func authorNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, line := range strings.Split(gitCmd(t, dir, "log", "--format=%an", "--all"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names[line] = true
		}
	}
	return names
}

// TestAuthorRewriteGuardBlocksInRlsblRepo: author rewrite is an equally
// destructive history rewrite and must be blocked in rlsbl-managed repos
// without orchestration, with a message pointing at rlsbl and without
// advertising the bypass env var.
func TestAuthorRewriteGuardBlocksInRlsblRepo(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "rlsbl") {
		t.Errorf("guard message should point at rlsbl, got: %s", stderr)
	}
	if strings.Contains(stderr, "RLSBL_SCRUB_ORCHESTRATED") {
		t.Errorf("guard message must not advertise the bypass env var, got: %s", stderr)
	}

	// No rewrite happened: the old author name is still in history.
	names := authorNames(t, dir)
	if !names["Test"] || names["Renamed"] {
		t.Errorf("history should be unchanged after blocked author rewrite, got authors %v", names)
	}
}

// TestAuthorRewriteGuardEnvVarAllows: with RLSBL_SCRUB_ORCHESTRATED=1 the
// author rewrite proceeds normally in an rlsbl-managed repo.
func TestAuthorRewriteGuardEnvVarAllows(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	env := append([]string{}, scrubEnv...)
	env = append(env, "RLSBL_SCRUB_ORCHESTRATED=1")
	_, stderr, code := runSafegitEnv(t, dir, env, "--yes", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("orchestrated author rewrite should succeed, got code %d: %s", code, stderr)
	}

	names := authorNames(t, dir)
	if names["Test"] || !names["Renamed"] {
		t.Errorf("author rewrite should have replaced Test with Renamed, got authors %v", names)
	}
}

// TestAuthorRewriteGuardEnvVarWrongValueStillBlocks: only the exact value "1"
// lifts the guard for author rewrite.
func TestAuthorRewriteGuardEnvVarWrongValueStillBlocks(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	env := append([]string{}, scrubEnv...)
	env = append(env, "RLSBL_SCRUB_ORCHESTRATED=0")
	_, stderr, code := runSafegitEnv(t, dir, env, "--yes", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 1 {
		t.Fatalf("expected exit code 1 with wrong env value, got %d (stderr: %s)", code, stderr)
	}
}

// TestAuthorRewriteGuardDryRunAllowed: the read-only dry-run preview works in
// rlsbl repos without orchestration.
func TestAuthorRewriteGuardDryRunAllowed(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--dry-run", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("dry-run author rewrite should succeed, got code %d: %s", code, stderr)
	}

	// Dry run changed nothing.
	names := authorNames(t, dir)
	if !names["Test"] || names["Renamed"] {
		t.Errorf("dry run must not modify history, got authors %v", names)
	}
}

// TestAuthorRewriteGuardNonRlsblRepoUnaffected: repos without .rlsbl markers
// rewrite authors normally without the env var.
func TestAuthorRewriteGuardNonRlsblRepoUnaffected(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "file.txt", "content\n", "add file")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("author rewrite in non-rlsbl repo should succeed, got code %d: %s", code, stderr)
	}

	names := authorNames(t, dir)
	if names["Test"] || !names["Renamed"] {
		t.Errorf("author rewrite should have replaced Test with Renamed, got authors %v", names)
	}
}

// TestScrubGuardNonRlsblRepoUnaffected: repos without .rlsbl markers scrub
// normally without the env var.
func TestScrubGuardNonRlsblRepoUnaffected(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "non-rlsbl scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub in non-rlsbl repo should succeed, got code %d: %s", code, stderr)
	}
}
