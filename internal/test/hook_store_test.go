package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The hook subsystem after the store move: safegit's live hooks are its own,
// under .git/safegit/hooks, and a repository may additionally COMMIT hooks to
// .safegit/hooks so that everyone who clones it runs the same checks.
//
// These tests cover the parts of that arrangement that are observable from the
// command line: migration off the old location, the refusal that follows a
// migration nobody ran, the two stores' execution order, the refusals around a
// committed hook, and what an uninstall is and is not allowed to remove.

// localHookDir is safegit's tool-owned live hook store.
func localHookDir(repoDir string) string {
	return filepath.Join(repoDir, ".git", "safegit", "hooks")
}

// trackedHookDir is the committed hook store, which lives in the work tree.
func trackedHookDir(repoDir string) string {
	return filepath.Join(repoDir, ".safegit", "hooks")
}

// legacyHookDir is git's own hook directory, where safegit used to keep its
// pre-pre-push hooks.
func legacyHookDir(repoDir string) string {
	return filepath.Join(repoDir, ".git", "hooks")
}

// appendingHook writes an executable hook that appends label plus a newline to
// an absolute marker file, which is how the ordering assertions read back the
// sequence hooks ran in.
func appendingHook(t *testing.T, path, marker, label string) {
	t.Helper()
	writeHookScript(t, path, "printf '"+label+"\\n' >> "+marker)
}

// markerLines reads a marker file written by appendingHook.
func markerLines(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}

// TestHookMigrateMovesBothSafegitOwnedNames: `hook migrate` relocates the
// `pre-pre-push` file and the `pre-pre-push.d` directory -- the only two names
// safegit ever wrote into git's own hook directory -- and the hooks run from
// their new home afterwards.
func TestHookMigrateMovesBothSafegitOwnedNames(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(dir, "ran.txt")

	appendingHook(t, filepath.Join(legacyHookDir(dir), "pre-pre-push"), marker, "single")
	appendingHook(t, filepath.Join(legacyHookDir(dir), "pre-pre-push.d", "20-nested"), marker, "nested")

	stdout, stderr, code := runSafegit(t, dir, "hook", "migrate")
	if code != 0 {
		t.Fatalf("hook migrate failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "migrated") {
		t.Errorf("migrate said nothing about what it moved: %q", stdout)
	}

	for _, rel := range []string{"pre-pre-push", filepath.Join("pre-pre-push.d", "20-nested")} {
		if _, err := os.Stat(filepath.Join(localHookDir(dir), rel)); err != nil {
			t.Errorf("%s did not arrive in the tool-owned store: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(legacyHookDir(dir), rel)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the legacy location (err=%v)", rel, err)
		}
	}

	if _, stderr, code := runSafegit(t, dir, "hook", "run"); code != 0 {
		t.Fatalf("hook run after migrate failed (%d): %s", code, stderr)
	}
	if got := markerLines(t, marker); len(got) != 2 {
		t.Errorf("migrated hooks did not both run: %v", got)
	}
}

// TestHookMigrateWithNothingToMoveSucceeds: a repository that has nothing in
// the legacy location is already migrated. That is a success with an
// explanation, not an error -- an agent running migrate defensively must not
// have to distinguish "already done" from "failed".
func TestHookMigrateWithNothingToMoveSucceeds(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "hook", "migrate")
	if code != 0 {
		t.Fatalf("hook migrate on a clean repo failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to migrate") {
		t.Errorf("migrate must say there was nothing to move, got: %q", stdout)
	}
}

// TestHookMigrateDryRunMovesNothing: the relocation is minted through the
// effects handle, so a preview records the moves and performs none of them.
func TestHookMigrateDryRunMovesNothing(t *testing.T) {
	dir := newRepo(t)
	legacy := filepath.Join(legacyHookDir(dir), "pre-pre-push")
	writeHookScript(t, legacy, "true")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "hook", "migrate")
	if code != 0 {
		t.Fatalf("dry-run migrate failed (%d): %s", code, stderr)
	}
	if log := wouldDoLog(stdout); !strings.Contains(log, "rename:") {
		t.Errorf("the would-do log must record the move, got: %s", log)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("a dry run moved the hook for real: %v", err)
	}
}

// TestHookDiscoveryRefusesUnmigratedHooks: once the store has moved, a hook
// left behind in git's own directory is a hard error naming `hook migrate` --
// not a silent skip that would stop an operator's checks without saying so, and
// not a second store to run from.
func TestHookDiscoveryRefusesUnmigratedHooks(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(dir, "ran.txt")
	appendingHook(t, filepath.Join(legacyHookDir(dir), "pre-pre-push"), marker, "legacy")

	_, stderr, code := runSafegit(t, dir, "hook", "run")
	if code != 24 {
		t.Errorf("hook run over an unmigrated hook exited %d, want 24: %s", code, stderr)
	}
	if !strings.Contains(stderr, "hook migrate") {
		t.Errorf("the refusal must name the remedy, got: %s", stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the refusal ran the hook anyway")
	}

	// `hook list` shows what is there before refusing -- the operator needs to
	// see the files the message is about.
	stdout, _, listCode := runSafegit(t, dir, "hook", "list")
	if listCode != 24 {
		t.Errorf("hook list over an unmigrated hook exited %d, want 24", listCode)
	}
	if !strings.Contains(stdout, "legacy") {
		t.Errorf("hook list must show the legacy entry and its origin, got: %q", stdout)
	}

	if _, stderr, code := runSafegit(t, dir, "hook", "migrate"); code != 0 {
		t.Fatalf("hook migrate failed (%d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "hook", "run"); code != 0 {
		t.Fatalf("hook run after migrate failed (%d): %s", code, stderr)
	}
	if got := markerLines(t, marker); len(got) != 1 {
		t.Errorf("the migrated hook did not run: %v", got)
	}
}

// TestCommittedHooksRunBeforeLocalOnes: both stores are read, a name present in
// both runs TWICE -- there is no precedence rule that silently drops one -- and
// the committed store goes first.
func TestCommittedHooksRunBeforeLocalOnes(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(dir, "order.txt")

	appendingHook(t, filepath.Join(trackedHookDir(dir), "pre-pre-push"), marker, "tracked-single")
	appendingHook(t, filepath.Join(trackedHookDir(dir), "pre-pre-push.d", "10-tracked"), marker, "tracked-nested")
	safegitCommit(t, dir, "add committed hooks",
		filepath.Join(".safegit", "hooks", "pre-pre-push"),
		filepath.Join(".safegit", "hooks", "pre-pre-push.d", "10-tracked"))

	appendingHook(t, filepath.Join(localHookDir(dir), "pre-pre-push"), marker, "local-single")

	if _, stderr, code := runSafegit(t, dir, "hook", "run"); code != 0 {
		t.Fatalf("hook run failed (%d): %s", code, stderr)
	}
	got := markerLines(t, marker)
	want := []string{"tracked-single", "tracked-nested", "local-single"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hooks ran as %v, want %v", got, want)
	}
}

// TestNonExecutableCommittedHookRefusesPush: a committed hook is disabled by
// committing its DELETION. A mode-based disabling would make an accidentally
// lost executable bit -- a checkout that dropped it, a patch tool that ignored
// it -- silently stop the repository's checks, so it is a refusal with its own
// exit code instead.
func TestNonExecutableCommittedHookRefusesPush(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	hookPath := filepath.Join(trackedHookDir(dir), "pre-pre-push")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	safegitCommit(t, dir, "add a committed hook without its mode", filepath.Join(".safegit", "hooks", "pre-pre-push"))

	_, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin")
	if code != 25 {
		t.Fatalf("push over a non-executable committed hook exited %d, want 25: %s", code, stderr)
	}
	if !strings.Contains(stderr, "chmod +x") || !strings.Contains(stderr, "COMMIT") {
		t.Errorf("the refusal must state the chmod-and-commit remedy, got: %s", stderr)
	}
}

// TestHookInstallRefusesExistingDestination: install never overwrites. Upgrading
// a hook is a remove followed by an install, which says out loud that the old
// script is going away.
func TestHookInstallRefusesExistingDestination(t *testing.T) {
	dir := newRepo(t)

	src := filepath.Join(dir, "hooksrc", "pre-pre-push")
	writeHookScript(t, src, "true")
	if _, stderr, code := runSafegit(t, dir, "hook", "install", src); code != 0 {
		t.Fatalf("hook install failed (%d): %s", code, stderr)
	}
	installed := filepath.Join(localHookDir(dir), "pre-pre-push")
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}

	replacement := filepath.Join(dir, "hooksrc2", "pre-pre-push")
	writeHookScript(t, replacement, "echo replaced")
	_, stderr, code := runSafegit(t, dir, "hook", "install", replacement)
	if code == 0 {
		t.Fatal("the second install reported success instead of refusing")
	}
	if !strings.Contains(stderr, "hook remove") {
		t.Errorf("the refusal must name the way to upgrade, got: %s", stderr)
	}
	after, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the refused install overwrote the hook:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestUninstallRemovesLiveAndLegacyHooksButNotCommittedOnes: uninstall takes
// everything that is safegit's -- the live store with the rest of
// .git/safegit, and the legacy names it may have written into git's own
// directory -- and nothing that is the repository's.
func TestUninstallRemovesLiveAndLegacyHooksButNotCommittedOnes(t *testing.T) {
	dir := newRepo(t)

	src := filepath.Join(dir, "hooksrc", "pre-pre-push")
	writeHookScript(t, src, "true")
	if _, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", src); code != 0 {
		t.Fatalf("hook install failed (%d): %s", code, stderr)
	}
	legacy := filepath.Join(legacyHookDir(dir), "pre-pre-push.d", "20-old")
	writeHookScript(t, legacy, "true")

	committed := filepath.Join(trackedHookDir(dir), "pre-pre-push")
	writeHookScript(t, committed, "true")
	safegitCommitEnv(t, dir, hookSafetyEnv, "add a committed hook", filepath.Join(".safegit", "hooks", "pre-pre-push"))
	committedBefore, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv,
		"doctor", "--action", "uninstall", "--approve-consequential"); code != 0 {
		t.Fatalf("doctor --action uninstall failed (%d): %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git", "safegit")); !os.IsNotExist(err) {
		t.Errorf("uninstall left .git/safegit behind (err=%v)", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("uninstall left the legacy-location hook %s behind (err=%v)", legacy, err)
	}
	after, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("uninstall removed the COMMITTED hook %s: %v", committed, err)
	}
	if string(after) != string(committedBefore) {
		t.Error("uninstall modified the committed hook")
	}
}

// TestHookRemoveDeletesInstalledHooks: remove-by-name over the tool-owned store,
// addressed either by the store-relative path or by the base name alone.
func TestHookRemoveDeletesInstalledHooks(t *testing.T) {
	dir := newRepo(t)

	top := filepath.Join(localHookDir(dir), "pre-pre-push")
	nested := filepath.Join(localHookDir(dir), "pre-pre-push.d", "20-lint")
	writeHookScript(t, top, "true")
	writeHookScript(t, nested, "true")

	if _, stderr, code := runSafegit(t, dir, "hook", "remove", "pre-pre-push"); code != 0 {
		t.Fatalf("hook remove failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(top); !os.IsNotExist(err) {
		t.Errorf("hook remove left %s in place (err=%v)", top, err)
	}

	// The base name alone reaches an entry inside the .d directory.
	if _, stderr, code := runSafegit(t, dir, "hook", "remove", "20-lint"); code != 0 {
		t.Fatalf("hook remove of a nested entry failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Errorf("hook remove left %s in place (err=%v)", nested, err)
	}
}

// TestHookRemoveUnknownNameFails: removing something that is not there is a
// refusal, not a silent success -- an agent that misspelled a name must not read
// "removed" back.
func TestHookRemoveUnknownNameFails(t *testing.T) {
	dir := newRepo(t)

	_, stderr, code := runSafegit(t, dir, "hook", "remove", "not-installed")
	if code == 0 {
		t.Fatal("removing an absent hook reported success")
	}
	if !strings.Contains(stderr, "hook list") {
		t.Errorf("the refusal should point at the listing, got: %s", stderr)
	}
}

// TestHookRemoveRefusesCommittedHook: a committed hook is repository content.
// Removing it means committing the deletion, which is a different act with a
// different audience, so `hook remove` explains rather than deletes.
func TestHookRemoveRefusesCommittedHook(t *testing.T) {
	dir := newRepo(t)

	committed := filepath.Join(trackedHookDir(dir), "pre-pre-push")
	writeHookScript(t, committed, "true")
	safegitCommit(t, dir, "add a committed hook", filepath.Join(".safegit", "hooks", "pre-pre-push"))

	_, stderr, code := runSafegit(t, dir, "hook", "remove", "pre-pre-push")
	if code == 0 {
		t.Fatal("hook remove deleted a committed hook instead of refusing")
	}
	if !strings.Contains(stderr, "committing the deletion") {
		t.Errorf("the refusal must explain what removing a committed hook means, got: %s", stderr)
	}
	if _, err := os.Stat(committed); err != nil {
		t.Errorf("the refused removal deleted the file anyway: %v", err)
	}
}

// TestHookRemoveDryRunRemovesNothing: the removal is minted through the effects
// handle, so a preview records it and deletes nothing.
func TestHookRemoveDryRunRemovesNothing(t *testing.T) {
	dir := newRepo(t)

	installed := filepath.Join(localHookDir(dir), "pre-pre-push")
	writeHookScript(t, installed, "true")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "hook", "remove", "pre-pre-push")
	if code != 0 {
		t.Fatalf("dry-run remove failed (%d): %s", code, stderr)
	}
	if log := wouldDoLog(stdout); !strings.Contains(log, "remove:") {
		t.Errorf("the would-do log must record the removal, got: %s", log)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Errorf("a dry run removed the hook for real: %v", err)
	}
}

// TestSubmodulePushRunsParentHooksAfterMigration: the parent's hooks still
// cascade into a submodule's push once the parent has been migrated. The
// cascade carries work-tree/git-dir pairs, so it finds the parent's hooks
// wherever the parent keeps them -- and refuses while the parent has not been
// migrated, exactly as a push in the parent itself would.
func TestSubmodulePushRunsParentHooksAfterMigration(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	bareRemote := filepath.Join(evalTempDir(t), "sub-bare")
	runGitIn(t, "", "init", "--bare", "--initial-branch=main", bareRemote)
	runGitIn(t, subDir, "remote", "set-url", "origin", bareRemote)
	runGitIn(t, subDir, "push", "origin", "main")

	// The parent's hook is where safegit USED to keep it.
	marker := filepath.Join(evalTempDir(t), "cascade-marker.txt")
	appendingHook(t, filepath.Join(legacyHookDir(parentDir), "pre-pre-push"), marker, "parent")

	testutil.WriteFile(t, subDir, "push-test.txt", "push test\n")
	safegitCommit(t, subDir, "push test commit", "push-test.txt")

	_, stderr, code := runSafegit(t, subDir, "push", "--refs", "head", "origin")
	if code != 24 {
		t.Fatalf("a push cascading off an unmigrated parent exited %d, want 24: %s", code, stderr)
	}

	if _, stderr, code := runSafegit(t, parentDir, "hook", "migrate"); code != 0 {
		t.Fatalf("migrating the parent failed (%d): %s", code, stderr)
	}

	if _, stderr, code := runSafegit(t, subDir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("push from the submodule failed (%d): %s", code, stderr)
	}
	if got := markerLines(t, marker); len(got) != 1 {
		t.Errorf("the parent's migrated hook did not run on the submodule push: %v", got)
	}
}

// TestHookRemoveTakesTheLiveHookWhenBothStoresShareAName: the command removes
// from the live store, so a name present in both stores is not ambiguous -- it
// names one hook this command can remove and one it cannot, and the one it
// cannot is stated rather than silently left behind.
func TestHookRemoveTakesTheLiveHookWhenBothStoresShareAName(t *testing.T) {
	dir := newRepo(t)

	committed := filepath.Join(trackedHookDir(dir), "pre-pre-push")
	writeHookScript(t, committed, "true")
	safegitCommit(t, dir, "add a committed hook", filepath.Join(".safegit", "hooks", "pre-pre-push"))

	installed := filepath.Join(localHookDir(dir), "pre-pre-push")
	writeHookScript(t, installed, "true")

	_, stderr, code := runSafegit(t, dir, "hook", "remove", "pre-pre-push")
	if code != 0 {
		t.Fatalf("hook remove failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Errorf("the live hook survived the removal (err=%v)", err)
	}
	if _, err := os.Stat(committed); err != nil {
		t.Errorf("hook remove deleted the committed hook: %v", err)
	}
	if !strings.Contains(stderr, "COMMITTED") {
		t.Errorf("the removal must say the committed hook of that name still runs, got: %s", stderr)
	}
}
