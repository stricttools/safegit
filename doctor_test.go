package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// TestDoctorCheckRegistryIsWellFormed pins the registry's own rules, so a later
// subphase that adds a check gets told immediately when its entry is
// malformed rather than producing a silently mis-reported finding.
func TestDoctorCheckRegistryIsWellFormed(t *testing.T) {
	if len(doctorChecks) == 0 {
		t.Fatal("the doctor check registry is empty")
	}
	seen := map[string]bool{}
	for _, c := range doctorChecks {
		if c.Name == "" {
			t.Error("a registered check has no name")
			continue
		}
		if seen[c.Name] {
			t.Errorf("duplicate check name %q: two checks would report under one name", c.Name)
		}
		seen[c.Name] = true
		switch c.Severity {
		case "warn", "error":
		default:
			t.Errorf("check %q declares severity %q; want warn or error", c.Name, c.Severity)
		}
		if c.Fn == nil {
			t.Errorf("check %q has no function", c.Name)
		}
	}
}

// TestDoctorFindingStatusResolution pins how a finding becomes a reported
// status: ok wins over the declared severity, a plain failure takes the
// declared severity, an explicit status overrides it, and findingNone reports
// nothing at all.
func TestDoctorFindingStatusResolution(t *testing.T) {
	cases := []struct {
		name     string
		severity string
		finding  doctorFinding
		wantRep  bool
		want     string
	}{
		{"ok", "warn", findingOK(""), true, "ok"},
		{"fail takes severity", "warn", findingFail("x"), true, "warn"},
		{"fail takes error severity", "error", findingFail("x"), true, "error"},
		{"override", "warn", findingAt("error", "x"), true, "error"},
		{"none", "warn", findingNone(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reported := resolveFinding(doctorCheck{Name: "x", Severity: tc.severity}, tc.finding)
			if reported != tc.wantRep {
				t.Fatalf("reported = %v, want %v", reported, tc.wantRep)
			}
			if reported && status != tc.want {
				t.Errorf("status = %q, want %q", status, tc.want)
			}
		})
	}
}

// bypassDetectStatus runs checkBypassDetect over the given env and resolves
// its finding exactly as the doctor loop would, using the registered check's
// own severity, so the test asserts what an operator sees.
func bypassDetectStatus(t *testing.T, env doctorEnv) (status string, reported bool, detail string) {
	t.Helper()
	var registered doctorCheck
	for _, c := range doctorChecks {
		if c.Name == "bypass_detect" {
			registered = c
		}
	}
	if registered.Fn == nil {
		t.Fatal("no bypass_detect entry in the doctor check registry")
	}
	f := registered.Fn(env)
	status, reported = resolveFinding(registered, f)
	return status, reported, f.detail
}

// bypassDetectEnv builds a repo with one commit, a safegit dir, and an oplog
// entry recording that commit as the tip safegit last wrote to HEAD's ref. It
// returns the env, the ref name, and a runner for raw git in that repo.
func bypassDetectEnv(t *testing.T) (doctorEnv, string, func(args ...string)) {
	t.Helper()
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "file.txt", "content\n")
	sha := commitAll(t, dir, ctx, "initial")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	ref, err := git.HeadRef(ctx)
	if err != nil {
		t.Fatalf("HeadRef: %v", err)
	}

	gitDir := filepath.Join(dir, ".git")
	sgDir := repo.SafegitDir(gitDir)
	if err := os.MkdirAll(sgDir, 0o755); err != nil {
		t.Fatalf("mkdir safegit dir: %v", err)
	}
	entry := oplog.Entry{Op: "commit", Extra: map[string]interface{}{"ref": ref, "sha": sha}}
	if err := oplog.Append(sgDir, entry); err != nil {
		t.Fatalf("appending oplog entry: %v", err)
	}

	return doctorEnv{ctx: ctx, gitDir: gitDir, sgDir: sgDir, inited: true}, ref, run
}

// TestCheckBypassDetectAgreeingTipIsOK is the control for the two cases below:
// when the ref resolves to the SHA the oplog recorded, the check passes.
func TestCheckBypassDetectAgreeingTipIsOK(t *testing.T) {
	env, _, _ := bypassDetectEnv(t)
	status, reported, detail := bypassDetectStatus(t, env)
	if !reported || status != "ok" {
		t.Fatalf("status = %q, reported = %v, want ok/true (detail: %s)", status, reported, detail)
	}
}

// TestCheckBypassDetectUnresolvableRefIsReported pins that a ref the oplog
// holds a tip for, which no longer resolves, is a REPORTED failure at error
// status rather than a silent findingNone. Deleting the branch out from under
// safegit is precisely the bypass this check exists to notice, so staying
// quiet would disable the check exactly when it has something to say.
func TestCheckBypassDetectUnresolvableRefIsReported(t *testing.T) {
	env, ref, run := bypassDetectEnv(t)

	// Plumbing deletion: HEAD keeps naming the ref (so the HeadRef early-out
	// is not what answers here), but the ref itself is gone.
	run("update-ref", "-d", ref)
	if _, err := git.RevParse(env.ctx, ref); err == nil {
		t.Fatalf("%s still resolves after update-ref -d; the fixture proves nothing", ref)
	}
	if got, err := git.HeadRef(env.ctx); err != nil || got != ref {
		t.Fatalf("HeadRef after deletion = %q, %v; want %q with no error", got, err, ref)
	}

	status, reported, detail := bypassDetectStatus(t, env)
	if !reported {
		t.Fatal("an unresolvable ref the oplog names must be reported, not silently skipped")
	}
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if !strings.Contains(detail, refShortName(ref)) {
		t.Errorf("detail must name the ref %q, got: %s", refShortName(ref), detail)
	}
	if !strings.Contains(detail, "cannot resolve") {
		t.Errorf("detail must say the ref could not be resolved, got: %s", detail)
	}
}

// TestCheckBypassDetectDetachedHeadIsSilent pins the one branch that stays
// findingNone: a detached HEAD names no ref, so there is no precondition to
// check rather than a failure to report.
func TestCheckBypassDetectDetachedHeadIsSilent(t *testing.T) {
	env, ref, run := bypassDetectEnv(t)

	sha, err := git.RevParse(env.ctx, ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	run("checkout", "--detach", sha)
	if _, err := git.HeadRef(env.ctx); err == nil {
		t.Fatal("HeadRef should fail on a detached HEAD; the fixture proves nothing")
	}

	_, reported, detail := bypassDetectStatus(t, env)
	if reported {
		t.Errorf("a detached HEAD must report nothing, got: %s", detail)
	}
}
