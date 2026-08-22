package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var uninstallEnv = []string{"CLAUDE_CODE_SESSION_ID=doctor-uninstall-test"}

// Uninstall is a REPOSITORY-wide operation, not a per-checkout one.
//
// A repository with linked worktrees keeps safegit state in as many places as
// it has worktrees: one state directory per worktree git dir, plus the shared
// store under the common git dir that holds the locks and the hooks every push
// runs. An uninstall that removed only the invoking worktree's directory left
// the rest behind while printing "safegit uninstalled", which is the worst of
// both answers: the operator believes the tool is gone and its state is still
// there, config, oplog and all.
//
// So uninstall takes every one of them, and says which ones it is taking
// BEFORE it takes them -- including, in as many words, the ones that belong to
// a worktree the operator is not standing in.

// TestUninstallFromLinkedWorktreeTakesEveryWorktreesState pins the scope: run
// from a linked worktree, the main worktree's own state directory goes too.
func TestUninstallFromLinkedWorktreeTakesEveryWorktreesState(t *testing.T) {
	dir := newRepo(t)

	// Give the main worktree state of its own.
	if _, stderr, code := runSafegitEnv(t, dir, uninstallEnv, "config", "set", "commit.casMaxAttempts", "7"); code != 0 {
		t.Fatalf("initializing the main worktree failed (%d): %s", code, stderr)
	}
	mainState := filepath.Join(dir, ".git", "safegit")
	if _, err := os.Stat(filepath.Join(mainState, "config.json")); err != nil {
		t.Fatalf("precondition: the main worktree has no state: %v", err)
	}

	wt := addLinkedWorktree(t, dir, "side")
	if _, stderr, code := runSafegitEnv(t, wt, uninstallEnv, "config", "set", "commit.casMaxAttempts", "9"); code != 0 {
		t.Fatalf("initializing the linked worktree failed (%d): %s", code, stderr)
	}
	linkedState := filepath.Join(dir, ".git", "worktrees", filepath.Base(wt), "safegit")
	if _, err := os.Stat(filepath.Join(linkedState, "config.json")); err != nil {
		t.Fatalf("precondition: the linked worktree has no state of its own: %v", err)
	}

	if _, stderr, code := runSafegitEnv(t, wt, uninstallEnv,
		"doctor", "--action", "uninstall", "--approve-consequential"); code != 0 {
		t.Fatalf("uninstall from the linked worktree failed (%d): %s", code, stderr)
	}

	if _, err := os.Stat(linkedState); !os.IsNotExist(err) {
		t.Errorf("uninstall left the invoking worktree's state %s behind (err=%v)", linkedState, err)
	}
	if _, err := os.Stat(mainState); !os.IsNotExist(err) {
		t.Errorf("uninstall left the MAIN worktree's state %s behind; uninstall is repository-wide (err=%v)", mainState, err)
	}
}

// TestUninstallEnumeratesWhatItRemoves pins that the operator is told which
// directories are going, one per line, and that the ones belonging to another
// worktree are called out as such.
func TestUninstallEnumeratesWhatItRemoves(t *testing.T) {
	dir := newRepo(t)
	if _, stderr, code := runSafegitEnv(t, dir, uninstallEnv, "config", "set", "commit.casMaxAttempts", "7"); code != 0 {
		t.Fatalf("initializing the main worktree failed (%d): %s", code, stderr)
	}
	wt := addLinkedWorktree(t, dir, "side")
	if _, stderr, code := runSafegitEnv(t, wt, uninstallEnv, "config", "set", "commit.casMaxAttempts", "9"); code != 0 {
		t.Fatalf("initializing the linked worktree failed (%d): %s", code, stderr)
	}

	mainState := filepath.Join(dir, ".git", "safegit")
	linkedState := filepath.Join(dir, ".git", "worktrees", filepath.Base(wt), "safegit")

	stdout, stderr, code := runSafegitEnv(t, wt, uninstallEnv,
		"doctor", "--action", "uninstall", "--approve-consequential")
	if code != 0 {
		t.Fatalf("uninstall failed (%d): %s", code, stderr)
	}
	for _, want := range []string{mainState, linkedState} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the uninstall output does not name %s; stdout was:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "worktree") {
		t.Errorf("the uninstall output must flag that other worktrees' state is included; stdout was:\n%s", stdout)
	}
}

// TestUninstallDryRunEnumeratesAndRemovesNothing pins the preview: the same
// enumeration, and every directory still on disk afterwards.
func TestUninstallDryRunEnumeratesAndRemovesNothing(t *testing.T) {
	dir := newRepo(t)
	if _, stderr, code := runSafegitEnv(t, dir, uninstallEnv, "config", "set", "commit.casMaxAttempts", "7"); code != 0 {
		t.Fatalf("initializing the main worktree failed (%d): %s", code, stderr)
	}
	wt := addLinkedWorktree(t, dir, "side")
	if _, stderr, code := runSafegitEnv(t, wt, uninstallEnv, "config", "set", "commit.casMaxAttempts", "9"); code != 0 {
		t.Fatalf("initializing the linked worktree failed (%d): %s", code, stderr)
	}

	mainState := filepath.Join(dir, ".git", "safegit")
	linkedState := filepath.Join(dir, ".git", "worktrees", filepath.Base(wt), "safegit")

	stdout, stderr, code := runSafegitEnv(t, wt, uninstallEnv,
		"--dry-run", "doctor", "--action", "uninstall", "--approve-consequential")
	if code != 0 {
		t.Fatalf("dry-run uninstall failed (%d): %s", code, stderr)
	}
	for _, want := range []string{mainState, linkedState} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the dry run does not enumerate %s; stdout was:\n%s", want, stdout)
		}
	}
	for _, keep := range []string{mainState, linkedState} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("the dry run removed %s (err=%v)", keep, err)
		}
	}
}
