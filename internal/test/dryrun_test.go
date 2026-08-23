package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// plantLiveRewriteLock writes a rewrite lock file owned by the (live) test
// process into a repository's own safegit directory, so any command that tries
// to acquire the rewrite lock blocks until its timeout and then fails. Returns
// the lock file path.
func plantLiveRewriteLock(t *testing.T, repoDir string) string {
	t.Helper()
	return plantLiveRewriteLockAt(t, filepath.Join(repoDir, ".git", "safegit"))
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
	parentHeadBefore := testutil.Rev(t, parentDir, "HEAD")
	subHeadBefore := testutil.Rev(t, subDir, "HEAD")

	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("dry content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "-m", "dry sub change", "--", "file.txt")
	if code != 0 {
		t.Fatalf("dry-run commit in submodule failed (code %d): %s", code, stderr)
	}

	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHeadBefore {
		t.Errorf("parent HEAD moved during a dry run: %s -> %s", parentHeadBefore[:12], got[:12])
	}
	if got := gitLog(t, parentDir, "HEAD"); got != parentCountBefore {
		t.Errorf("parent commit count changed during a dry run: %d -> %d", parentCountBefore, got)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHeadBefore {
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
	parentHeadBefore := testutil.Rev(t, parentDir, "HEAD")

	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "--amend", "-m", "reworded in dry run")
	if code != 0 {
		t.Fatalf("dry-run reword in submodule failed (code %d): %s", code, stderr)
	}

	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHeadBefore {
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

	testutil.Git(t, dir, "init", "--initial-branch=main")
	testutil.Git(t, dir, "config", "user.email", "test@test.com")
	testutil.Git(t, dir, "config", "user.name", "Test")

	testutil.WriteFile(t, dir, "secret.txt", "hunter2\n")
	testutil.Git(t, dir, "add", "secret.txt")
	testutil.Git(t, dir, "commit", "-m", "add secret")
	initialSHA = testutil.Git(t, dir, "rev-parse", "HEAD")

	// Replacement content committed on top, so the tree is clean and
	// `scrub file` has something to substitute.
	testutil.WriteFile(t, dir, "secret.txt", "REDACTED\n")
	testutil.Git(t, dir, "add", "secret.txt")
	testutil.Git(t, dir, "commit", "-m", "commit replacement")

	if _, err := os.Stat(filepath.Join(dir, ".git", "safegit")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: .git/safegit already exists (stat err: %v)", err)
	}
	return dir, initialSHA
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
			return []string{"--dry-run", "scrub", "file", "--replace-with", "secret.txt",
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
			testutil.WriteFile(t, dir, "seed.txt", "seed modified\n")
			testutil.WriteFile(t, dir, "untracked.txt", "not committed\n")

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

// TestDryRunInUninitializedRepoStillPreviews guards the other side of the
// auto-init seam: skipping auto-init under --dry-run must not turn a preview
// into an error in a repo where safegit has never run. The config a first
// execute run would read is the default one auto-init writes, so a preview
// works from those same defaults without creating anything.
func TestDryRunInUninitializedRepoStillPreviews(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"commit", []string{"--dry-run", "commit", "-m", "preview", "--", "new.txt"}},
		{"config set", []string{"--dry-run", "config", "set", "push.retryAttempts", "7"}},
		{"config show", []string{"--dry-run", "config", "show"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := newRawSecretRepo(t)
			testutil.WriteFile(t, dir, "new.txt", "new content\n")
			headBefore := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, tc.args...)
			if code != 0 {
				t.Fatalf("%s --dry-run in an uninitialized repo failed (code %d): stdout=%s stderr=%s",
					tc.name, code, stdout, stderr)
			}
			assertNoSafegitDir(t, dir, tc.name+" --dry-run")
			if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
				t.Errorf("HEAD moved during a dry run: %s -> %s", headBefore[:12], got[:12])
			}
		})
	}
}

// TestCommitDryRunLeavesNoSafegitDir: a dry-run commit must write nothing to
// disk, the same promise the scrub previews keep. The commit pipeline created
// its per-invocation temp index under .git/safegit/tmp/ even under --dry-run,
// and Cleanup() left .git/safegit/tmp/ behind. The leftover directory made the
// repo read as initialized while config.json was absent, so every later safegit
// invocation there -- preview or execute -- died with "reading config.json: no
// such file or directory". The second pass below is the one that used to fail.
func TestCommitDryRunLeavesNoSafegitDir(t *testing.T) {
	dir, _ := newRawSecretRepo(t)
	testutil.WriteFile(t, dir, "new.txt", "new content\n")
	headBefore := testutil.Rev(t, dir, "HEAD")

	for _, pass := range []string{"first", "second"} {
		stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv,
			"--dry-run", "commit", "-m", "preview", "--", "new.txt")
		if code != 0 {
			t.Fatalf("%s --dry-run commit failed (code %d): stdout=%s stderr=%s",
				pass, code, stdout, stderr)
		}
		assertNoSafegitDir(t, dir, pass+" --dry-run commit")
	}

	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved during a dry-run commit: %s -> %s", headBefore[:12], got[:12])
	}
}

// TestAmendDryRunLeavesNoSafegitDir is TestCommitDryRunLeavesNoSafegitDir for
// the amend pipeline, which reaches the same temp-index seam (indexBaseDir)
// through tryAmend. Without this the seam could regress back to the repo
// directory on the amend side alone and the suite would stay green. Both
// --amend forms are covered: an amend that stages files (which builds a temp
// index) and a reword (which does not), since neither may create .git/safegit
// or move HEAD.
func TestAmendDryRunLeavesNoSafegitDir(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"amend with files", []string{"--dry-run", "commit", "--amend", "-m", "preview amend", "--", "new.txt"}},
		{"reword", []string{"--dry-run", "commit", "--amend", "-m", "preview reword"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := newRawSecretRepo(t)
			testutil.WriteFile(t, dir, "new.txt", "new content\n")
			headBefore := testutil.Rev(t, dir, "HEAD")

			// Two passes: a leftover .git/safegit without config.json is what
			// makes the *next* invocation in the same repo die on the missing
			// config, so the second pass is the one that shows the damage.
			for _, pass := range []string{"first", "second"} {
				stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, tc.args...)
				if code != 0 {
					t.Fatalf("%s --dry-run (%s pass) failed (code %d): stdout=%s stderr=%s",
						tc.name, pass, code, stdout, stderr)
				}
				assertNoSafegitDir(t, dir, pass+" --dry-run "+tc.name)
			}

			if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
				t.Errorf("HEAD moved during a dry-run %s: %s -> %s", tc.name, headBefore[:12], got[:12])
			}
		})
	}
}

// previewDirLeftovers returns the names of the safegit-preview-* entries left
// in root.
func previewDirLeftovers(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading temp root %s: %v", root, err)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "safegit-preview-") {
			leftovers = append(leftovers, e.Name())
		}
	}
	return leftovers
}

// TestCommitDryRunCleansUpPreviewTempDir: routing the preview index out of the
// repository moves the cleanup obligation to the OS temp root, where nothing
// garbage-collects it (the doctor only sweeps .git/safegit/tmp). The
// per-invocation safegit-preview-* directory must therefore be removed on the
// way out of every path, not just the one where a commit object gets built.
// TMPDIR is pointed at a directory this test owns, so the assertion sees only
// what this test's own invocations created.
func TestCommitDryRunCleansUpPreviewTempDir(t *testing.T) {
	dir, _ := newRawSecretRepo(t)
	testutil.WriteFile(t, dir, "new.txt", "new content\n")

	tmpRoot := filepath.Join(t.TempDir(), "preview-tmp")
	if err := os.MkdirAll(tmpRoot, 0755); err != nil {
		t.Fatalf("creating the test-owned temp root: %v", err)
	}
	env := append([]string{"TMPDIR=" + tmpRoot}, dryRunScrubEnv...)

	// Success path: a preview that stages, writes the tree and builds the
	// commit object.
	stdout, stderr, code := runSafegitEnv(t, dir, env,
		"--dry-run", "commit", "-m", "preview", "--", "new.txt")
	if code != 0 {
		t.Fatalf("dry-run commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if leftovers := previewDirLeftovers(t, tmpRoot); len(leftovers) > 0 {
		t.Errorf("a successful dry-run commit left preview dirs behind in %s: %v", tmpRoot, leftovers)
	}

	// Failure path: the temp index is created and the run then aborts on the
	// unchanged tree, so the cleanup has to run on the error return too.
	_, stderr, code = runSafegitEnv(t, dir, env,
		"--dry-run", "commit", "-m", "preview", "--", "secret.txt")
	if code == 0 {
		t.Fatalf("a dry-run commit of an unchanged file must fail, got code 0")
	}
	if !strings.Contains(stderr, "nothing to commit") {
		t.Fatalf("expected a nothing-to-commit failure, got: %s", stderr)
	}
	if leftovers := previewDirLeftovers(t, tmpRoot); len(leftovers) > 0 {
		t.Errorf("a failed dry-run commit left preview dirs behind in %s: %v", tmpRoot, leftovers)
	}

	// The counterpart assertion: nothing went into the repository either, so
	// the preview index really did live under the temp root.
	assertNoSafegitDir(t, dir, "dry-run commit with TMPDIR set")
}

// TestHalfInitializedSafegitDirIsRepaired: a .git/safegit/ that exists without
// config.json is a half-initialized repository -- any stray subdirectory puts it
// there. Initialization must complete such a directory instead of reading the
// bare directory as proof that everything is present, which left every command
// failing on the missing config.json until someone deleted .git/safegit by hand.
func TestHalfInitializedSafegitDirIsRepaired(t *testing.T) {
	dir, _ := newRawSecretRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".git", "safegit", "tmp"), 0755); err != nil {
		t.Fatalf("manufacturing the half-initialized state: %v", err)
	}
	testutil.WriteFile(t, dir, "new.txt", "new content\n")

	stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv,
		"commit", "-m", "real commit", "--", "new.txt")
	if code != 0 {
		t.Fatalf("an execute-path command must repair a half-initialized safegit dir (code %d): stdout=%s stderr=%s",
			code, stdout, stderr)
	}
	cfgPath := filepath.Join(dir, ".git", "safegit", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("the repair did not create config.json: %v", err)
	}
}

// TestAuthorRewriteDryRunPreviewsDirtyTree: `author rewrite` is the fourth
// rewrite entry point and kept the defect its scrub siblings had -- it ran the
// clean-tree check before its dry-run branch, so a preview refused exactly when
// it is most wanted (mid-edit, deciding whether to rewrite at all).
func TestAuthorRewriteDryRunPreviewsDirtyTree(t *testing.T) {
	dir := newRepo(t)
	headBefore := testutil.Rev(t, dir, "HEAD")

	// Dirty the tree: one modified tracked file, one untracked file.
	testutil.WriteFile(t, dir, "seed.txt", "seed modified\n")
	testutil.WriteFile(t, dir, "untracked.txt", "not committed\n")

	stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv,
		"--dry-run", "author", "rewrite", "--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("author rewrite --dry-run must preview on a dirty tree (code %d): stdout=%s stderr=%s",
			code, stdout, stderr)
	}
	if strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("author rewrite --dry-run refused a dirty tree: %s", stderr)
	}
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("expected a preview on stdout, got: %s", stdout)
	}

	// The dirty files and the history are untouched.
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
	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("history was rewritten by a dry run: %s -> %s", headBefore[:12], got[:12])
	}
}

// TestAuthorRewriteExecuteStillRequiresCleanTree is the non-weakening half of
// the same move: the execute path keeps refusing a dirty working tree, whose
// uncommitted work a rewrite would lose.
func TestAuthorRewriteExecuteStillRequiresCleanTree(t *testing.T) {
	dir := newRepo(t)
	headBefore := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "seed.txt", "seed modified\n")

	_, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv,
		"--approve-consequential", "author", "rewrite", "--old-name", "Test", "--new-name", "Renamed")
	if code == 0 {
		t.Fatalf("author rewrite must refuse a dirty working tree, got code 0: %s", stderr)
	}
	if !strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("author rewrite must say the tree is dirty, got: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("history was rewritten despite the dirty tree: %s -> %s", headBefore[:12], got[:12])
	}
}

// TestScrubExecuteStillRequiresCleanTree pins the other half of the same seam:
// moving the clean-tree check past the dry-run branch must not weaken the
// execute path, which still refuses a dirty working tree.
func TestScrubExecuteStillRequiresCleanTree(t *testing.T) {
	recipePath := writeRecipe(t, "execute-recipe.toml", dryRunRecipe)

	modes := []scrubMode{
		{"scrub file", func(initialSHA, _ string) []string {
			return []string{"--approve-consequential", "scrub", "file", "--replace-with", "secret.txt",
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
			headBefore := testutil.Rev(t, dir, "HEAD")
			testutil.WriteFile(t, dir, "seed.txt", "seed modified\n")

			_, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv, mode.args(initialSHA, recipePath)...)
			if code == 0 {
				t.Fatalf("%s must refuse a dirty working tree, got code 0: %s", mode.name, stderr)
			}
			if !strings.Contains(stderr, "working tree is dirty") {
				t.Errorf("%s must say the tree is dirty, got: %s", mode.name, stderr)
			}
			if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
				t.Errorf("history was rewritten despite the dirty tree: %s -> %s", headBefore[:12], got[:12])
			}
		})
	}
}

// A preview mutates nothing, so it has nothing to serialize against and must
// take neither the worktree operation lock nor the per-ref CAS lock. Two
// properties, pinned together because each alone would miss half of it:
//
//   - Nothing is left behind. Every lock file under the safegit directory
//     after the dry run was already there before it, so a preview cannot
//     strand a lock for the next contender to wait out or for doctor to
//     report.
//   - Nothing was taken DURING the run either. Both locks a real commit takes
//     are held by this (live) test process for the whole subtest, so a dry run
//     that tried to acquire either one would wait out the one-second timeout
//     and exit LockTimeout instead of previewing. A file scan alone could not
//     see that: a lock taken and released cleanly leaves no trace.
//
// The third consequence is the operator-visible one: a preview stays possible
// while another safegit process owns the worktree.
//
// undo rides the same table: it is the fourth entry point that moves the branch
// ref, it takes both of these locks on its execute path, and its preview must
// keep taking neither. Pinning that here is what stops the fix for undo's empty
// effects record -- minting the ref move through the effects handle -- from
// dragging the lock acquisition above the dry-run branch along with it.
func TestDryRunCommitFamilyTakesNoLocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"commit", []string{"--dry-run", "commit", "-m", "previewed", "--", "preview.txt"}},
		{"amend with files", []string{"--dry-run", "commit", "--amend", "-m", "previewed", "--", "preview.txt"}},
		{"reword", []string{"--dry-run", "commit", "--amend", "-m", "previewed"}},
		{"undo", []string{"--dry-run", "undo", "--bypass-session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			// A real commit first: it creates the locks subtree and removes its
			// own locks again, so the assertion below is about what the dry run
			// adds rather than about a directory that never existed.
			testutil.WriteFile(t, dir, "committed.txt", "content\n")
			safegitCommit(t, dir, "a real commit", "committed.txt")
			testutil.WriteFile(t, dir, "preview.txt", "to be previewed\n")

			sgDir := filepath.Join(dir, ".git", "safegit")
			shortLockTimeout(t, dir)
			holdLock(t, dir, "refs/heads/main", "test-holder")
			holdLock(t, dir, "safegit/operation", "test-holder")
			planted := heldLockFiles(t, sgDir)
			if len(planted) != 2 {
				t.Fatalf("fixture: expected the two planted locks, got %v", planted)
			}

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("the preview did not run (code %d); a dry run must not contend for a lock.\n  stdout: %s\n  stderr: %s",
					code, oneLine(stdout), oneLine(stderr))
			}

			after := heldLockFiles(t, sgDir)
			if strings.Join(after, "\n") != strings.Join(planted, "\n") {
				t.Errorf("the dry run changed the set of lock files.\n  before: %v\n  after:  %v", planted, after)
			}
		})
	}
}
