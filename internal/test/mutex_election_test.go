package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin the CLI-level consequences of strictcli's mutex
// election rule: a bool mutex member elects its group only when its resolved
// value is TRUE, so `--no-x` DECLINES the option instead of choosing one, and a
// declined-only group is refused by the PARSER before any handler runs.
//
// Two of these invocations were actively dangerous before that rule existed:
// `push --no-only-tags` elected the push-mode group, fell through safegit's
// switch and pushed HEAD; `scrub match --no-mangle` elected the
// replace/mangle group and mangled everything the pattern matched. Both are
// now parse errors, and each test asserts BOTH the error text and that
// nothing was pushed or rewritten.

var mutexElectionEnv = []string{"CLAUDE_CODE_SESSION_ID=mutex-election-test"}

// assertNoOutput fails when a supposedly-refused invocation succeeded or said
// something other than the expected parse error.
func assertParseRefusal(t *testing.T, what, stderr string, code int, want string) {
	t.Helper()
	if code == 0 {
		t.Fatalf("%s: expected a nonzero exit, got 0", what)
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("%s: stderr %q does not contain %q", what, stderr, want)
	}
}

// TestPushDeclinedModeIsRefused covers the formerly dangerous fall-through:
// `push --no-only-tags` used to push HEAD because the switch had no default
// case and pushModeHead is the zero value.
func TestPushDeclinedModeIsRefused(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Give the repo a tag and a second branch, so a stray push of any mode
	// would leave a visible mark on the remote.
	gitCmd(t, dir, "tag", "v0.0.1")
	gitCmd(t, dir, "branch", "side")

	_, stderr, code := runSafegit(t, dir, "push", "--no-only-tags", "origin")
	assertParseRefusal(t, "push --no-only-tags", stderr, code,
		"one of --only-head, --only-branches, --only-tags, --both-branches-and-tags is required (--no-only-tags declines an option; it does not choose one)")

	if b := remoteBranches(t, remoteDir); len(b) != 0 {
		t.Errorf("push --no-only-tags pushed branches to the remote: %v", b)
	}
	if tg := remoteTags(t, remoteDir); len(tg) != 0 {
		t.Errorf("push --no-only-tags pushed tags to the remote: %v", tg)
	}
}

// TestPushNoModeIsRefused pins the no-clause form of the same error: nothing
// was typed at all, so there is no declined member to teach about.
func TestPushNoModeIsRefused(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	_, stderr, code := runSafegit(t, dir, "push", "origin")
	assertParseRefusal(t, "push (no mode)", stderr, code,
		"one of --only-head, --only-branches, --only-tags, --both-branches-and-tags is required")
	if strings.Contains(stderr, "declines an option") {
		t.Errorf("push (no mode): unexpected decline clause in %q", stderr)
	}

	if b := remoteBranches(t, remoteDir); len(b) != 0 {
		t.Errorf("push with no mode pushed branches: %v", b)
	}
}

// TestPushRedundantNegationIsRefused pins the dedicated error for a negation
// typed beside a real election: every typed token does something or errors.
func TestPushRedundantNegationIsRefused(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	gitCmd(t, dir, "tag", "v0.0.1")

	_, stderr, code := runSafegit(t, dir, "push", "--only-tags", "--no-only-head", "origin")
	assertParseRefusal(t, "push --only-tags --no-only-head", stderr, code,
		"--no-only-head cannot be combined with --only-tags (--no-only-head declines an option; it does not choose one)")

	if tg := remoteTags(t, remoteDir); len(tg) != 0 {
		t.Errorf("refused push still pushed tags: %v", tg)
	}
}

// TestDoctorDeclinedModeIsRefused pins the doctor group. `--no-fix` alone used
// to elect the group and land in the diagnose path (--diagnose is never read).
func TestDoctorDeclinedModeIsRefused(t *testing.T) {
	dir := newRepo(t)

	_, stderr, code := runSafegit(t, dir, "doctor", "--no-fix")
	assertParseRefusal(t, "doctor --no-fix", stderr, code,
		"one of --diagnose, --fix, --uninstall is required (--no-fix declines an option; it does not choose one)")

	// A declined --uninstall must not have removed anything either.
	if _, err := os.Stat(filepath.Join(dir, ".git", "safegit")); err != nil {
		t.Errorf(".git/safegit missing after a refused doctor: %v", err)
	}
}

// TestScrubMatchDeclinedMangleIsRefused covers the second formerly dangerous
// fall-through: `--no-mangle` with no `--replace` elected the group, and
// scrub match derived mangle mode from "replace is nil", so it mangled every
// match in history.
func TestScrubMatchDeclinedMangleIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--reason", "test declined mangle",
		"--no-mangle",
		"--entire-history",
	)
	assertParseRefusal(t, "scrub match --no-mangle", stderr, code,
		"one of --replace, --mangle is required (--no-mangle declines an option; it does not choose one)")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// TestScrubMatchDeclinedRangeIsRefused pins the parser error that replaced
// safegit's hand-written "one of --from or --entire-history is required"
// guard, which `--no-entire-history` was the only way to reach.
func TestScrubMatchDeclinedRangeIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test declined range",
		"--no-entire-history",
	)
	assertParseRefusal(t, "scrub match --no-entire-history", stderr, code,
		"one of --from, --entire-history is required (--no-entire-history declines an option; it does not choose one)")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// TestScrubMatchNoRangeIsRefused pins the no-clause form of the range error.
func TestScrubMatchNoRangeIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test missing range",
	)
	assertParseRefusal(t, "scrub match (no range)", stderr, code,
		"one of --from, --entire-history is required")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// TestScrubRunDeclinedRangeIsRefused is the scrub run half of the same
// deleted guard.
func TestScrubRunDeclinedRangeIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	recipe := "[[operations]]\ntype = \"match\"\npattern = \"SECRET_ABC\"\nreplace = \"REDACTED\"\n"
	commitFileEnv(t, dir, mutexElectionEnv, "recipe.toml", recipe, "add recipe")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "run",
		"--reason", "test declined range",
		"--no-entire-history",
		"recipe.toml",
	)
	assertParseRefusal(t, "scrub run --no-entire-history", stderr, code,
		"one of --from, --entire-history is required (--no-entire-history declines an option; it does not choose one)")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// TestScrubRunNoRangeIsRefused pins the no-clause form for scrub run.
func TestScrubRunNoRangeIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	recipe := "[[operations]]\ntype = \"match\"\npattern = \"SECRET_ABC\"\nreplace = \"REDACTED\"\n"
	commitFileEnv(t, dir, mutexElectionEnv, "recipe.toml", recipe, "add recipe")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "run",
		"--reason", "test missing range",
		"recipe.toml",
	)
	assertParseRefusal(t, "scrub run (no range)", stderr, code,
		"one of --from, --entire-history is required")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// TestScrubMatchDoubleElectionIsRefused pins that two real elections still
// report exclusivity rather than the new decline error.
func TestScrubMatchDoubleElectionIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, mutexElectionEnv, "secret.txt", "token SECRET_ABC here\n", "add secret")
	before := revParseHEAD(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, mutexElectionEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--mangle",
		"--reason", "test double election",
		"--entire-history",
	)
	assertParseRefusal(t, "scrub match --replace --mangle", stderr, code,
		"--replace and --mangle are mutually exclusive")
	assertHistoryIntact(t, dir, before, "secret.txt", "SECRET_ABC")
}

// assertHistoryIntact fails when HEAD moved or the named file lost the marker
// string -- i.e. when a refused rewrite rewrote something anyway.
func assertHistoryIntact(t *testing.T, dir, beforeHead, file, marker string) {
	t.Helper()
	if after := revParseHEAD(t, dir); after != beforeHead {
		t.Errorf("refused rewrite moved HEAD: %s -> %s", beforeHead, after)
	}
	content, ok := gitShow(t, dir, "HEAD", file)
	if !ok {
		t.Fatalf("refused rewrite removed %s from HEAD", file)
	}
	if !strings.Contains(content, marker) {
		t.Errorf("refused rewrite altered %s: %q no longer contains %q", file, content, marker)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	if !strings.Contains(string(onDisk), marker) {
		t.Errorf("refused rewrite altered the working tree copy of %s: %q", file, string(onDisk))
	}
}
