package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the two framework-level behaviours the strictcli effects
// regime introduced, and the safegit-level honesty they buy.

// dryRunLogHeader is the first line strictcli writes to stdout at the end of
// every dry-run dispatch. It is never suppressed -- not by --quiet and not by
// safegit's --json -- so a machine-readable dry run's stdout is safegit's JSON
// payload followed by this log.
const dryRunLogHeader = "DRY RUN — no changes were made. Would do:"

// jsonPayload returns stdout with any trailing would-do log removed, so a dry
// run's JSON body can be decoded. Non-dry-run output passes through unchanged.
func jsonPayload(stdout string) string {
	if i := strings.Index(stdout, dryRunLogHeader); i >= 0 {
		return stdout[:i]
	}
	return stdout
}

// wouldDoLog returns the would-do log portion of stdout, or "" when the run was
// not a dry run.
func wouldDoLog(stdout string) string {
	if i := strings.Index(stdout, dryRunLogHeader); i >= 0 {
		return stdout[i:]
	}
	return ""
}

// TestMutatingCommandRefusesWithoutConsent: strictcli's confirm protocol gates
// every command classified `mutating`. A spawned safegit has no terminal to
// confirm at, so an unconsented mutating run is refused and changes nothing.
func TestMutatingCommandRefusesWithoutConsent(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	before := gitLog(t, dir, "HEAD")

	_, stderr, code := runSafegitNoConsent(t, dir, nil, "commit", "-m", "unconsented", "--", "a.txt")
	if code == 0 {
		t.Fatalf("an unconsented mutating command must not succeed; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "--yes") && !strings.Contains(stderr, "aborted") {
		t.Errorf("the refusal must show that consent was missing, got: %s", stderr)
	}
	if after := gitLog(t, dir, "HEAD"); after != before {
		t.Errorf("an unconsented commit landed anyway: %d -> %d", before, after)
	}
}

// TestReadOnlyCommandNeedsNoConsent: a `read_only` command never prompts.
func TestReadOnlyCommandNeedsNoConsent(t *testing.T) {
	dir := newRepo(t)
	_, stderr, code := runSafegitNoConsent(t, dir, nil, "version")
	if code != 0 {
		t.Fatalf("a read-only command must run without consent, got %d: %s", code, stderr)
	}
}

// TestDryRunRendersWouldDoLog: dry mode's primary output is the would-do log,
// and it reaches stdout even when the command also emits JSON.
func TestDryRunRendersWouldDoLog(t *testing.T) {
	dir := newRepo(t)
	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "config", "set", "commit.casMaxAttempts", "42")
	if code != 0 {
		t.Fatalf("dry run failed (%d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	if log == "" {
		t.Fatalf("dry mode must render the would-do log to stdout, got: %q", stdout)
	}
	if !strings.Contains(log, "write:") {
		t.Errorf("the config write must appear in the would-do log, got: %s", log)
	}
}

// TestConfigSetDryRunWritesNothing: the recorded write must not happen.
func TestConfigSetDryRunWritesNothing(t *testing.T) {
	dir := newRepo(t)
	// Establish a config file with a known value first.
	if _, stderr, code := runSafegit(t, dir, "config", "set", "commit.casMaxAttempts", "7"); code != 0 {
		t.Fatalf("seeding config failed: %s", stderr)
	}
	configPath := filepath.Join(dir, ".git", "safegit", "config.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	if _, stderr, code := runSafegit(t, dir, "--dry-run", "config", "set", "commit.casMaxAttempts", "99"); code != 0 {
		t.Fatalf("dry run failed: %s", stderr)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config after dry run: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("a dry run rewrote the config file:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestHookInstallDryRunInstallsNothing: the mkdir/write/chmod are recorded, not
// performed.
func TestHookInstallDryRunInstallsNothing(t *testing.T) {
	dir := newRepo(t)
	src := filepath.Join(t.TempDir(), "my-hook")
	if err := os.WriteFile(src, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing hook source: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "hook", "install", src)
	if code != 0 {
		t.Fatalf("dry run failed (%d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	for _, verb := range []string{"mkdir:", "write:", "chmod:"} {
		if !strings.Contains(log, verb) {
			t.Errorf("the would-do log is missing %q, got: %s", verb, log)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "my-hook")); err == nil {
		t.Error("a dry run installed the hook for real")
	}
}

// TestPushDryRunDoesNotPush: safegit push had no dry-run handling at all before
// the effects regime -- `--dry-run push` pushed. It must now record the push and
// contact no remote.
func TestPushDryRunDoesNotPush(t *testing.T) {
	dir, remote := newRepoWithRemote(t)
	commitFileIn(t, dir, "a.txt", "one\n", "first")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "push", "--only-head")
	if code != 0 {
		t.Fatalf("push --dry-run failed (%d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	if !strings.Contains(log, "run: git push") {
		t.Errorf("the would-do log must record the push, got: %s", log)
	}
	if !strings.Contains(log, "granted: push") {
		t.Errorf("the recorded push must carry its grant, got: %s", log)
	}
	if branches := remoteBranches(t, remote); len(branches) != 0 {
		t.Errorf("a dry run pushed for real; remote now holds %v", branches)
	}
}

// TestCommitDryRunRecordsAndCommitsNothing: the commit pipeline's own dry-run
// seam builds the objects and stops before the ref moves. The would-do log must
// still say what the run would do -- an empty log reads as "this would change
// nothing" -- and the output must not claim a commit landed.
func TestCommitDryRunRecordsAndCommitsNothing(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	before := gitLog(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "preview", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit --dry-run failed (%d): %s", code, stderr)
	}
	if log := wouldDoLog(stdout); !strings.Contains(log, "run: git update-ref") {
		t.Errorf("the would-do log must record the ref update, got: %s", log)
	}
	if !strings.Contains(stdout, "would be committed") {
		t.Errorf("a preview must not claim files were committed, got: %s", stdout)
	}
	if after := gitLog(t, dir, "HEAD"); after != before {
		t.Errorf("a dry-run commit landed: %d -> %d", before, after)
	}
}
