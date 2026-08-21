package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Machine mode is the framework's, not safegit's. safegit used to declare its
// own app-global --json, print a hand-rolled JSON document to stdout, and force
// --quiet so its own human text could not corrupt that document. All three are
// gone: --json is framework-owned, the envelope is the sole stdout document,
// and it is written outside the writers --quiet can reach.

// TestMachineModeStdoutIsExactlyTheEnvelope: one document, parsed whole. A
// second document (safegit's own JSON, a summary line, a would-do log) makes
// json.Unmarshal fail, which is the point.
func TestMachineModeStdoutIsExactlyTheEnvelope(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "--json", "author", "list")
	if code != 0 {
		t.Fatalf("author list --json failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.App != "safegit" {
		t.Errorf("envelope app = %q, want safegit", env.App)
	}
	if env.Command == nil || *env.Command != "author.list" {
		t.Errorf("envelope command = %v, want author.list", env.Command)
	}
	if env.ExitCode != 0 {
		t.Errorf("envelope exit_code = %d, want 0", env.ExitCode)
	}
	if env.DryRun {
		t.Error("envelope dry_run = true for a live run")
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(env.Payload, &entries); err != nil {
		t.Fatalf("payload is not the identity list: %v", err)
	}
	if len(entries) == 0 {
		t.Error("payload carries no identities")
	}
}

// TestMachineModeSuppressesTheHumanRendering: the human table prints to stdout
// directly, so machine mode must not produce it -- there is exactly one
// document on that stream.
func TestMachineModeSuppressesTheHumanRendering(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	human, _, code := runSafegit(t, dir, "author", "list")
	if code != 0 {
		t.Fatalf("human author list failed")
	}
	if !strings.Contains(human, "Email") {
		t.Fatalf("the human rendering lost its table header: %q", human)
	}

	machine, _, code := runSafegit(t, dir, "--json", "author", "list")
	if code != 0 {
		t.Fatalf("machine author list failed")
	}
	if strings.Contains(machine, "Email  Role") {
		t.Errorf("the human table leaked into machine mode: %q", machine)
	}
}

// TestMachineModeIsExemptFromQuiet: --quiet governs the human stream only. The
// envelope is not written through the writers quiet suppresses, so it survives
// the combination in full -- payload included.
func TestMachineModeIsExemptFromQuiet(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "--json", "--quiet", "author", "list")
	if code != 0 {
		t.Fatalf("author list --json --quiet failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Errorf("--quiet reached the envelope's payload: %s", stdout)
	}
}

// TestMachineModeFlagIsNotDeclaredBySafegit: --json is reserved by the
// framework at every level. safegit declaring it again is a registration-time
// hard error, so the app must not carry it as a global -- and help must not
// list it among safegit's own globals.
func TestMachineModeFlagIsNotDeclaredBySafegit(t *testing.T) {
	dir := newRepo(t)
	stdout, stderr, code := runSafegit(t, dir, "--help")
	if code != 0 {
		t.Fatalf("--help failed (%d): %s", code, stderr)
	}
	out := stdout + stderr
	if strings.Contains(out, "emit machine-readable JSON output") {
		t.Errorf("safegit still declares its own --json flag:\n%s", out)
	}
}

// TestMachineModeReachesEveryCommand: --json is framework-owned, so it works on
// commands that never had machine output before. `version` reports the same
// three versions in both renderings; `hook list`, which declares no payload,
// still answers with a well-formed envelope carrying a null payload rather than
// with its human text.
func TestMachineModeReachesEveryCommand(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "--json", "version")
	if code != 0 {
		t.Fatalf("version --json failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.Command == nil || *env.Command != "version" {
		t.Errorf("envelope command = %v, want version", env.Command)
	}
	var v map[string]string
	if err := json.Unmarshal(env.Payload, &v); err != nil {
		t.Fatalf("version payload is not an object: %v", err)
	}
	human, _, code := runSafegit(t, dir, "version")
	if code != 0 {
		t.Fatalf("human version failed")
	}
	for _, want := range []string{v["safegit"], v["go"], v["git"]} {
		if !strings.Contains(human, want) {
			t.Errorf("the human rendering does not carry %q:\n%s", want, human)
		}
	}

	stdout, stderr, code = runSafegit(t, dir, "--json", "hook", "list")
	if code != 0 {
		t.Fatalf("hook list --json failed (%d): %s", code, stderr)
	}
	env = decodeEnvelope(t, stdout)
	if string(env.Payload) != "null" {
		t.Errorf("a command that declares no payload must carry null, got %s", env.Payload)
	}
}
