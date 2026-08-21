package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Destructive confirmations are DELIBERATE: `--json` produces machine-readable
// output but says nothing about consent, so it must never answer "yes" to a
// prompt that destroys history or uninstalls the tool. Only an explicit --approve-consequential
// does. These tests pin both halves at every destructive confirmation site.
//
// Since the strictcli effects regime landed, the FIRST gate is the framework's
// own confirm protocol -- but it fires only for commands that declare
// themselves `consequential` (the three scrub rewrites and `author rewrite`).
// safegit's own confirmDeliberate seam sits behind it, and it is the ONLY gate
// for `doctor --uninstall` and the public-remote backup, which are guarded at
// flag/remote granularity rather than command granularity. The property under
// test is unchanged either way -- --json is not --approve-consequential -- so
// these tests assert the refusal and, above all, that nothing was destroyed.

var confirmEnv = []string{"CLAUDE_CODE_SESSION_ID=confirm-test"}

// assertRefusedForConsent checks the shape of a refusal: the run failed to do
// the thing, and it made clear that consent was missing. Three refusals
// qualify: the framework's non-interactive refusal ("a consequential command
// must be confirmed at a terminal"), its declined prompt ("Proceed? [y/N]
// aborted") -- which of those two a spawned process gets depends on whether its
// stdin happens to be a character device -- and safegit's own refusals for the
// conditions the framework cannot see (doctor --uninstall, a public backup
// remote), which name the consent flag themselves.
func assertRefusedForConsent(t *testing.T, code int, stderr string) {
	t.Helper()
	if code == 0 {
		t.Errorf("an unconsented destructive run must not succeed; stderr: %s", stderr)
	}
	refused := strings.Contains(stderr, "must be confirmed at a terminal") ||
		strings.Contains(stderr, "aborted") ||
		strings.Contains(stderr, "--approve-consequential")
	if !refused {
		t.Errorf("the refusal must show that consent was missing, got: %s", stderr)
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

	_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "scrub", "file",
		"--from", initialSHA, "--reason", "json consent probe", "secret.txt")
	assertRefusedForConsent(t, code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub confirmation; history was rewritten")
	}

	_, stderr, code = runSafegitNoConsent(t, dir, confirmEnv, "--json", "--approve-consequential", "scrub", "file",
		"--from", initialSHA, "--reason", "explicit consent", "secret.txt")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the scrub, got code %d: %s", code, stderr)
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
	env := decodeEnvelope(t, stdout)
	if !env.DryRun {
		t.Errorf("expected a dry-run envelope on stdout, got: %s", stdout)
	}
	if !strings.Contains(string(env.Payload), `"dry_run":true`) {
		t.Errorf("expected the preview payload on stdout, got: %s", stdout)
	}
	if !secretSurvives(t, dir) {
		t.Error("a dry run must not rewrite history")
	}
}

func TestScrubMatchJSONDoesNotConfirm(t *testing.T) {
	dir, _ := newSecretRepo(t)

	_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "json consent probe",
		"--entire-history")
	assertRefusedForConsent(t, code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub match confirmation; history was rewritten")
	}

	_, stderr, code = runSafegitNoConsent(t, dir, confirmEnv, "--json", "--approve-consequential", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "explicit consent",
		"--entire-history")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the scrub match, got code %d: %s", code, stderr)
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

	_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "scrub", "run",
		"--reason", "json consent probe", "--entire-history", "recipe.toml")
	assertRefusedForConsent(t, code, stderr)
	if !secretSurvives(t, dir) {
		t.Error("--json must not answer the scrub run confirmation; history was rewritten")
	}

	_, stderr, code = runSafegitNoConsent(t, dir, confirmEnv, "--json", "--approve-consequential", "scrub", "run",
		"--reason", "explicit consent", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the recipe scrub, got code %d: %s", code, stderr)
	}
	if secretSurvives(t, dir) {
		t.Error("the consented recipe scrub did not rewrite history")
	}
}

func TestAuthorRewriteJSONDoesNotConfirm(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")

	_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	assertRefusedForConsent(t, code, stderr)
	if names := authorNames(t, dir); !names["Test"] || names["Renamed"] {
		t.Errorf("--json must not answer the author-rewrite confirmation, got authors %v", names)
	}

	_, stderr, code = runSafegitNoConsent(t, dir, confirmEnv, "--json", "--approve-consequential", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the author rewrite, got code %d: %s", code, stderr)
	}
	if names := authorNames(t, dir); names["Test"] || !names["Renamed"] {
		t.Errorf("the consented author rewrite did not run, got authors %v", names)
	}
}

func TestDoctorUninstallJSONDoesNotConfirm(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")
	safegitDir := filepath.Join(dir, ".git", "safegit")

	_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "doctor", "--action", "uninstall")
	assertRefusedForConsent(t, code, stderr)
	if _, err := os.Stat(safegitDir); err != nil {
		t.Errorf("--json must not answer the uninstall confirmation; %s is gone: %v", safegitDir, err)
	}

	if _, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "--json", "--approve-consequential", "doctor", "--action", "uninstall"); code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the uninstall, got code %d: %s", code, stderr)
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
	testutil.Git(t, subDir, "add", "secret.txt")
	testutil.Git(t, subDir, "commit", "-m", "commit replacement")
	testutil.Git(t, parentDir, "add", "mysub")
	testutil.Git(t, parentDir, "commit", "-m", "update submodule ref")

	parentHeadBefore := revParseHEAD(t, parentDir)
	subHeadBefore := revParseHEAD(t, subDir)

	_, stderr, code := runSafegitNoConsent(t, parentDir, submoduleEnv, "--json", "scrub", "file",
		"mysub/secret.txt", "--from", firstSubCommit, "--reason", "json consent probe")
	assertRefusedForConsent(t, code, stderr)
	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("--json must not answer the submodule scrub confirmation; parent HEAD moved to %s", got)
	}
	if got := revParseHEAD(t, subDir); got != subHeadBefore {
		t.Errorf("--json must not answer the submodule scrub confirmation; submodule HEAD moved to %s", got)
	}

	_, stderr, code = runSafegitNoConsent(t, parentDir, submoduleEnv, "--json", "--approve-consequential", "scrub", "file",
		"mysub/secret.txt", "--from", firstSubCommit, "--reason", "explicit consent")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the submodule scrub, got code %d: %s", code, stderr)
	}
	if got := revParseHEAD(t, subDir); got == subHeadBefore {
		t.Error("the consented submodule scrub did not rewrite the submodule history")
	}
}

// TestDeclinedDeliberateConfirmationExitsNonzero pins the exit code of a
// refusal at safegit's OWN confirmDeliberate seam -- the sites the framework's
// confirm protocol does not cover, because the command they guard is not
// consequential at command granularity (`doctor --uninstall` guards one flag;
// `backup backup` guards one remote classification).
//
// A declined confirmation is a refusal, not a success. While the framework
// prompted for every `mutating` command its exit-1 refusal masked this: the
// handler was never reached. Now the seam is the only gate at these two sites,
// so a zero exit here would tell a script or agent that the uninstall (or the
// backup) succeeded when nothing happened.
func TestDeclinedDeliberateConfirmationExitsNonzero(t *testing.T) {
	t.Run("doctor uninstall", func(t *testing.T) {
		dir := newRepo(t)
		commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")
		safegitDir := filepath.Join(dir, ".git", "safegit")

		// Not a TTY, so the prompt reads EOF and declines.
		_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "doctor", "--action", "uninstall")
		if code == 0 {
			t.Errorf("a declined uninstall must exit nonzero, got 0; stderr: %s", stderr)
		}
		if _, err := os.Stat(safegitDir); err != nil {
			t.Errorf("the declined uninstall removed %s anyway: %v", safegitDir, err)
		}
	})

	t.Run("backup to unclassifiable remote", func(t *testing.T) {
		dir := newRepo(t)
		commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")
		testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

		_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv, "backup", "backup", "cloudy")
		if code == 0 {
			t.Errorf("a declined backup must exit nonzero, got 0; stderr: %s", stderr)
		}
		if strings.Contains(stderr, "Could not resolve host") {
			t.Errorf("the refusal must precede any network contact, got: %s", stderr)
		}
	})
}
