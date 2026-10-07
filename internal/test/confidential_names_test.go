package test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// These tests cover the commit command's confidential-name rules: a public
// repository (a repository without a lifecycle-and-license record included)
// has every commit scanned against the machine-local confidential-name index,
// and a confidential repository -- decided from its record's license periods,
// offline -- is not scanned and keeps its own entry in the index current.

// confidentialIndexEnv returns the environment entries that point a spawned
// safegit's user configuration directory at a fresh directory, and the path
// the confidential-name index has under it. The entries are the variables
// os.UserConfigDir reads.
func confidentialIndexEnv(t *testing.T) ([]string, string) {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	configDir := xdg
	if runtime.GOOS == "darwin" {
		configDir = filepath.Join(home, "Library", "Application Support")
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + xdg}
	return env, filepath.Join(configDir, "strictspec", "confidential-names.toml")
}

// writeConfidentialIndex writes an index holding the given entries, each keyed
// by one releasable name and holding its names.
func writeConfidentialIndex(t *testing.T, path string, entries map[string][]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("format_version = 1\n")
	for subject, names := range entries {
		b.WriteString("\n[[repositories]]\nsubjects = [\"" + subject + "\"]\nnames = [")
		for i, n := range names {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("\"" + n + "\"")
		}
		b.WriteString("]\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readConfidentialIndex returns the index file's text, or "" when it does not
// exist.
func readConfidentialIndex(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeProprietaryRecord writes a lifecycle-and-license record in which
// subject has had a proprietary license since 2020, which makes the repository
// confidential, and its releasable-name identity, which keys the repository's
// entry in the confidential-name index.
func writeProprietaryRecord(t *testing.T, dir, subject string) {
	t.Helper()
	writeRecord(t, dir, subject, "proprietary", true)
}

// writeRecord writes a lifecycle-and-license record licensing subject under
// license since 2020, with subject's releasable-name identity when named is
// set.
func writeRecord(t *testing.T, dir, subject, license string, named bool) {
	t.Helper()
	record := "format_version = 1\n\n[[licenses]]\nsubject = \"" + subject + "\"\nlicense = \"" + license + "\"\nfrom = 2020-01-01\nreason = \"the " + subject + " license\"\n"
	if named {
		record += "\n[[identities]]\nsubject = \"" + subject + "\"\nfacet = \"releasable-name\"\nvalue = \"" + subject + "\"\nregistry = \"\"\ntag_patterns = [\"v*\"]\nfrom = 2020-01-01\nreason = \"its name\"\n"
	}
	testutil.WriteFile(t, dir, ".strictmetadata/lifecycle-and-license/manifest.toml", "owner = \"strictspec\"\n")
	testutil.WriteFile(t, dir, ".strictmetadata/lifecycle-and-license/lifecycle-and-license.toml", record)
}

// otherConfidentialRepository is the index entry of a confidential repository
// other than the one a test commits in.
var otherConfidentialRepository = map[string][]string{"portal": {"portal"}}

func TestCommitInARepositoryWithoutARecordRefusesAConfidentialLine(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)
	testutil.Git(t, dir, "remote", "add", "origin", "https://git.invalid/example/public-site.git")
	before := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "notes.txt", "line one\nthe Portal launches\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add notes", "--", "notes.txt")
	if code == 0 {
		t.Fatalf("a commit naming a confidential term in a public repository was accepted")
	}
	if !strings.Contains(stderr, "notes.txt, line 2, column 5: portal") {
		t.Errorf("the refusal does not name the file, line, column, and term:\n%s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != before {
		t.Errorf("a refused commit moved HEAD from %s to %s", before, got)
	}

	// The refusal's fix: remove the term, and the same commit goes through.
	testutil.WriteFile(t, dir, "notes.txt", "line one\nthe site launches\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("the commit was refused after the term was removed (code %d): %s", code, stderr)
	}
}

func TestCommitMessageNamingAConfidentialTermIsRefused(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)

	testutil.WriteFile(t, dir, "notes.txt", "harmless\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "-m", "about the portal", "--", "notes.txt")
	if code == 0 {
		t.Fatalf("a commit message naming a confidential term was accepted")
	}
	if !strings.Contains(stderr, "the commit message, line 3, column 11: portal") {
		t.Errorf("the refusal does not name the message line and term:\n%s", stderr)
	}
}

func TestCommitOfANewPathNamingAConfidentialTermIsRefused(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)

	testutil.WriteFile(t, dir, "docs/portal.md", "harmless\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add a page", "--", "docs/portal.md")
	if code == 0 {
		t.Fatalf("a new path naming a confidential term was accepted")
	}
	if !strings.Contains(stderr, "docs/portal.md: the new path, column 6: portal") {
		t.Errorf("the refusal does not name the path and term:\n%s", stderr)
	}
}

func TestCommitScansOnlyAddedAndChangedLines(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)

	// A line committed before the term was confidential stays where it is; the
	// commit that adds an unrelated line does not answer for it.
	testutil.WriteFile(t, dir, "history.txt", "the portal was announced\n")
	testutil.Git(t, dir, "add", "history.txt")
	testutil.Git(t, dir, "commit", "-m", "history")
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)

	testutil.WriteFile(t, dir, "history.txt", "the portal was announced\nand then nothing happened\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "more history", "--", "history.txt"); code != 0 {
		t.Fatalf("a commit adding only an unrelated line was refused (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "history.txt", "the portal was announced\nand then nothing happened\nthe portal closed\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "more history", "--", "history.txt")
	if code == 0 {
		t.Fatalf("a commit adding a line naming a confidential term was accepted")
	}
	if !strings.Contains(stderr, "history.txt, line 3, column 5: portal") {
		t.Errorf("the refusal does not name the added line:\n%s", stderr)
	}
	if strings.Contains(stderr, "history.txt, line 1,") {
		t.Errorf("the refusal names a line the commit did not add:\n%s", stderr)
	}
}

func TestAmendAndRewordAreScanned(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)

	testutil.WriteFile(t, dir, "notes.txt", "harmless\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("the clean commit failed (code %d): %s", code, stderr)
	}
	tip := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "notes.txt", "harmless\nportal\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "--amend", "--", "notes.txt")
	if code == 0 {
		t.Fatalf("an amend adding a confidential term was accepted")
	}
	if !strings.Contains(stderr, "notes.txt, line 2, column 1: portal") {
		t.Errorf("the amend's refusal does not name the line:\n%s", stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "commit", "--amend", "-m", "portal notes")
	if code == 0 {
		t.Fatalf("a reword naming a confidential term was accepted")
	}
	if !strings.Contains(stderr, "the commit message, line 1, column 1: portal") {
		t.Errorf("the reword's refusal does not name the message:\n%s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tip {
		t.Errorf("a refused amend or reword moved HEAD from %s to %s", tip, got)
	}
}

func TestConfidentialRepositoryIsNotScannedAndRecordsItsNames(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeConfidentialIndex(t, indexPath, otherConfidentialRepository)
	writeProprietaryRecord(t, dir, "widget")
	// A host that never resolves: confidentiality is read from the record's
	// license periods, so nothing may ask the network, and a commit that
	// tried would fail.
	testutil.Git(t, dir, "remote", "add", "origin", "https://git.invalid/example/gadget-works.git")

	testutil.WriteFile(t, dir, "notes.txt", "the widget talks to the portal\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "widget notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("a commit in a confidential repository was refused (code %d): %s", code, stderr)
	}

	got := readConfidentialIndex(t, indexPath)
	if !strings.Contains(got, `subjects = ["widget"]`) {
		t.Fatalf("the index has no entry for the confidential repository:\n%s", got)
	}
	for _, name := range []string{`"widget"`, `"gadget-works"`, `"portal"`} {
		if !strings.Contains(got, name) {
			t.Errorf("the index does not hold %s:\n%s", name, got)
		}
	}
}

// The index keys a repository by its record's releasable-name identities, so
// a confidential repository without an origin remote records its names.
func TestConfidentialRepositoryWithoutAnOriginRecordsItsNames(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeProprietaryRecord(t, dir, "widget")

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("a confidential repository with no origin remote was refused (code %d): %s", code, stderr)
	}
	got := readConfidentialIndex(t, indexPath)
	if !strings.Contains(got, `subjects = ["widget"]`) || !strings.Contains(got, `"widget"`) || !strings.Contains(got, `"`+filepath.Base(dir)+`"`) {
		t.Errorf("the commit did not record the repository's names under its releasable name:\n%s", got)
	}
}

func TestConfidentialRepositoryWithoutAReleasableNameIsRefused(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeRecord(t, dir, "widget", "proprietary", false)

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "--", "notes.txt")
	if code == 0 {
		t.Fatalf("a confidential repository with no releasable-name identity committed without recording its names")
	}
	if !strings.Contains(stderr, "rlsbl transition identity --facet releasable-name") {
		t.Errorf("the refusal does not name the fix:\n%s", stderr)
	}

	// The refusal's fix: record the releasable-name identity, and the commit
	// goes through.
	writeProprietaryRecord(t, dir, "widget")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("the commit was refused after the identity was recorded (code %d): %s", code, stderr)
	}
	if !strings.Contains(readConfidentialIndex(t, indexPath), `"widget"`) {
		t.Errorf("the commit did not record the repository's names")
	}
}

func TestPublicRepositoryRemovesItsOwnIndexEntry(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeRecord(t, dir, "widget", "MIT", true)
	writeConfidentialIndex(t, indexPath, map[string][]string{
		"widget": {"widget"},
		"portal": {"portal"},
	})

	// The record licenses widget publicly, so the repository's old entry goes,
	// and the names it held no longer refuse anything here.
	testutil.WriteFile(t, dir, "notes.txt", "the widget is public now\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("the commit was refused (code %d): %s", code, stderr)
	}
	got := readConfidentialIndex(t, indexPath)
	if strings.Contains(got, `"widget"`) {
		t.Errorf("the public repository's entry was not removed:\n%s", got)
	}
	if !strings.Contains(got, `"portal"`) {
		t.Errorf("another repository's entry was removed:\n%s", got)
	}
}

func TestDryRunCommitLeavesTheIndexUnwritten(t *testing.T) {
	dir := newRepo(t)
	env, indexPath := confidentialIndexEnv(t)
	writeProprietaryRecord(t, dir, "widget")
	testutil.Git(t, dir, "remote", "add", "origin", "https://git.invalid/example/gadget-works.git")

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "--dry-run", "commit", "-m", "notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("the dry run failed (code %d): %s", code, stderr)
	}
	if got := readConfidentialIndex(t, indexPath); got != "" {
		t.Errorf("a dry run wrote the index:\n%s", got)
	}
}
