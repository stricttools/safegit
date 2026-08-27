package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

// noOptionalLocks is the argv prefix internal/gitexec puts on EVERY git
// invocation safegit builds. A would-do record is an argv safegit would have
// executed, so it carries the prefix too: a preview that recorded a different
// command line from the one the execute path runs would not be a preview.
const noOptionalLocks = "--no-optional-locks"

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

	_, stderr, code := runSafegitEnv(t, dir, nil, "commit", "-m", "bare commit", "--", "a.txt")
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
	if _, stderr, code := runSafegitEnv(t, dir, nil, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, nil,
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
	if _, stderr, code := runSafegitEnv(t, dir, nil, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	_, stderr, code := runSafegitEnv(t, dir, nil,
		"author", "rewrite", "--old-name=Test", "--new-name=Renamed")
	const want = "error: stdin is not interactive; a consequential command must be confirmed at a terminal"
	if code == 0 || !strings.Contains(stderr, want) {
		t.Errorf("expected %q on stderr (code %d), got: %s", want, code, stderr)
	}
}

// TestReadOnlyCommandNeedsNoConsent: a `read_only` command never prompts.
func TestReadOnlyCommandNeedsNoConsent(t *testing.T) {
	dir := newRepo(t)
	_, stderr, code := runSafegitEnv(t, dir, nil, "version")
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
	if _, err := os.Stat(filepath.Join(dir, ".git", "safegit", "hooks", "my-hook")); err == nil {
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
	if !strings.Contains(log, "run: git "+noOptionalLocks+" push") {
		t.Errorf("the would-do log must record the push, got: %s", log)
	}
	if !strings.Contains(log, "granted: push") {
		t.Errorf("the recorded push must carry its grant, got: %s", log)
	}
	if branches := remoteBranches(t, remote); len(branches) != 0 {
		t.Errorf("a dry run pushed for real; remote now holds %v", branches)
	}
}

// TestCommitDryRunRecordsAndCommitsNothing: the commit pipeline's ref update is
// minted through the effects handle, so a dry run records it instead of
// performing it. The would-do log must say what the run would do -- an empty
// log reads as "this would change nothing" -- and everything it says must be
// something the preview actually knows.
//
// The commit SHA is not such a thing. It is a function of the committer
// timestamp, so the object a preview could build is never the object a real run
// will create; the recorded argv therefore carries a placeholder where the new
// SHA goes, and the human output states the tree the preview really computed
// instead of a `[branch sha]` line naming a commit that will never exist.
func TestCommitDryRunRecordsAndCommitsNothing(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	before := gitLog(t, dir, "HEAD")
	head := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "preview", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit --dry-run failed (%d): %s", code, stderr)
	}

	log := wouldDoLog(stdout)
	// The whole argv, with the ref that moves, the placeholder in place of the
	// unknowable new SHA, and the value the compare-and-swap is made against.
	wantRecord := "run: git " + noOptionalLocks + " update-ref refs/heads/main <new-commit> " + head
	if !strings.Contains(log, wantRecord) {
		t.Errorf("the would-do log must record the ref update as %q, got: %s", wantRecord, log)
	}
	if sha := previewSHAIn(log); sha != "" {
		t.Errorf("the recorded ref update names %s as the new commit, which no run will ever create: %s", sha, log)
	}

	// The human output: the tree is real and reported, the commit line is not.
	if !strings.Contains(stdout, "would be committed") {
		t.Errorf("a preview must not claim files were committed, got: %s", stdout)
	}
	tree := strings.TrimSpace(testutil.GitOut(t, dir, "rev-parse", "HEAD^{tree}"))
	if strings.Contains(stdout, "(tree "+tree[:8]+")") {
		t.Errorf("the preview reported the PARENT's tree; it must report the tree it computed: %s", stdout)
	}
	if !strings.Contains(stdout, "would commit on main (tree ") {
		t.Errorf("the preview must name the branch and the tree it computed, got: %s", stdout)
	}
	if line := commitShapedLine(stdout); line != "" {
		t.Errorf("a preview printed a commit line %q, which reads as a commit that happened: %s", line, stdout)
	}

	if after := gitLog(t, dir, "HEAD"); after != before {
		t.Errorf("a dry-run commit landed: %d -> %d", before, after)
	}
}

// hexSHA matches a full or abbreviated object name.
var hexSHA = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// previewSHAIn returns the first object-name-shaped token a recorded `update-ref`
// line gives as the NEW value, or "" when it carries the placeholder. The old
// value is exempt: a preview knows what the ref points at now.
func previewSHAIn(log string) string {
	for _, line := range strings.Split(log, "\n") {
		i := strings.Index(line, "update-ref ")
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[i:])
		if len(fields) < 3 {
			continue
		}
		if hexSHA.MatchString(fields[2]) {
			return fields[2]
		}
	}
	return ""
}

// commitShapedLine returns the first line shaped like the `[branch sha] subject`
// line a real commit prints -- the line the submodule auto-bump parses a child's
// commit SHA out of -- or "" when there is none.
func commitShapedLine(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, "[") {
			continue
		}
		close := strings.IndexByte(line, ']')
		if close < 0 {
			continue
		}
		if fields := strings.Fields(line[1:close]); len(fields) >= 2 && hexSHA.MatchString(fields[1]) {
			return line
		}
	}
	return ""
}

// TestAmendAndRewordDryRunPrintNoCommitLine: the same honesty on the other two
// entry points. A reword's preview goes further -- it builds no commit object at
// all -- so there is not even an unpublishable SHA to be tempted by.
func TestAmendAndRewordDryRunPrintNoCommitLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		verb string
		args []string
	}{
		{"amend", "amend", []string{"--dry-run", "commit", "--amend", "-m", "preview amend", "--", "extra.txt"}},
		{"reword", "reword", []string{"--dry-run", "commit", "--amend", "-m", "preview reword"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// newRepo already carries one commit, which is the tip both
			// forms rewrite.
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "extra.txt", "extra\n")
			head := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("%s --dry-run failed (%d): %s", tc.name, code, stderr)
			}
			if line := commitShapedLine(stdout); line != "" {
				t.Errorf("the %s preview printed a commit line %q: %s", tc.name, line, stdout)
			}
			if !strings.Contains(stdout, "would "+tc.verb+" on main (tree ") {
				t.Errorf("the %s preview must name the branch and the tree, got: %s", tc.name, stdout)
			}
			if sha := previewSHAIn(wouldDoLog(stdout)); sha != "" {
				t.Errorf("the recorded ref update of the %s preview invents the new SHA %s: %s", tc.name, sha, stdout)
			}
			if now := testutil.Rev(t, dir, "HEAD"); now != head {
				t.Errorf("the %s preview moved HEAD: %s -> %s", tc.name, head, now)
			}
		})
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

	// Anything in the tool-owned store is a hook, whatever it is called;
	// `hook install` copies the source under its own basename.
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

// TestPassthroughDryRunArgvCarriesTheGlobalPrefix pins the 0.2 boundary
// property from the operator's side: the guarded passthroughs used to build
// their git argv by hand, so the command they recorded (and, before the effects
// regime, executed) was missing the --no-optional-locks prefix every other git
// invocation in safegit carries. The argv now comes from internal/gitexec, so
// what a dry run records is exactly what the execute path would run.
//
// switch is the whole family's representative: every one of them --
// switch, merge, rebase, reset, bisect, pull, cherry-pick and revert -- goes
// through the same single argv builder.
func TestPassthroughDryRunArgvCarriesTheGlobalPrefix(t *testing.T) {
	dir := newRepo(t)
	testutil.GitRaw(t, dir, "branch", "other")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "switch", "other")
	if code != 0 {
		t.Fatalf("switch --dry-run failed (%d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	if !strings.Contains(log, "run: git "+noOptionalLocks+" switch other") {
		t.Errorf("the recorded passthrough argv must carry the global prefix, got: %s", log)
	}
	if branch := strings.TrimSpace(testutil.GitRaw(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); branch != "main" {
		t.Errorf("a dry-run switch moved the branch to %q", branch)
	}
}

// TestComputeStepDryRunRecordsTheArgvTheExecutePathRuns: what a preview records
// is what the real run would perform, and for the restructured merge and
// cherry-pick that is the COMPUTE step's argv rather than the operator's.
//
// The execute path never runs `git merge <branch>` or `git cherry-pick <sha>`:
// it runs them with the flags that stop git before the commit -- `--no-ff
// --no-commit` for a merge, always, whatever the operator passed, and
// `--no-commit` for a pick -- and safegit's own pipeline makes the commit. A
// would-do log naming the bare form would describe git authoring a commit,
// which is exactly the mutation the restructure removed.
func TestComputeStepDryRunRecordsTheArgvTheExecutePathRuns(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		dir := newDivergedBranchRepo(t)

		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "merge", "feature")
		if code != 0 {
			t.Fatalf("merge --dry-run failed (%d): %s", code, stderr)
		}
		log := wouldDoLog(stdout)
		if !strings.Contains(log, "run: git "+noOptionalLocks+" merge --no-ff --no-commit feature") {
			t.Errorf("the recorded argv is not the compute step's, got: %s", log)
		}
	})

	t.Run("cherry-pick", func(t *testing.T) {
		dir := newDivergedBranchRepo(t)
		sha := testutil.Rev(t, dir, "feature")

		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "cherry-pick", sha)
		if code != 0 {
			t.Fatalf("cherry-pick --dry-run failed (%d): %s", code, stderr)
		}
		log := wouldDoLog(stdout)
		if !strings.Contains(log, "run: git "+noOptionalLocks+" cherry-pick --no-commit "+sha) {
			t.Errorf("the recorded argv is not the compute step's, got: %s", log)
		}
	})
}

// TestMergePreviewRecordsNoGitMergeWhereNoneRuns is the other half of the same
// honesty, and it is the half that was wrong: a preview must record what the
// real run would do, so a preview whose answer is one the real run reaches
// WITHOUT running git's merge machinery must record NO `git merge` at all.
//
// Three answers are in that set, and they are the three subtests:
//
//   - a born FAST-FORWARD, which safegit performs itself with a
//     compare-and-swap onto the incoming tip and an index-and-working-tree
//     sync -- performMerge takes that arm before its compute step;
//   - the same on an UNBORN branch, where a merge can only ever be a
//     fast-forward;
//   - an `--ff-only` REFUSAL, where performMerge decides the branches have
//     diverged and exits before handing git any argv at all.
//
// Each of them used to record `git merge --no-ff --no-commit <branch>`, which
// described a subprocess that nothing performs.
//
// The control is the last subtest: a merge that really would compute still
// records its compute step, so the suppression is scoped to the answers that
// earn it rather than to previews at large. The FAILING paths keep recording
// too, deliberately -- `safegit merge no-such-ref` really does hand that
// argument to git in a real run.
func TestMergePreviewRecordsNoGitMergeWhereNoneRuns(t *testing.T) {
	// gitMergeRecord is the phrase a would-do log carries only when the compute
	// step was recorded. Matching on `run: git ... merge` rather than the whole
	// argv keeps the assertion true whatever branch name the case uses.
	const gitMergeRecord = "run: git " + noOptionalLocks + " merge"

	t.Run("a born fast-forward", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "f.txt", "one\n")
		safegitCommit(t, dir, "one", "f.txt")
		testutil.Git(t, dir, "branch", "ahead")
		testutil.Git(t, dir, "switch", "ahead")
		testutil.WriteFile(t, dir, "f.txt", "two\n")
		safegitCommit(t, dir, "two", "f.txt")
		testutil.Git(t, dir, "switch", "main")

		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "merge", "ahead")
		if code != 0 {
			t.Fatalf("merge --dry-run failed (%d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "fast-forward") {
			t.Fatalf("the fixture no longer produces a fast-forward preview:\n%s", stdout)
		}
		if log := wouldDoLog(stdout); strings.Contains(log, gitMergeRecord) {
			t.Errorf("the fast-forward preview recorded a git merge the real run never issues:\n%s", log)
		}
	})

	t.Run("an unborn fast-forward", func(t *testing.T) {
		fx := newUnbornRepo(t)

		stdout, stderr, code := runSafegit(t, fx.dir, "--dry-run", "merge", "side")
		if code != 0 {
			t.Fatalf("merge --dry-run on an unborn branch failed (%d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "fast-forward") {
			t.Fatalf("the unborn preview no longer reports a fast-forward:\n%s", stdout)
		}
		if log := wouldDoLog(stdout); strings.Contains(log, gitMergeRecord) {
			t.Errorf("the unborn fast-forward preview recorded a git merge the real run never issues:\n%s", log)
		}
	})

	// --ff-only over DIVERGED branches: safegit decides this one itself, before
	// git runs, and the divergences catalog records why ("The fast-forward-only
	// refusal is safegit's, not git's") -- letting git decide would let git move
	// the ref outside the compare-and-swap. So the preview's verdict is a
	// refusal, and a refusal reaches no compute step to record.
	t.Run("an --ff-only refusal", func(t *testing.T) {
		fx := newPreviewRepo(t)

		stdout, stderr, code := runSafegit(t, fx.dir, "--dry-run", "merge", "--ff-only", "side")
		if code != 0 {
			t.Fatalf("merge --ff-only --dry-run failed (%d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "REFUSED") {
			t.Fatalf("the fixture no longer produces an --ff-only refusal preview:\n%s", stdout)
		}
		if log := wouldDoLog(stdout); strings.Contains(log, gitMergeRecord) {
			t.Errorf("the --ff-only refusal preview recorded a git merge the real run never issues:\n%s", log)
		}
	})

	t.Run("a merge that really computes still records it", func(t *testing.T) {
		fx := newPreviewRepo(t)

		stdout, stderr, code := runSafegit(t, fx.dir, "--dry-run", "merge", "side")
		if code != 0 {
			t.Fatalf("merge --dry-run failed (%d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "CONFLICT") {
			t.Fatalf("the fixture no longer produces a conflicting merge preview:\n%s", stdout)
		}
		log := wouldDoLog(stdout)
		if !strings.Contains(log, gitMergeRecord+" --no-ff --no-commit side") {
			t.Errorf("a preview that would compute did not record its compute step:\n%s", log)
		}
	})
}

// TestHistoryRewriteDryRunRecordsNoInventedSHA is the same honesty on the
// history-rewrite side, which mints four effects: the ref move onto the
// rewritten history, then the reflog expire, repack and prune that make the
// pre-rewrite objects unreachable.
//
// The ref move is the one with a value nobody can know in advance -- computing
// the rewritten head would mean performing the rewrite -- so it carries a
// placeholder there, while the value it moves AWAY from is the real current
// head, which the preview did read.
//
// All four recorded argv carry the --no-optional-locks prefix, and that is
// pinned here for the same reason the passthrough family pins it above: this
// recorder is a dry-mode-only description written BESIDE the execute path
// rather than by it, so the one thing keeping the two from drifting is that
// both build their argv through internal/gitexec. A recorded command missing
// the prefix would be the first sign that this one had gone back to building
// argv by hand.
func TestHistoryRewriteDryRunRecordsNoInventedSHA(t *testing.T) {
	dir, initialSHA := newRawSecretRepo(t)
	head := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, dryRunScrubEnv,
		"--dry-run", "scrub", "file", "--replace-with", "secret.txt", "secret.txt", "--from", initialSHA, "--reason", "preview honesty")
	if code != 0 {
		t.Fatalf("scrub file --dry-run failed (%d): %s", code, stderr)
	}

	log := wouldDoLog(stdout)
	if log == "" {
		t.Fatalf("a rewrite preview must render a would-do log, got: %s", stdout)
	}
	for _, want := range []string{
		"run: git " + noOptionalLocks + " update-ref refs/heads/main <rewritten> " + head,
		"run: git " + noOptionalLocks + " reflog expire --expire=now --all",
		"run: git " + noOptionalLocks + " repack -a -d --unpack-unreachable=now",
		"run: git " + noOptionalLocks + " prune --expire=now",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the would-do log must record %q, got: %s", want, log)
		}
	}
	if sha := previewSHAIn(log); sha != "" {
		t.Errorf("the recorded ref move names %s as the rewritten head, which the preview cannot know: %s", sha, log)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("the rewrite preview moved HEAD: %s -> %s", head, now)
	}
}

// procMutations returns the envelope's records for subprocess mutations -- the
// effects a `run` mints. The records are populated in BOTH modes (the framework
// documents its effect log as a live run's record as much as a preview's), so
// this counts what an executing run really performed as readily as what a
// preview would.
func procMutations(env machineEnvelope) []map[string]interface{} {
	var out []map[string]interface{}
	for _, rec := range env.Preview {
		if kind, _ := rec["kind"].(string); kind == "proc_mutate" {
			out = append(out, rec)
		}
	}
	return out
}

// reflogLength counts the reflog entries of a ref, which is one per ref
// movement: it is how many times the ref was really updated, measured off git's
// own record rather than off safegit's report.
func reflogLength(t *testing.T, dir, ref string) int {
	t.Helper()
	out := strings.TrimSpace(testutil.GitOut(t, dir, "reflog", "show", "--format=%H", ref))
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// TestCommitPerformsExactlyOneRefUpdate: the commit pipeline's ref update is
// minted through the effects handle in BOTH modes, from a single site inside
// the compare-and-swap loop. The trap that design avoids is a second mint
// beside it -- a handler-side record alongside the pipeline's own update, which
// fires twice per commit and describes a move that has already happened.
//
// So: one recorded mutation, and one ref movement in git's own reflog.
func TestCommitPerformsExactlyOneRefUpdate(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	before := reflogLength(t, dir, "refs/heads/main")

	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "-m", "one update", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.DryRun {
		t.Error("dry_run = true for an executing commit")
	}

	mutations := procMutations(env)
	if len(mutations) != 1 {
		t.Fatalf("an executing commit recorded %d subprocess mutations, want exactly 1: %v", len(mutations), env.Preview)
	}
	detail, _ := mutations[0]["detail"].(string)
	if !strings.Contains(detail, "update-ref refs/heads/main") {
		t.Errorf("the recorded mutation is not the ref update: %q", detail)
	}
	if recorded, _ := mutations[0]["recorded"].(bool); recorded {
		t.Error("an executing run's effect is marked recorded, which means it was not performed")
	}

	if got := reflogLength(t, dir, "refs/heads/main") - before; got != 1 {
		t.Errorf("the commit moved refs/heads/main %d times, want exactly 1", got)
	}
}

// TestCommitDryRunEnvelopeCarriesOneRecordedMutation is the same count on the
// preview side, read off the envelope rather than the would-do text: exactly
// one recorded mutation, marked as recorded, plus the payload the preview
// computed.
func TestCommitDryRunEnvelopeCarriesOneRecordedMutation(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	before := reflogLength(t, dir, "refs/heads/main")

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "commit", "-m", "preview", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit --dry-run failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if !env.DryRun {
		t.Error("dry_run = false under --dry-run")
	}

	// One record, and it is the ref update: the whole of what a preview of a
	// commit would change about the world.
	if len(env.Preview) != 1 {
		t.Fatalf("a preview carried %d effect records, want exactly 1: %v", len(env.Preview), env.Preview)
	}
	mutations := procMutations(env)
	if len(mutations) != 1 {
		t.Fatalf("a preview recorded %d subprocess mutations, want exactly 1: %v", len(mutations), env.Preview)
	}
	if recorded, _ := mutations[0]["recorded"].(bool); !recorded {
		t.Error("a preview's effect is not marked recorded, which means it was performed")
	}
	if detail, _ := mutations[0]["detail"].(string); !strings.Contains(detail, "update-ref refs/heads/main <new-commit>") {
		t.Errorf("the recorded ref update is not the preview's: %q", detail)
	}

	// The preview's own document rides the same envelope.
	if len(env.Payload) == 0 || !strings.Contains(string(env.Payload), `"tree"`) {
		t.Errorf("the envelope carries no preview payload: %s", env.Payload)
	}

	if got := reflogLength(t, dir, "refs/heads/main"); got != before {
		t.Errorf("a preview moved refs/heads/main: %d -> %d reflog entries", before, got)
	}
}

// TestDumpSchemaPublishesTheObserveAllowlist: the observe authorization is part
// of safegit's published interface, not a private detail -- a consumer reading
// the schema can see exactly which git invocations the tool considers
// observations. It is generated from the argv classification table's read view,
// so what is published here is the table's own answer.
func TestDumpSchemaPublishesTheObserveAllowlist(t *testing.T) {
	dir := newRepo(t)
	// The dump derives its project_id from the module it is run in, and writes
	// the schema under that directory -- so the probe runs in a throwaway
	// module rather than in safegit's own checkout, whose committed dump the
	// release pipeline owns.
	testutil.WriteFile(t, dir, "go.mod", "module example.test/schema-probe\n\ngo 1.25\n")

	stdout, stderr, code := runSafegit(t, dir, "--dump-schema")
	if code != 0 {
		t.Fatalf("--dump-schema failed (%d): %s", code, stderr)
	}
	path := strings.TrimSpace(stdout)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the dumped schema at %q: %v", path, err)
	}

	var schema struct {
		Allowlist [][]string `json:"proc_observe_allowlist"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("the dumped schema does not parse: %v", err)
	}
	if len(schema.Allowlist) == 0 {
		t.Fatal("the dumped schema publishes no proc_observe_allowlist")
	}

	verbs := make(map[string]bool, len(schema.Allowlist))
	for _, prefix := range schema.Allowlist {
		if len(prefix) != 3 || prefix[0] != "git" || prefix[1] != noOptionalLocks {
			t.Errorf("published prefix %v is not `git %s <verb>`; a shorter one would match invocations it does not mean to",
				prefix, noOptionalLocks)
			continue
		}
		verbs[prefix[2]] = true
	}
	for _, read := range []string{"rev-parse", "cat-file", "ls-tree", "status"} {
		if !verbs[read] {
			t.Errorf("the published allowlist omits %q, which the classification table declares observe-only", read)
		}
	}
	// Nothing that changes anything, and nothing whose reading depends on what
	// follows it.
	for _, mutating := range []string{"update-ref", "switch", "merge", "rebase", "reset", "push", "commit-tree", "write-tree", "add", "reflog", "tag"} {
		if verbs[mutating] {
			t.Errorf("the published allowlist admits %q, which would execute during a --dry-run", mutating)
		}
	}
}
