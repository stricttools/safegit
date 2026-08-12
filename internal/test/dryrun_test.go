package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantLiveRewriteLock writes a rewrite lock file owned by the (live) test
// process, so any command that tries to acquire the rewrite lock blocks until
// its timeout and then fails. Returns the lock file path.
func plantLiveRewriteLock(t *testing.T, repoDir string) string {
	t.Helper()
	lockDir := filepath.Join(repoDir, ".git", "safegit", "locks", "safegit")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	lockFile := filepath.Join(lockDir, "rewrite.lock")
	content := fmt.Sprintf("pid=%d\nts=2026-01-01T00:00:00Z\nop=scrub-file\nhost=%s\n", os.Getpid(), hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
	return lockFile
}

// TestAuthorRewriteDryRunSkipsRewriteLock: a dry-run preview is read-only, so
// it must not contend for the repo-wide rewrite lock the way the execute path
// does. With the lock held by a live process, the preview still succeeds.
func TestAuthorRewriteDryRunSkipsRewriteLock(t *testing.T) {
	dir := newRepo(t)

	// Keep the lock timeout short so the pre-fix behaviour fails fast.
	if _, stderr, code := runSafegit(t, dir, "config", "set", "lock.acquireTimeoutSeconds", "1"); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}
	lockFile := plantLiveRewriteLock(t, dir)

	stdout, stderr, code := runSafegit(t, dir,
		"--dry-run", "author", "rewrite", "--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("author rewrite --dry-run should not need the rewrite lock (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("expected a preview on stdout, got: %s", stdout)
	}

	// The foreign lock must be left exactly as it was found.
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("dry-run disturbed the foreign rewrite lock: %v", err)
	}
}

// TestSubmoduleCommitDryRunDoesNotBumpParent: a dry-run commit inside a
// submodule must leave the parent repository completely untouched, even when
// commit.autoBumpParent is enabled there.
func TestSubmoduleCommitDryRunDoesNotBumpParent(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)

	// Land a real submodule commit while auto-bump is still disabled, so the
	// parent's gitlink is stale: this is exactly the state in which the parent
	// bump would produce a real commit.
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, subDir, "commit", "-m", "real sub change", "--", "file.txt"); code != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", code, stderr)
	}

	enableAutoBump(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentHeadBefore := revParseHEAD(t, parentDir)
	subHeadBefore := revParseHEAD(t, subDir)

	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("dry content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "-m", "dry sub change", "--", "file.txt")
	if code != 0 {
		t.Fatalf("dry-run commit in submodule failed (code %d): %s", code, stderr)
	}

	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("parent HEAD moved during a dry run: %s -> %s", parentHeadBefore[:12], got[:12])
	}
	if got := gitLog(t, parentDir, "HEAD"); got != parentCountBefore {
		t.Errorf("parent commit count changed during a dry run: %d -> %d", parentCountBefore, got)
	}
	if got := revParseHEAD(t, subDir); got != subHeadBefore {
		t.Errorf("submodule HEAD moved during a dry run: %s -> %s", subHeadBefore[:12], got[:12])
	}
}

// TestSubmoduleRewordDryRunDoesNotBumpParent covers the amend/reword autobump
// call site: a dry-run message reword inside a submodule must not commit in
// the parent either.
func TestSubmoduleRewordDryRunDoesNotBumpParent(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, subDir, "commit", "-m", "real sub change", "--", "file.txt"); code != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", code, stderr)
	}

	enableAutoBump(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentHeadBefore := revParseHEAD(t, parentDir)

	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "--amend", "-m", "reworded in dry run")
	if code != 0 {
		t.Fatalf("dry-run reword in submodule failed (code %d): %s", code, stderr)
	}

	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("parent HEAD moved during a dry-run reword: %s -> %s", parentHeadBefore[:12], got[:12])
	}
	if got := gitLog(t, parentDir, "HEAD"); got != parentCountBefore {
		t.Errorf("parent commit count changed during a dry-run reword: %d -> %d", parentCountBefore, got)
	}
}

// TestAuthorRewriteDryRunSkipsConfigLoad: the preview needs neither config nor
// lock, so an unreadable config file must not block it.
func TestAuthorRewriteDryRunSkipsConfigLoad(t *testing.T) {
	dir := newRepo(t)

	cfgPath := filepath.Join(dir, ".git", "safegit", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file missing at %s: %v", cfgPath, err)
	}
	if err := os.WriteFile(cfgPath, []byte("{ this is not json"), 0644); err != nil {
		t.Fatalf("corrupting config: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir,
		"--dry-run", "author", "rewrite", "--old-email", "test@test.com", "--new-email", "new@test.com")
	if code != 0 {
		t.Fatalf("author rewrite --dry-run should not load config (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("expected a preview on stdout, got: %s", stdout)
	}
}

// newRawSecretRepo builds a repo with a secret in history using raw git only,
// so .git/safegit/ is never created: exactly the state a first-ever safegit
// invocation sees. Returns the repo dir and the SHA of the first commit.
func newRawSecretRepo(t *testing.T) (dir, initialSHA string) {
	t.Helper()
	dir = evalTempDir(t)

	gitCmd(t, dir, "init", "--initial-branch=main")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "config", "user.name", "Test")

	writeRepoFile(t, dir, "secret.txt", "hunter2\n")
	gitCmd(t, dir, "add", "secret.txt")
	gitCmd(t, dir, "commit", "-m", "add secret")
	initialSHA = gitCmd(t, dir, "rev-parse", "HEAD")

	// Replacement content committed on top, so the tree is clean and
	// `scrub file` has something to substitute.
	writeRepoFile(t, dir, "secret.txt", "REDACTED\n")
	gitCmd(t, dir, "add", "secret.txt")
	gitCmd(t, dir, "commit", "-m", "commit replacement")

	if _, err := os.Stat(filepath.Join(dir, ".git", "safegit")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: .git/safegit already exists (stat err: %v)", err)
	}
	return dir, initialSHA
}

// writeRepoFile writes content to a path inside the repo.
func writeRepoFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// assertNoSafegitDir fails if the safegit data directory exists.
func assertNoSafegitDir(t *testing.T, dir, what string) {
	t.Helper()
	sgDir := filepath.Join(dir, ".git", "safegit")
	if _, err := os.Stat(sgDir); err == nil {
		entries, _ := os.ReadDir(sgDir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s wrote to disk: %s exists containing %v", what, sgDir, names)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", sgDir, err)
	}
}

// scrubMode names one of the three scrub entry points and builds its argv for a
// given repo.
type scrubMode struct {
	name string
	args func(initialSHA, recipePath string) []string
}

// scrubDryRunModes returns the three scrub entry points in their --dry-run form.
func scrubDryRunModes() []scrubMode {
	return []scrubMode{
		{"scrub file", func(initialSHA, _ string) []string {
			return []string{"--dry-run", "scrub", "file",
				"--from", initialSHA, "--reason", "preview", "secret.txt"}
		}},
		{"scrub match", func(_, _ string) []string {
			return []string{"--dry-run", "scrub", "match",
				"--pattern", "hunter2", "--replace", "GONE", "--reason", "preview", "--entire-history"}
		}},
		{"scrub run", func(_, recipePath string) []string {
			return []string{"--dry-run", "scrub", "run",
				"--reason", "preview", "--entire-history", recipePath}
		}},
	}
}

var dryRunScrubEnv = []string{"CLAUDE_CODE_SESSION_ID=dryrun-scrub-test"}

const dryRunRecipe = `
[[operations]]
pattern = "hunter2"
replace = "GONE"
`

// TestScrubDryRunDoesNotAutoInit: a preview must not write to disk. All three
// scrub modes used to call repo.EnsureInitialized before their dry-run branch,
// which created .git/safegit/ (directories, config.json, log) during a run that
// promised to change nothing.
func TestScrubDryRunDoesNotAutoInit(t *testing.T) {
	recipePath := writeRecipe(t, "dryrun-recipe.toml", dryRunRecipe)

	for _, mode := range scrubDryRunModes() {
		t.Run(mode.name, func(t *testing.T) {
			dir, initialSHA := newRawSecretRepo(t)
			stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, mode.args(initialSHA, recipePath)...)
			if code != 0 {
				t.Fatalf("%s --dry-run failed (code %d): stdout=%s stderr=%s", mode.name, code, stdout, stderr)
			}
			assertNoSafegitDir(t, dir, mode.name+" --dry-run")
		})
	}
}

// TestScrubDryRunPreviewsDirtyTree: a preview changes nothing, so it must work
// on a dirty working tree -- which is exactly when a preview is wanted (you are
// mid-edit and want to know what a scrub would do). All three scrub modes used
// to run requireCleanTree before their dry-run branch and refuse.
func TestScrubDryRunPreviewsDirtyTree(t *testing.T) {
	recipePath := writeRecipe(t, "dirty-recipe.toml", dryRunRecipe)

	for _, mode := range scrubDryRunModes() {
		t.Run(mode.name, func(t *testing.T) {
			dir, initialSHA := newSecretRepo(t)

			// Dirty the tree: one modified tracked file, one untracked file.
			writeRepoFile(t, dir, "seed.txt", "seed modified\n")
			writeRepoFile(t, dir, "untracked.txt", "not committed\n")

			stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, mode.args(initialSHA, recipePath)...)
			if code != 0 {
				t.Fatalf("%s --dry-run must preview on a dirty tree (code %d): stdout=%s stderr=%s",
					mode.name, code, stdout, stderr)
			}
			if strings.Contains(stderr, "working tree is dirty") {
				t.Errorf("%s --dry-run refused a dirty tree: %s", mode.name, stderr)
			}
			// The dirty files are untouched.
			for path, want := range map[string]string{
				"seed.txt":      "seed modified\n",
				"untracked.txt": "not committed\n",
			} {
				got, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}
				if string(got) != want {
					t.Errorf("%s changed during a dry run: %q, want %q", path, got, want)
				}
			}
		})
	}
}

// TestScrubExecuteStillRequiresCleanTree pins the other half of the same seam:
// moving the clean-tree check past the dry-run branch must not weaken the
// execute path, which still refuses a dirty working tree.
func TestScrubExecuteStillRequiresCleanTree(t *testing.T) {
	recipePath := writeRecipe(t, "execute-recipe.toml", dryRunRecipe)

	modes := []scrubMode{
		{"scrub file", func(initialSHA, _ string) []string {
			return []string{"--approve-consequential", "scrub", "file",
				"--from", initialSHA, "--reason", "execute", "secret.txt"}
		}},
		{"scrub match", func(_, _ string) []string {
			return []string{"--approve-consequential", "scrub", "match",
				"--pattern", "hunter2", "--replace", "GONE", "--reason", "execute", "--entire-history"}
		}},
		{"scrub run", func(_, recipePath string) []string {
			return []string{"--approve-consequential", "scrub", "run",
				"--reason", "execute", "--entire-history", recipePath}
		}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			dir, initialSHA := newSecretRepo(t)
			headBefore := revParseHEAD(t, dir)
			writeRepoFile(t, dir, "seed.txt", "seed modified\n")

			_, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, mode.args(initialSHA, recipePath)...)
			if code == 0 {
				t.Fatalf("%s must refuse a dirty working tree, got code 0: %s", mode.name, stderr)
			}
			if !strings.Contains(stderr, "working tree is dirty") {
				t.Errorf("%s must say the tree is dirty, got: %s", mode.name, stderr)
			}
			if got := revParseHEAD(t, dir); got != headBefore {
				t.Errorf("history was rewritten despite the dirty tree: %s -> %s", headBefore[:12], got[:12])
			}
		})
	}
}
