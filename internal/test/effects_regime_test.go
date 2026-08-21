package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These tests pin the two framework-level behaviours the strictcli effects
// regime introduced, and the safegit-level honesty they buy.

// dryRunLogHeader is the first line strictcli writes to stdout at the end of a
// dry-run dispatch in HUMAN mode. In machine mode there is no would-do text on
// stdout at all: the envelope is the sole document and the same records ride
// its preview member.
const dryRunLogHeader = "DRY RUN — no changes were made. Would do:"

// machineEnvelope is the framework's machine-mode document (effects contract
// §19.2), as much of it as safegit's tests read.
type machineEnvelope struct {
	InterfaceVersion int                      `json:"interface_version"`
	App              string                   `json:"app"`
	AppVersion       string                   `json:"app_version"`
	Command          *string                  `json:"command"`
	ExitCode         int                      `json:"exit_code"`
	Payload          json.RawMessage          `json:"payload"`
	DryRun           bool                     `json:"dry_run"`
	Preview          []map[string]interface{} `json:"preview"`
	PreviewError     map[string]interface{}   `json:"preview_error"`
	Diagnostics      []map[string]string      `json:"diagnostics"`
}

// decodeEnvelope parses a machine-mode run's stdout. In machine mode stdout
// carries exactly one document, so anything that does not parse whole is a
// failure rather than something to tolerate.
func decodeEnvelope(t *testing.T, stdout string) machineEnvelope {
	t.Helper()
	var env machineEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not a strictcli envelope: %v\nstdout: %s", err, stdout)
	}
	// The envelope contract's own version. It became 2 when the framework
	// added the update-command construct's `writes` member; safegit declares
	// no update command, so no envelope it emits carries one, but the version
	// it prints is the framework's and it is pinned here as such.
	if env.InterfaceVersion != 2 {
		t.Fatalf("unexpected envelope interface_version %d", env.InterfaceVersion)
	}
	return env
}

// jsonPayload returns the envelope's payload member, which is where a
// machine-mode run's own document now lives.
func jsonPayload(t *testing.T, stdout string) string {
	t.Helper()
	return string(decodeEnvelope(t, stdout).Payload)
}

// wouldDoLog returns the would-do log portion of stdout, or "" when the run was
// not a dry run.
func wouldDoLog(stdout string) string {
	if i := strings.Index(stdout, dryRunLogHeader); i >= 0 {
		return stdout[i:]
	}
	return ""
}

// TestPlainMutatingCommandNeedsNoConsent: strictcli's confirm protocol keys on
// the `consequential` declaration, NOT on the `mutating` classification. A
// plain mutating command -- `commit`, the single most-used command in the
// ecosystem -- must dispatch straight through with nothing added to argv, in a
// spawned process that has no terminal to confirm at.
//
// This is the regression that matters most: while the protocol inferred the
// prompt from `mutating`, bare `safegit commit` refused, and every documented
// convention that spells it bare was broken.
func TestPlainMutatingCommandNeedsNoConsent(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	before := gitLog(t, dir, "HEAD")

	_, stderr, code := runSafegitNoConsent(t, dir, nil, "commit", "-m", "bare commit", "--", "a.txt")
	if code != 0 {
		t.Fatalf("bare `safegit commit` must succeed with no approval flag; code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "Proceed?") || strings.Contains(stderr, "approve-consequential") {
		t.Errorf("a plain mutating command must not raise the confirm protocol, got: %s", stderr)
	}
	if after := gitLog(t, dir, "HEAD"); after != before+1 {
		t.Errorf("the bare commit did not land: %d -> %d", before, after)
	}
}

// TestConsequentialCommandRefusesWithoutConsent: the commands that DO declare
// themselves consequential still stop. A spawned safegit has no terminal to
// confirm at, so an unapproved consequential run is refused and changes
// nothing.
func TestConsequentialCommandRefusesWithoutConsent(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegitNoConsent(t, dir, nil, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitNoConsent(t, dir, nil,
		"author", "rewrite", "--old-name=Test", "--new-name=Renamed")
	if code == 0 {
		t.Fatalf("an unapproved consequential command must not succeed; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "must be confirmed at a terminal") && !strings.Contains(stderr, "aborted") {
		t.Errorf("the refusal must show that approval was missing, got: %s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("an unapproved rewrite moved HEAD anyway: %s -> %s", before, after)
	}
}

// TestConsequentialNonInteractiveMessageIsPinned: the exact stderr line a
// non-TTY stdin gets, verbatim from the contract (§8.3). It states why the run
// was refused -- there was no terminal to confirm at -- so a script or agent
// that trips it can tell this apart from an ordinary failure.
func TestConsequentialNonInteractiveMessageIsPinned(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegitNoConsent(t, dir, nil, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	_, stderr, code := runSafegitNoConsent(t, dir, nil,
		"author", "rewrite", "--old-name=Test", "--new-name=Renamed")
	const want = "error: stdin is not interactive; a consequential command must be confirmed at a terminal"
	if code == 0 || !strings.Contains(stderr, want) {
		t.Errorf("expected %q on stderr (code %d), got: %s", want, code, stderr)
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

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "push", "--refs", "head")
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
	testutil.WriteFile(t, dir, "a.txt", "one\n")
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

// TestHookRunRefusesDryRun: `hook run` executes operator-supplied scripts.
// safegit cannot know what a hook does, and the effects handle's `run` carries
// no stdin parameter, so the invocation cannot be minted either -- there is no
// honest preview to render. Before the declaration landed, `--dry-run hook run`
// silently ran every hook for real, which is the exact reading the effects
// contract's §3.5 exists to prevent. It must now refuse.
func TestHookRunRefusesDryRun(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")

	// The discovered name is `pre-pre-push` (or an entry under
	// `pre-pre-push.d/`); `hook install` copies the source under its own
	// basename, so the source has to carry that name to be found.
	src := filepath.Join(t.TempDir(), "pre-pre-push")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(src, []byte(script), 0o755); err != nil {
		t.Fatalf("writing hook source: %v", err)
	}
	if _, stderr, code := runSafegit(t, dir, "hook", "install", src); code != 0 {
		t.Fatalf("installing the hook failed (%d): %s", code, stderr)
	}

	_, stderr, code := runSafegit(t, dir, "--dry-run", "hook", "run")
	if code == 0 {
		t.Errorf("`--dry-run hook run` must refuse, got exit 0: %s", stderr)
	}
	if !strings.Contains(stderr, "--dry-run is not supported") {
		t.Errorf("the refusal must name the unsupported flag, got: %s", stderr)
	}
	if !strings.Contains(stderr, "hook") {
		t.Errorf("the refusal must name the command, got: %s", stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("`--dry-run hook run` executed the hook for real")
	}

	// A real run still executes it -- the refusal is scoped to the preview.
	if _, stderr, code := runSafegit(t, dir, "hook", "run"); code != 0 {
		t.Fatalf("`hook run` without --dry-run failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("`hook run` did not execute the hook: %v", err)
	}
}
