package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A destructive history rewrite in a repository managed by release tooling is
// an ordinary operation: safegit performs it and records it in the rewrite
// journal. Consistency of the metadata that lives outside the commit graph
// (changelog hashes, tags, forge releases) is restored afterwards by the
// release tooling, which detects the dangling hashes as a hard error and heals
// them from that journal. These tests pin that safegit does not stand in the
// way -- there is no handshake to satisfy and no environment variable to set.

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

// TestScrubFileInRlsblManagedRepoProceeds: a raw, unorchestrated scrub file
// rewrites history and leaves the secret nowhere in the object store.
func TestScrubFileInRlsblManagedRepoProceeds(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "raw scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub file must proceed in an rlsbl-managed repo, got code %d: %s", code, stderr)
	}

	shas := revListReverse(t, dir)
	for i := 1; i < len(shas); i++ {
		content, ok := testutil.Show(t, dir, shas[i], "secret.txt")
		if ok && content != "REDACTED\n" {
			t.Errorf("commit %d: secret.txt = %q, want REDACTED", i, content)
		}
	}
}

// TestScrubMatchAndRunInRlsblManagedRepoProceed: the other two scrub entry
// points are equally unblocked.
func TestScrubMatchAndRunInRlsblManagedRepoProceed(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "raw scrub", "--entire-history")
	if code != 0 {
		t.Fatalf("scrub match must proceed in an rlsbl-managed repo, got code %d: %s", code, stderr)
	}

	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"REDACTED\"\nreplace = \"CLEARED\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "run",
		"--reason", "raw scrub", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run must proceed in an rlsbl-managed repo, got code %d: %s", code, stderr)
	}
}

// TestScrubInRlsblManagedRepoWritesJournal: the rewrite journal is what the
// release tooling heals from, so a raw rewrite must leave one behind.
func TestScrubInRlsblManagedRepoWritesJournal(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)
	before := revListReverse(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "raw scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): %s", code, stderr)
	}

	journal, err := os.ReadFile(filepath.Join(dir, ".git", "safegit", "rewrite-maps.jsonl"))
	if err != nil {
		t.Fatalf("reading rewrite journal: %v", err)
	}
	// The rewritten commit's old SHA must be mappable from the journal: that
	// is the input `rlsbl changelog remap --from-journal` consumes.
	rewritten := before[len(before)-1]
	if !strings.Contains(string(journal), rewritten) {
		t.Errorf("journal does not record the rewritten commit %s:\n%s", rewritten, journal)
	}
}

// authorNames returns the distinct author names across all commits.
func authorNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, line := range strings.Split(testutil.Git(t, dir, "log", "--format=%an", "--all"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names[line] = true
		}
	}
	return names
}

// TestAuthorRewriteInRlsblManagedRepoProceeds: author rewrite is as
// destructive as a scrub and is equally unblocked.
func TestAuthorRewriteInRlsblManagedRepoProceeds(t *testing.T) {
	dir, _ := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("author rewrite must proceed in an rlsbl-managed repo, got code %d: %s", code, stderr)
	}

	names := authorNames(t, dir)
	if names["Test"] || !names["Renamed"] {
		t.Errorf("author rewrite should have replaced Test with Renamed, got authors %v", names)
	}
}

// TestRewritePreviewsInRlsblManagedRepo: the read-only previews keep working,
// unchanged.
func TestRewritePreviewsInRlsblManagedRepo(t *testing.T) {
	dir, initialSHA := newRlsblRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--dry-run", "scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "preview", "secret.txt")
	if code != 0 {
		t.Fatalf("dry-run scrub file should succeed, got code %d: %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--dry-run", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("dry-run author rewrite should succeed, got code %d: %s", code, stderr)
	}
	names := authorNames(t, dir)
	if !names["Test"] || names["Renamed"] {
		t.Errorf("dry run must not modify history, got authors %v", names)
	}
}
