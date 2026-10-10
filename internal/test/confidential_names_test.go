package test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// Commits are never refused for a confidential term: the confidential-term
// list is enforced only at release, by rlsbl, so a false positive never
// interrupts work. safegit reads no term list and writes none, so a leftover
// file from the earlier machine-local name index changes nothing.

// leftoverIndexEnv points a spawned safegit's home and user configuration
// directory at a fresh directory holding a leftover name index that names
// "portal", and returns the environment entries and that configuration
// directory.
func leftoverIndexEnv(t *testing.T) ([]string, string) {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	configDir := xdg
	if runtime.GOOS == "darwin" {
		configDir = filepath.Join(home, "Library", "Application Support")
	}
	index := filepath.Join(configDir, "strictspec", "confidential-names.toml")
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("format_version = 1\n\n[[repositories]]\nsubjects = [\"portal\"]\nnames = [\"portal\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + xdg}, configDir
}

func TestCommitNamingATermIsNeverRefused(t *testing.T) {
	dir := newRepo(t)
	env, _ := leftoverIndexEnv(t)
	testutil.Git(t, dir, "remote", "add", "origin", "https://git.invalid/example/public-site.git")

	testutil.WriteFile(t, dir, "portal-notes.txt", "line one\nthe Portal launches\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "notes", "-m", "about the portal", "--", "portal-notes.txt"); code != 0 {
		t.Fatalf("a commit naming a term was refused (code %d): %s", code, stderr)
	}
	testutil.WriteFile(t, dir, "portal-notes.txt", "line one\nthe Portal launches today\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "--amend", "-m", "more about the portal", "--", "portal-notes.txt"); code != 0 {
		t.Fatalf("an amend naming a term was refused (code %d): %s", code, stderr)
	}
}

func TestCommitInAProprietaryRepositoryWritesNoNameIndex(t *testing.T) {
	dir := newRepo(t)
	env, configDir := leftoverIndexEnv(t)
	index := filepath.Join(configDir, "strictspec", "confidential-names.toml")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	record := "format_version = 1\n\n[[licenses]]\nsubject = \"vault\"\nlicense = \"proprietary\"\nfrom = 2020-01-01\nreason = \"the vault license\"\n" +
		"\n[[identities]]\nsubject = \"vault\"\nfacet = \"releasable-name\"\nvalue = \"vault\"\nregistry = \"\"\ntag_patterns = [\"v*\"]\nfrom = 2020-01-01\nreason = \"its name\"\n"
	testutil.WriteFile(t, dir, ".strictmetadata/lifecycle-and-license/manifest.toml", "owner = \"strictspec\"\n")
	testutil.WriteFile(t, dir, ".strictmetadata/lifecycle-and-license/lifecycle-and-license.toml", record)
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "record", "--", ".strictmetadata"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a commit rewrote the leftover name index:\n%s", after)
	}
	if strings.Contains(string(after), "vault") {
		t.Errorf("a commit recorded the repository's names in the leftover name index")
	}
}
