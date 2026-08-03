package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Destructive confirmations are DELIBERATE: `--json` produces machine-readable
// output but says nothing about consent, so it must never answer "yes" to a
// prompt that destroys history or uninstalls the tool. Only an explicit --yes
// does. These tests pin both halves at every destructive confirmation site.

var confirmEnv = []string{"CLAUDE_CODE_SESSION_ID=confirm-test"}

// assertRefusedForConsent checks the shape of a refusal: the run failed to do
// the thing, and it said which flag would have consented.
func assertRefusedForConsent(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal must name --yes as the flag that consents, got: %s", stderr)
	}
	if !strings.Contains(stderr, "--json does not answer this confirmation") {
		t.Errorf("the refusal must say --json did not answer the prompt, got: %s", stderr)
	}
}

// newSecretRepo builds a repo with a secret in history and clean replacement
// content committed on top, ready for any of the scrub entry points.
func newSecretRepo(t *testing.T) (dir, initialSHA string) {
	t.Helper()
	dir = newRepo(t)
	commitFileEnv(t, dir, confirmEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA = revListReverse(t, dir)[0]
	commitFileEnv(t, dir, confirmEnv, "secret.txt", "REDACTED\n", "commit replacement")
	return dir, initialSHA
}

// secretSurvives reports whether the pre-rewrite secret is still in history.
func secretSurvives(t *testing.T, dir string) bool {
	t.Helper()
	for _, sha := range revListReverse(t, dir) {
		if content, ok := gitShow(t, dir, sha, "secret.txt"); ok && content == "hunter2\n" {
			return true
		}
	}
	return false
}

func TestScrubFileJSONDoesNotConfirm(t *testing.T) {
	dir, initialSHA := newSecretRepo(t)

	_, stderr, _ := runSafegitEnv(t, dir, confirmEnv, "--json", "scrub", "file",
		"--from", initialSHA, "--reason", "json consent probe", "secret.txt")
	assertRefusedForConsent(t, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub confirmation; history was rewritten")
	}

	_, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "explicit consent", "secret.txt")
	if code != 0 {
		t.Fatalf("an explicit --yes must run the scrub, got code %d: %s", code, stderr)
	}
	if secretSurvives(t, dir) {
		t.Error("the consented scrub did not rewrite history")
	}
}

// TestScrubFileDryRunPreviewsWithoutConsent: a preview destroys nothing, so it
// must not ask for consent -- with or without --json.
func TestScrubFileDryRunPreviewsWithoutConsent(t *testing.T) {
	dir, initialSHA := newSecretRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--dry-run", "scrub", "file",
		"--from", initialSHA, "--reason", "preview", "secret.txt")
	if code != 0 {
		t.Fatalf("a --json dry run must preview without consent, got code %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "\"dry_run\": true") {
		t.Errorf("expected the dry-run JSON payload on stdout, got: %s", stdout)
	}
	if !secretSurvives(t, dir) {
		t.Error("a dry run must not rewrite history")
	}
}

func TestScrubMatchJSONDoesNotConfirm(t *testing.T) {
	dir, _ := newSecretRepo(t)

	_, stderr, _ := runSafegitEnv(t, dir, confirmEnv, "--json", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "json consent probe",
		"--entire-history")
	assertRefusedForConsent(t, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub match confirmation; history was rewritten")
	}

	_, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--yes", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "explicit consent",
		"--entire-history")
	if code != 0 {
		t.Fatalf("an explicit --yes must run the scrub match, got code %d: %s", code, stderr)
	}
	if secretSurvives(t, dir) {
		t.Error("the consented scrub match did not rewrite history")
	}
}

func TestScrubRunJSONDoesNotConfirm(t *testing.T) {
	dir, _ := newSecretRepo(t)

	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"hunter2\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegitEnv(t, dir, confirmEnv, "commit", "-m", "add recipe", "--", "recipe.toml"); code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	_, stderr, _ := runSafegitEnv(t, dir, confirmEnv, "--json", "scrub", "run",
		"--reason", "json consent probe", "--entire-history", "recipe.toml")
	assertRefusedForConsent(t, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub run confirmation; history was rewritten")
	}

	_, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--yes", "scrub", "run",
		"--reason", "explicit consent", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("an explicit --yes must run the recipe scrub, got code %d: %s", code, stderr)
	}
	if secretSurvives(t, dir) {
		t.Error("the consented recipe scrub did not rewrite history")
	}
}

func TestAuthorRewriteJSONDoesNotConfirm(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")

	_, stderr, _ := runSafegitEnv(t, dir, confirmEnv, "--json", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	assertRefusedForConsent(t, stderr)
	if names := authorNames(t, dir); !names["Test"] || names["Renamed"] {
		t.Errorf("--json must not answer the author-rewrite confirmation, got authors %v", names)
	}

	_, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--yes", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("an explicit --yes must run the author rewrite, got code %d: %s", code, stderr)
	}
	if names := authorNames(t, dir); names["Test"] || !names["Renamed"] {
		t.Errorf("the consented author rewrite did not run, got authors %v", names)
	}
}

func TestDoctorUninstallJSONDoesNotConfirm(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")
	safegitDir := filepath.Join(dir, ".git", "safegit")

	_, stderr, _ := runSafegitEnv(t, dir, confirmEnv, "--json", "doctor", "--uninstall")
	assertRefusedForConsent(t, stderr)
	if _, err := os.Stat(safegitDir); err != nil {
		t.Errorf("--json must not answer the uninstall confirmation; %s is gone: %v", safegitDir, err)
	}

	if _, stderr, code := runSafegitEnv(t, dir, confirmEnv, "--json", "--yes", "doctor", "--uninstall"); code != 0 {
		t.Fatalf("an explicit --yes must run the uninstall, got code %d: %s", code, stderr)
	}
	if _, err := os.Stat(safegitDir); !os.IsNotExist(err) {
		t.Errorf("the consented uninstall left %s behind (err=%v)", safegitDir, err)
	}
}

// TestScrubFileInSubmoduleJSONDoesNotConfirm covers the second confirmation in
// scrub file: the one that gates rewriting a submodule AND its parent.
func TestScrubFileInSubmoduleJSONDoesNotConfirm(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "SUBCONFIRM_SECRET", "secret.txt")

	subSHAs := revListReverse(t, subDir)
	firstSubCommit := subSHAs[0]

	// Clean replacement content in the submodule, recorded in the parent, so
	// both trees are clean and the rewrite is the only thing left to gate.
	if err := os.WriteFile(filepath.Join(subDir, "secret.txt"), []byte("CLEANED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, subDir, "add", "secret.txt")
	gitCmd(t, subDir, "commit", "-m", "commit replacement")
	gitCmd(t, parentDir, "add", "mysub")
	gitCmd(t, parentDir, "commit", "-m", "update submodule ref")

	parentHeadBefore := revParseHEAD(t, parentDir)
	subHeadBefore := revParseHEAD(t, subDir)

	_, stderr, _ := runSafegitEnv(t, parentDir, submoduleEnv, "--json", "scrub", "file",
		"mysub/secret.txt", "--from", firstSubCommit, "--reason", "json consent probe")
	assertRefusedForConsent(t, stderr)
	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("--json must not answer the submodule scrub confirmation; parent HEAD moved to %s", got)
	}
	if got := revParseHEAD(t, subDir); got != subHeadBefore {
		t.Errorf("--json must not answer the submodule scrub confirmation; submodule HEAD moved to %s", got)
	}

	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "--json", "--yes", "scrub", "file",
		"mysub/secret.txt", "--from", firstSubCommit, "--reason", "explicit consent")
	if code != 0 {
		t.Fatalf("an explicit --yes must run the submodule scrub, got code %d: %s", code, stderr)
	}
	if got := revParseHEAD(t, subDir); got == subHeadBefore {
		t.Error("the consented submodule scrub did not rewrite the submodule history")
	}
}
