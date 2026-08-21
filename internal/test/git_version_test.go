package test

import (
	"strings"
	"testing"
)

// TestDoctorReportsGitVersion pins the version-floor report: doctor states the
// git it found against the newest floor any safegit feature declares.
func TestDoctorReportsGitVersion(t *testing.T) {
	dir := newRepo(t)

	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor failed (code %d)", code)
	}
	if !strings.Contains(stdout, "git_version") {
		t.Errorf("doctor should report a git_version check, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "feature floor") {
		t.Errorf("the git_version check should name the highest feature floor, got:\n%s", stdout)
	}
}
