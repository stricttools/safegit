package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
)

// holdLock takes a real safegit lock on ref, held by THIS process, and releases
// it when the test ends. The holder is genuinely alive, so the staleness rules
// cannot reclaim it and a safegit subprocess contending for the same ref must
// wait out its configured timeout and then refuse.
func holdLock(t *testing.T, dir, ref, op string) {
	t.Helper()
	sgDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	if err := os.MkdirAll(sgDir, 0755); err != nil {
		t.Fatalf("creating %s: %v", sgDir, err)
	}
	lk, err := lock.Acquire(sgDir, sgDir, ref, op, 5*time.Second)
	if err != nil {
		t.Fatalf("the test could not take the %s lock it needs to hold: %v", ref, err)
	}
	t.Cleanup(func() { _ = lk.Release() })
}

// shortLockTimeout configures a one-second lock acquisition timeout so a
// contended-lock test finishes quickly instead of waiting out the 30s default.
func shortLockTimeout(t *testing.T, dir string) {
	t.Helper()
	if _, stderr, code := runSafegit(t, dir, "config", "set", "lock.acquireTimeoutSeconds", "1"); code != 0 {
		t.Fatalf("setting lock.acquireTimeoutSeconds failed (code %d): %s", code, stderr)
	}
}

// assertLockTimeoutRefusal pins both halves of the exit-8 contract: the typed
// exit code, and the REAL lock error rather than a fixed sentence that throws
// away which ref timed out and who was holding it.
func assertLockTimeoutRefusal(t *testing.T, command string, code int, stderr string) {
	t.Helper()
	if code != exitcode.LockTimeout {
		t.Errorf("%s under a held lock exited %d, want %d (LockTimeout); stderr: %s",
			command, code, exitcode.LockTimeout, stderr)
	}
	if !strings.Contains(stderr, "timeout acquiring lock on") {
		t.Errorf("%s must surface the real lock error, not a fixed string; stderr: %s", command, stderr)
	}
	if !strings.Contains(stderr, "held by") {
		t.Errorf("%s must name the lock holder from the real error; stderr: %s", command, stderr)
	}
}

// TestScrubFileLockTimeoutIsTyped covers `scrub file`, one of the four commands
// that contend on the single repo-wide rewrite lock.
func TestScrubFileLockTimeoutIsTyped(t *testing.T) {
	dir, initialSHA := newSecretRepo(t)
	shortLockTimeout(t, dir)
	holdLock(t, dir, "safegit/rewrite", "test-holder")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "file",
		"--from", initialSHA, "--reason", "lock timeout probe", "secret.txt")
	assertLockTimeoutRefusal(t, "scrub file", code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("a refused scrub file must not have rewritten history")
	}
}

// TestScrubMatchLockTimeoutIsTyped covers `scrub match`.
func TestScrubMatchLockTimeoutIsTyped(t *testing.T) {
	dir, _ := newSecretRepo(t)
	shortLockTimeout(t, dir)
	holdLock(t, dir, "safegit/rewrite", "test-holder")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "lock timeout probe",
		"--entire-history")
	assertLockTimeoutRefusal(t, "scrub match", code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("a refused scrub match must not have rewritten history")
	}
}

// TestScrubRunLockTimeoutIsTyped covers `scrub run`.
func TestScrubRunLockTimeoutIsTyped(t *testing.T) {
	dir, _ := newSecretRepo(t)
	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"hunter2\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml"); code != 0 {
		t.Fatalf("committing the recipe failed: %s", stderr)
	}
	shortLockTimeout(t, dir)
	holdLock(t, dir, "safegit/rewrite", "test-holder")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "run",
		"--reason", "lock timeout probe", "--entire-history", "recipe.toml")
	assertLockTimeoutRefusal(t, "scrub run", code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("a refused scrub run must not have rewritten history")
	}
}

// TestAuthorRewriteLockTimeoutIsTyped covers `author rewrite`, the fourth
// contender on the rewrite lock.
func TestAuthorRewriteLockTimeoutIsTyped(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "file.txt", "content\n", "add file")
	shortLockTimeout(t, dir)
	holdLock(t, dir, "safegit/rewrite", "test-holder")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	assertLockTimeoutRefusal(t, "author rewrite", code, stderr)
	if names := authorNames(t, dir); !names["Test"] || names["Renamed"] {
		t.Errorf("a refused author rewrite must not have rewritten history, got authors %v", names)
	}
}

// TestUndoLockTimeoutIsTyped covers undo, which already surfaced the real lock
// error and gains only the typed code. Undo contends on the branch ref itself,
// not on the rewrite lock.
func TestUndoLockTimeoutIsTyped(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "file.txt", "content\n", "add file")
	shortLockTimeout(t, dir)
	holdLock(t, dir, "refs/heads/main", "test-holder")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "undo", "--bypass-session")
	assertLockTimeoutRefusal(t, "undo", code, stderr)
}

// TestCommitHunkSpecOnBinaryFileIsTyped pins exit 14: a hunk spec against a
// file git reports as binary is refused with its own code rather than the
// undifferentiated general error.
func TestCommitHunkSpecOnBinaryFileIsTyped(t *testing.T) {
	dir := newRepo(t)
	binPath := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe}, 0644); err != nil {
		t.Fatal(err)
	}
	safegitCommitEnv(t, dir, scrubEnv, "add binary", "blob.bin")

	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0x00, 0x42}, 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "hunk of a binary file", "--", "blob.bin:1")
	if code != exitcode.BinaryHunkSpec {
		t.Errorf("a hunk spec on a binary file exited %d, want %d (BinaryHunkSpec); stderr: %s",
			code, exitcode.BinaryHunkSpec, stderr)
	}
	if !strings.Contains(stderr, "binary file") {
		t.Errorf("the refusal must say the file is binary; stderr: %s", stderr)
	}
}

// TestAmendHunkSpecOnBinaryFileIsTyped is the same refusal on the amend path,
// which reaches the staging step through its own code.
func TestAmendHunkSpecOnBinaryFileIsTyped(t *testing.T) {
	dir := newRepo(t)
	binPath := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe}, 0644); err != nil {
		t.Fatal(err)
	}
	safegitCommitEnv(t, dir, scrubEnv, "add binary", "blob.bin")

	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0x00, 0x42}, 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "--amend", "-m", "amend a binary hunk", "--", "blob.bin:1")
	if code != exitcode.BinaryHunkSpec {
		t.Errorf("an --amend hunk spec on a binary file exited %d, want %d (BinaryHunkSpec); stderr: %s",
			code, exitcode.BinaryHunkSpec, stderr)
	}
}

// TestFrameworkRefusalsExitOne records what the CLI framework does with a
// command line it will not accept, which is the empirical basis for the exit-2
// row in the generated documentation table: the framework's parse refusals are
// exit 1, and only safegit's OWN post-parse argument validation is exit 2.
func TestFrameworkRefusalsExitOne(t *testing.T) {
	dir := newRepo(t)

	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"commit", "--no-such-flag", "-m", "x", "--", "seed.txt"}},
		{"unknown command", []string{"no-such-command"}},
		{"missing required flag", []string{"push"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runSafegit(t, dir, tc.args...)
			if code != exitcode.General {
				t.Errorf("%v exited %d, want %d (the framework's own refusal code); stderr: %s",
					tc.args, code, exitcode.General, stderr)
			}
		})
	}

	// safegit's own validation, reached only after a successful parse, is the
	// other side of the split.
	if _, stderr, code := runSafegit(t, dir, "commit"); code != exitcode.Usage {
		t.Errorf("a commit with no message exited %d, want %d (Usage); stderr: %s", code, exitcode.Usage, stderr)
	}
}
