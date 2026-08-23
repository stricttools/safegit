package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE FINDING: the two hook stores answer a missing execute bit differently.
// A non-executable hook in the CHECKOUT's store (.safegit/hooks) makes
// `safegit push` refuse with exit 25 and a chmod remedy
// (hooks.TrackedNotExecutableError, raised in internal/hooks/hooks.go
// Discover). A non-executable hook in the LOCAL, tool-owned store
// (.git/safegit/hooks) is SKIPPED, with only a `warning: <path> is not
// executable, skipping` line on stderr, and the push proceeds as if the hook
// were not there. The same accident -- a checkout on a filesystem without
// modes, a patch tool that dropped the bit, an editor that rewrote the file --
// therefore stops an operator's checks silently in one store and loudly in the
// other.
//
// THE RULING: ANY discovered non-executable hook makes the push refuse,
// whichever store it came from. The refusal names the offending file and states
// the chmod fix. A hook is disabled by removing it, never by dropping its mode,
// and that rule is now store-independent. The EXACT exit code is the
// implementation's choice: exit 25's registered meaning may widen to cover both
// stores, or a new code may be registered for the local-store case -- so this
// test asserts a nonzero exit, the file being named, and a chmod mention, and
// deliberately asserts NO specific number.
//
// SANCTIONED REWRITES at implementation time: the existing tests that pin the
// skip-with-warning behavior for the local store are expected to change, and
// changing them is part of implementing this ruling, not a regression --
// internal/test/hook_store_test.go TestHookListRendersStateAndOrigin (its tail
// asserts `hook run` exits 0 and merely warns about a non-executable local
// entry) and internal/hooks/hooks_test.go TestSkipNonExecutable, plus any
// doctor hook-permission assertion in internal/test/doctor_hooks_test.go that
// treats a non-executable local hook as a survivable state.
//
// TestPushRefusesNonExecutableLocalHook is red today: the push succeeds,
// updating the remote, with only the skip warning on stderr.
func TestPushRefusesNonExecutableLocalHook(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// One healthy local hook that leaves evidence when it runs, so the refusal
	// can be shown to precede execution rather than follow it.
	marker := filepath.Join(evalTempDir(t), "healthy-ran.txt")
	appendingHook(t, filepath.Join(localHookDir(dir), "pre-pre-push"), marker, "healthy")

	// The offender: same store, correct name, no execute bit.
	unarmed := filepath.Join(localHookDir(dir), "pre-pre-push.d", "20-unarmed")
	writeHookScript(t, unarmed, "true")
	if err := os.Chmod(unarmed, 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin")

	if code == 0 {
		t.Errorf("push over the non-executable local hook %s succeeded; it must refuse.\nstderr: %s",
			unarmed, stderr)
	}
	if !strings.Contains(stderr, unarmed) {
		t.Errorf("the refusal must name the offending hook %s, got: %s", unarmed, stderr)
	}
	if !strings.Contains(stderr, "chmod") {
		t.Errorf("the refusal must state the chmod remedy, got: %s", stderr)
	}
	if branches := remoteBranches(t, remoteDir); len(branches) != 0 {
		t.Errorf("the refused push reached the remote anyway; remote branches: %v", branches)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the healthy hook ran: discovery must refuse the whole set before executing any of it")
	}
}
