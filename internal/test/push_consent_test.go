package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `push --force-with-lease` overwrites remote refs, which is a deliberate act;
// an ordinary push publishes and overwrites nothing, which is not. So push is
// consequential CONDITIONALLY -- on one flag the caller typed -- and the
// confirmation lives at safegit's own confirmDeliberate seam rather than at the
// framework's command-granularity confirm protocol, which cannot express a
// condition. These tests pin the four halves of that: a terminal is asked,
// --approve-consequential answers in advance, --json answers nothing, and an
// unforced push is never asked at all.

// TestPushForceWithLeaseJSONDoesNotConfirm: machine mode produces
// machine-readable output and says nothing about consent, so it must not
// answer the force-push confirmation. An explicit --approve-consequential does.
func TestPushForceWithLeaseJSONDoesNotConfirm(t *testing.T) {
	dir, remoteDir, remoteBase := divergedFromRemote(t)
	want := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "--force-with-lease", "origin")
	assertRefusedForConsent(t, code, stderr)
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got == want {
		t.Error("--json must not answer the force-push confirmation; the remote ref was overwritten")
	}

	_, stderr, code = runSafegit(t, dir, "--json", "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("an explicit --approve-consequential must run the force-push, got code %d: %s", code, stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != want {
		t.Errorf("the consented force-push did not move the remote ref: it is %s, want %s (was %s)", got, want, remoteBase)
	}
}

// TestPushForceWithLeaseDeclinedExitsNonzero: with no terminal to answer at,
// the prompt reads EOF and declines -- and a declined force-push is a refusal,
// not a success. It also refuses BEFORE any network contact, so a caller who
// says no never reaches the remote at all.
func TestPushForceWithLeaseDeclinedExitsNonzero(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	_, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "--force-with-lease", "cloudy")
	if code != exitcode.General {
		t.Errorf("a declined force-push must exit %d (General), got %d; stderr: %s", exitcode.General, code, stderr)
	}
	if strings.Contains(stderr, "Could not resolve host") {
		t.Errorf("the refusal must precede any network contact, got: %s", stderr)
	}
}

// TestDeclinedForcePushSaysSoOnTheOrdinaryOutputChannel: the question goes to
// stderr, but the ANSWER safegit acts on is ordinary human output and goes
// through the one mechanism every other command's "Aborted." goes through --
// the same one `doctor --action uninstall` and a declined backup use. A bare
// write to stdout would say it in a channel --quiet cannot reach and machine
// mode cannot keep out of its envelope.
func TestDeclinedForcePushSaysSoOnTheOrdinaryOutputChannel(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	stdout, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "--force-with-lease", "cloudy")
	if code != exitcode.General {
		t.Fatalf("a declined force-push must exit %d (General), got %d; stdout=%s stderr=%s",
			exitcode.General, code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Aborted.") {
		t.Errorf("a declined force-push does not say it aborted; stdout=%s stderr=%s", stdout, stderr)
	}

	stdout, _, code = runSafegit(t, dir, "--quiet", "push", "--refs", "head", "--force-with-lease", "cloudy")
	if code != exitcode.General {
		t.Fatalf("a declined force-push must exit %d (General) under --quiet too, got %d", exitcode.General, code)
	}
	if strings.Contains(stdout, "Aborted.") {
		t.Errorf("--quiet did not suppress the abort notice, so it is not going through the shared output mechanism: %s", stdout)
	}
}

// TestPushWithoutForcePromptsNothing: the confirmation is conditional, so an
// ordinary push must run straight through -- no prompt, no
// --approve-consequential, no mention of forcing anywhere in its output.
func TestPushWithoutForcePromptsNothing(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")

	stdout, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("an unforced push must not need consent (code %d): %s", code, stderr)
	}
	out := stdout + stderr
	for _, forbidden := range []string{"[y/N]", "Force-push", "--approve-consequential", "refusing"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an unforced push asked for consent (%q appeared):\n%s", forbidden, out)
		}
	}
	if got, want := testutil.Rev(t, remoteDir, "refs/heads/main"), testutil.Rev(t, dir, "HEAD"); got != want {
		t.Errorf("remote main is %s, want %s", got, want)
	}
}

// TestPushForceDryRunPreviewsWithoutConsent: a preview overwrites nothing, so
// it has nothing to ask consent for -- with or without --json.
func TestPushForceDryRunPreviewsWithoutConsent(t *testing.T) {
	dir, remoteDir, _ := divergedFromRemote(t)
	remoteBefore := testutil.Rev(t, remoteDir, "refs/heads/main")

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("a --json dry run must preview without consent, got code %d: %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if !env.DryRun {
		t.Errorf("expected a dry-run envelope on stdout, got: %s", stdout)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != remoteBefore {
		t.Errorf("a dry run pushed for real: remote main is %s, want %s", got, remoteBefore)
	}
}
