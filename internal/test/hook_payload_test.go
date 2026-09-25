package test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The machine payload is the record of the hook runs: `push` carries a `hooks`
// list on every run that ran hooks, passing or failing, and `hook run` carries
// the same list. Each entry holds facts about one run -- its name, exit code,
// whether it timed out, how long it took, and the processes it left behind.
// Whether the run passed is carried by the exit code and the error text on
// stderr, never by a payload field. A timed-out hook was killed and has no exit
// status of its own, so its exit_code is null; a leftover safegit could not
// identify is recorded as unidentified_leftovers, with the reason
// identification failed in leftover_identification_error.

// hookEntry is one element of a payload's `hooks` list.
type hookEntry struct {
	Name              *string          `json:"name"`
	ExitCode          *int             `json:"exit_code"`
	TimedOut          *bool            `json:"timed_out"`
	DurationMS        *int64           `json:"duration_ms"`
	LeftoverProcesses []leftoverRecord `json:"leftover_processes"`
	// UnidentifiedLeftovers and LeftoverIdentificationError record that
	// something still held the hook's output and could not be named, and why.
	UnidentifiedLeftovers       *bool   `json:"unidentified_leftovers"`
	LeftoverIdentificationError *string `json:"leftover_identification_error"`
}

type leftoverRecord struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Killed  bool   `json:"killed"`
}

// hookEntryMembers is the closed set of members an entry may carry. A verdict
// member (a status, a reason, a pass/fail flag) is refused by this set.
var hookEntryMembers = map[string]bool{
	"name": true, "exit_code": true, "timed_out": true, "duration_ms": true, "leftover_processes": true,
	"unidentified_leftovers": true, "leftover_identification_error": true,
}

// payloadHooks decodes the `hooks` list out of a machine-mode run's payload and
// checks every entry carries all of its members and nothing else.
func payloadHooks(t *testing.T, stdout string) []hookEntry {
	t.Helper()
	raw := decodeEnvelope(t, stdout).Payload
	if len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("the payload is null; a run that ran hooks must record them\nstdout: %s", stdout)
	}
	var generic struct {
		Hooks []map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("payload does not decode: %v\n%s", err, raw)
	}
	if generic.Hooks == nil {
		t.Fatalf("the payload has no `hooks` list:\n%s", raw)
	}
	for i, e := range generic.Hooks {
		for k := range e {
			if !hookEntryMembers[k] {
				t.Errorf("hooks[%d] carries %q, which is not a fact about the run; the verdict is the exit code:\n%s", i, k, raw)
			}
		}
		for k := range hookEntryMembers {
			if _, ok := e[k]; !ok {
				t.Errorf("hooks[%d] lacks %q:\n%s", i, k, raw)
			}
		}
		if lp, ok := e["leftover_processes"]; ok && string(lp) == "null" {
			t.Errorf("hooks[%d].leftover_processes is null; it is a list, empty when nothing was left:\n%s", i, raw)
		}
	}
	var typed struct {
		Hooks []hookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("payload does not decode: %v\n%s", err, raw)
	}
	return typed.Hooks
}

// assertNoVerdictMember refuses a payload top-level member that states a
// verdict about the run rather than a fact.
func assertNoVerdictMember(t *testing.T, stdout string) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &top); err != nil {
		t.Fatalf("payload is not an object: %v", err)
	}
	for k := range top {
		for _, bad := range []string{"status", "reason", "verdict", "failed", "passed", "error"} {
			if strings.Contains(k, bad) {
				t.Errorf("the payload carries %q, a verdict; the exit code carries it", k)
			}
		}
	}
}

// checkEntry asserts one hook entry's facts.
func checkEntry(t *testing.T, e hookEntry, name string, exitCode int, timedOut bool) {
	t.Helper()
	if e.Name == nil || *e.Name != name {
		t.Errorf("entry name = %v, want %q", e.Name, name)
	}
	if e.ExitCode == nil || *e.ExitCode != exitCode {
		t.Errorf("entry %s exit_code = %v, want %d", name, e.ExitCode, exitCode)
	}
	if e.TimedOut == nil || *e.TimedOut != timedOut {
		t.Errorf("entry %s timed_out = %v, want %v", name, e.TimedOut, timedOut)
	}
	if e.DurationMS == nil || *e.DurationMS < 0 {
		t.Errorf("entry %s duration_ms = %v, want a non-negative number", name, e.DurationMS)
	}
	checkIdentified(t, e)
}

// checkIdentified asserts an entry records no unidentified leftover: false and
// null, never absent.
func checkIdentified(t *testing.T, e hookEntry) {
	t.Helper()
	name := "<unnamed>"
	if e.Name != nil {
		name = *e.Name
	}
	if e.UnidentifiedLeftovers == nil || *e.UnidentifiedLeftovers {
		t.Errorf("entry %s unidentified_leftovers = %v, want false", name, e.UnidentifiedLeftovers)
	}
	if e.LeftoverIdentificationError != nil {
		t.Errorf("entry %s leftover_identification_error = %q, want null", name, *e.LeftoverIdentificationError)
	}
}

// checkTimedOutEntry asserts the entry of a hook the timeout killed: exit_code
// null, because a killed hook has no exit status of its own, and timed_out true.
func checkTimedOutEntry(t *testing.T, e hookEntry, name string) {
	t.Helper()
	if e.Name == nil || *e.Name != name {
		t.Errorf("entry name = %v, want %q", e.Name, name)
	}
	if e.ExitCode != nil {
		t.Errorf("entry %s exit_code = %d, want null: a killed hook has no exit status", name, *e.ExitCode)
	}
	if e.TimedOut == nil || !*e.TimedOut {
		t.Errorf("entry %s timed_out = %v, want true", name, e.TimedOut)
	}
	checkIdentified(t, e)
}

// installDirHook writes an executable hook into the live store's
// pre-pre-push.d directory, so hooks run in name order.
func installDirHook(t *testing.T, dir, name, body string) {
	t.Helper()
	writeHookScript(t, filepath.Join(localHookDir(dir), "pre-pre-push.d", name), body)
}

func setHookTimeout(t *testing.T, dir, seconds string) {
	t.Helper()
	if _, stderr, code := runSafegit(t, dir, "config", "set", "hooks.preprepush.timeoutSeconds", seconds); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}
}

func TestPushPayloadRecordsAPassingHook(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	installDirHook(t, dir, "10-lint", "true")

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("push --json failed (code %d): %s", code, stderr)
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %d entries, want 1", len(hooks))
	}
	checkEntry(t, hooks[0], "10-lint", 0, false)
	if len(hooks[0].LeftoverProcesses) != 0 {
		t.Errorf("a hook that left nothing must record no leftover processes: %+v", hooks[0].LeftoverProcesses)
	}
	assertNoVerdictMember(t, stdout)
}

func TestPushPayloadRecordsNoHooksWhenNoneRan(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	installDirHook(t, dir, "10-lint", "true")

	for _, args := range [][]string{
		{"--json", "--dry-run", "push", "--refs", "head", "origin"},
		{"--json", "push", "--refs", "head", "--no-pre-push-hook", "origin"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, dir, args...)
			if code != 0 {
				t.Fatalf("%v failed (code %d): %s", args, code, stderr)
			}
			if hooks := payloadHooks(t, stdout); len(hooks) != 0 {
				t.Errorf("no hook ran, so the list must be empty: %+v", hooks)
			}
		})
	}
}

func TestPushPayloadEmittedWhenAHookFails(t *testing.T) {
	dir, remote := newRepoWithRemote(t)
	installDirHook(t, dir, "10-lint", "true")
	installDirHook(t, dir, "20-test", "exit 3")

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "origin")
	if code != exitcode.PushHookFailed {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitcode.PushHookFailed, stderr)
	}
	if !strings.Contains(stderr, "hook 20-test failed (exit 3)") {
		t.Errorf("the verdict must be on stderr:\n%s", stderr)
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) != 2 {
		t.Fatalf("hooks = %d entries, want 2", len(hooks))
	}
	checkEntry(t, hooks[0], "10-lint", 0, false)
	checkEntry(t, hooks[1], "20-test", 3, false)
	assertNoVerdictMember(t, stdout)

	var p struct {
		Refs []json.RawMessage `json:"refs"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Refs) != 0 {
		t.Errorf("nothing was pushed, so refs must be empty: %d entries", len(p.Refs))
	}
	if branches := remoteBranches(t, remote); len(branches) != 0 {
		t.Errorf("a failed hook must push nothing; the remote has %v", branches)
	}
}

func TestPushPayloadEmittedWhenAHookTimesOut(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	setHookTimeout(t, dir, "1")
	installDirHook(t, dir, "10-lint", "exec sleep 30")

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "origin")
	if code != exitcode.PushHookTimeout {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitcode.PushHookTimeout, stderr)
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %d entries, want 1", len(hooks))
	}
	checkTimedOutEntry(t, hooks[0], "10-lint")
	if hooks[0].DurationMS == nil || *hooks[0].DurationMS < 1000 {
		t.Errorf("duration_ms = %v, want at least the 1s budget", hooks[0].DurationMS)
	}
	assertNoVerdictMember(t, stdout)
}

func TestHookRunPayloadRecordsTheHooks(t *testing.T) {
	dir := newRepo(t)
	installDirHook(t, dir, "10-lint", "true")
	installDirHook(t, dir, "20-test", "true")

	stdout, stderr, code := runSafegit(t, dir, "--json", "hook", "run")
	if code != 0 {
		t.Fatalf("hook run --json failed (code %d): %s", code, stderr)
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) != 2 {
		t.Fatalf("hooks = %d entries, want 2", len(hooks))
	}
	checkEntry(t, hooks[0], "10-lint", 0, false)
	checkEntry(t, hooks[1], "20-test", 0, false)
	assertNoVerdictMember(t, stdout)

	stdout, stderr, code = runSafegit(t, dir, "--json", "hook", "run", "20-test")
	if code != 0 {
		t.Fatalf("hook run 20-test --json failed (code %d): %s", code, stderr)
	}
	hooks = payloadHooks(t, stdout)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %d entries, want 1", len(hooks))
	}
	checkEntry(t, hooks[0], "20-test", 0, false)
}

func TestHookRunPayloadRecordsAFailingHook(t *testing.T) {
	dir := newRepo(t)
	installDirHook(t, dir, "10-lint", "exit 4")

	for _, args := range [][]string{
		{"--json", "hook", "run"},
		{"--json", "hook", "run", "10-lint"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, dir, args...)
			if code != exitcode.PushHookFailed {
				t.Fatalf("exit = %d, want %d; stderr: %s", code, exitcode.PushHookFailed, stderr)
			}
			hooks := payloadHooks(t, stdout)
			if len(hooks) != 1 {
				t.Fatalf("hooks = %d entries, want 1", len(hooks))
			}
			checkEntry(t, hooks[0], "10-lint", 4, false)
			assertNoVerdictMember(t, stdout)
		})
	}
}

// The all-hooks form returns the code of the first hook that did not pass, in
// run order: 21 when that hook timed out, not the generic hook-failure 20.
func TestHookRunAllReturnsTimeoutCodeForATimedOutHook(t *testing.T) {
	dir := newRepo(t)
	setHookTimeout(t, dir, "1")
	installDirHook(t, dir, "10-lint", "exec sleep 30")
	installDirHook(t, dir, "20-test", "true")

	stdout, stderr, code := runSafegit(t, dir, "--json", "hook", "run")
	if code != exitcode.PushHookTimeout {
		t.Fatalf("exit = %d, want %d (PushHookTimeout); stderr: %s", code, exitcode.PushHookTimeout, stderr)
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) == 0 || hooks[0].Name == nil || *hooks[0].Name != "10-lint" {
		t.Fatalf("the first entry must be 10-lint: %+v", hooks)
	}
	checkTimedOutEntry(t, hooks[0], "10-lint")

	// The text form answers the same code.
	if _, stderr, code := runSafegit(t, dir, "hook", "run"); code != exitcode.PushHookTimeout {
		t.Errorf("text-mode exit = %d, want %d; stderr: %s", code, exitcode.PushHookTimeout, stderr)
	}
}

// A multi-ref push is atomic, and the payload says so whether or not it got as
// far as git: `atomic`, like `force_with_lease`, echoes what the invocation was
// going to push. A hook that stops the push leaves `refs` empty, and the atomic
// fact must not be recomputed from that empty list.
func TestPushPayloadStatesAtomicWhenAHookStopsAMultiRefPush(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	testutil.Git(t, dir, "branch", "feature")
	installDirHook(t, dir, "10-lint", "exit 3")

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "branches", "origin")
	if code != exitcode.PushHookFailed {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitcode.PushHookFailed, stderr)
	}
	var p struct {
		Refs   []json.RawMessage `json:"refs"`
		Atomic *bool             `json:"atomic"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Refs) != 0 {
		t.Errorf("nothing was pushed, so refs must be empty: %d entries", len(p.Refs))
	}
	if p.Atomic == nil || !*p.Atomic {
		t.Errorf("atomic = %v, want true: the stopped push was a two-branch push, which safegit makes atomic", p.Atomic)
	}
}
