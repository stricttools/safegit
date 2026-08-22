package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A --dry-run push does NOT run the pre-pre-push hooks: a hook is an arbitrary
// operator script, so running one is a mutation a preview may not perform. That
// is the right behaviour and it is also invisible -- a preview whose hooks were
// never asked reads exactly like a preview whose hooks passed. These tests pin
// the three places the skip is stated, one per reader: --help before the run,
// stderr during it, and the payload for a machine consumer that sees neither.

// installPrePrePushHook installs a pre-pre-push hook that touches marker when it
// runs, and returns the marker path.
func installPrePrePushHook(t *testing.T, dir string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	src := filepath.Join(dir, "hooksrc", "pre-pre-push")
	writeHookScript(t, src, "touch "+marker)
	if _, stderr, code := runSafegit(t, dir, "hook", "install", src); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}
	return marker
}

// TestPushDryRunSaysTheHooksAreNotRun: the preview skips the hooks AND says so.
func TestPushDryRunSaysTheHooksAreNotRun(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	marker := installPrePrePushHook(t, dir)

	_, stderr, code := runSafegit(t, dir, "--dry-run", "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("dry-run push failed (code %d): %s", code, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a dry run ran the pre-pre-push hook; a hook is a mutation a preview may not perform")
	}
	if !strings.Contains(stderr, "pre-pre-push hooks are not run under --dry-run") {
		t.Errorf("the preview must state the hook skip; stderr was:\n%s", stderr)
	}

	// The control: the same push, executed, DOES run the hook. Without it the
	// assertion above passes just as well against a hook that never worked.
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("push failed (code %d): %s", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the executed push did not run the hook either (%v); the skip assertion above proves nothing", err)
	}
}

// TestPushDryRunPayloadStatesTheHookSkip: the same fact, for the reader that
// sees no stderr -- a machine consumer reading the envelope.
func TestPushDryRunPayloadStatesTheHookSkip(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	installPrePrePushHook(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("dry-run push --json failed (code %d): %s", code, stderr)
	}
	payload := jsonPayload(t, stdout)
	for _, want := range []string{`"pre_pre_push_hooks_skipped":"dry-run"`, `"pre_pre_push_hooks_run":0`, `"dry_run":true`} {
		if !strings.Contains(payload, want) {
			t.Errorf("the preview payload must carry %s; got:\n%s", want, payload)
		}
	}
}

// TestPushPayloadReportsHooksThatRan: the executed counterpart, so the skip
// member is a real distinction rather than a constant.
func TestPushPayloadReportsHooksThatRan(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	installPrePrePushHook(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("push --json failed (code %d): %s", code, stderr)
	}
	payload := jsonPayload(t, stdout)
	for _, want := range []string{`"pre_pre_push_hooks_skipped":null`, `"pre_pre_push_hooks_run":1`, `"dry_run":false`} {
		if !strings.Contains(payload, want) {
			t.Errorf("the executed push's payload must carry %s; got:\n%s", want, payload)
		}
	}

	// --no-pre-push-hook is the other way none run, and it is a different
	// answer: disabled, not previewed.
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")
	stdout, stderr, code = runSafegit(t, dir, "--json", "push", "--refs", "head", "--no-pre-push-hook", "origin")
	if code != 0 {
		t.Fatalf("push --no-pre-push-hook --json failed (code %d): %s", code, stderr)
	}
	if payload := jsonPayload(t, stdout); !strings.Contains(payload, `"pre_pre_push_hooks_skipped":"disabled"`) {
		t.Errorf("a disabled hook run must say so in the payload; got:\n%s", payload)
	}
}

// TestPushPayloadReportsThePinnedLease: the payload is also where the lease
// safegit pinned becomes machine-visible -- the observed remote SHA and the
// expectation sent with it, including the empty expectation that means "this
// ref must not exist yet".
func TestPushPayloadReportsThePinnedLease(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")

	// First push: the remote has no such ref, so the expectation is empty.
	stdout, stderr, code := runSafegit(t, dir, "--json", "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("force-push --json failed (code %d): %s", code, stderr)
	}
	payload := jsonPayload(t, stdout)
	for _, want := range []string{`"force_with_lease":true`, `"remote_sha":null`, `"lease":""`} {
		if !strings.Contains(payload, want) {
			t.Errorf("the first force-push's payload must carry %s; got:\n%s", want, payload)
		}
	}

	// Second push: the ref exists, so the expectation is the SHA observed.
	observed := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	safegitCommit(t, dir, "b", "b.txt")
	stdout, stderr, code = runSafegit(t, dir, "--json", "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("second force-push --json failed (code %d): %s", code, stderr)
	}
	if payload := jsonPayload(t, stdout); !strings.Contains(payload, `"lease":"`+observed+`"`) {
		t.Errorf("the lease must be pinned to the observed remote SHA %s; got:\n%s", observed, payload)
	}
}

// TestPushHelpStatesTheDryRunHookSkip: --help is where an operator reads it
// before ever running the command.
func TestPushHelpStatesTheDryRunHookSkip(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "push", "--help")
	if code != 0 {
		t.Fatalf("push --help failed (code %d): %s", code, stderr)
	}
	help := stdout + stderr
	for _, want := range []string{"--dry-run", "never run", "pre_pre_push_hooks_skipped"} {
		if !strings.Contains(help, want) {
			t.Errorf("push --help must mention %q so the hook skip is visible before the run; got:\n%s", want, help)
		}
	}
}
