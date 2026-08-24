package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `doctor --action fix` cleans each submodule's own safegit state directory, and
// it asks the submodule enumeration which those are. A failed enumeration used
// to print a warning and then clean nothing at all: the scope of the repair went
// silently empty while the run reported the repairs it did make and exited on
// what it found elsewhere.
//
// The enumeration failure is an error-severity FINDING now -- a repository whose
// submodules safegit cannot read is one safegit cannot look after, and the
// nonzero exit is what an agent or script reads. `--action fix` says on stderr
// that no submodule state was cleaned, rather than filing it as advice.
func unenumerableSubmodulesDoctorRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	// A regular file where .git/modules would be: enumeration reads that
	// directory and cannot.
	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "modules"), "not a directory\n")
	return dir
}

func TestDoctorReportsAnUnreadableSubmoduleEnumerationAsAnError(t *testing.T) {
	dir := unenumerableSubmodulesDoctorRepo(t)

	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != exitcode.DoctorFindings {
		t.Fatalf("doctor exited %d, want %d (DoctorFindings):\n%s", code, exitcode.DoctorFindings, stdout)
	}
	line := doctorFindingLine(t, stdout, "submodules")
	if !strings.HasPrefix(line, "[FAIL]") {
		t.Errorf("the enumeration failure is not reported as an error: %s", line)
	}
	if !strings.Contains(line, "enumerating") {
		t.Errorf("the finding does not say what could not be read: %s", line)
	}
}

// A repository whose submodules enumerate cleanly is the control: the check
// reports nothing to answer for, and doctor still exits 0.
func TestDoctorSaysNothingWhenSubmodulesEnumerate(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor exited %d on a healthy repository:\n%s\n%s", code, stdout, stderr)
	}
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "submodules") && strings.HasPrefix(l, "[FAIL]") {
			t.Errorf("a repository with no submodules got a failing finding: %s", l)
		}
	}
}

// The fix path carries the same verdict: the cleanup scope it could not
// determine is stated as an error, and the run still exits on the finding.
func TestDoctorFixSaysNoSubmoduleStateWasCleaned(t *testing.T) {
	dir := unenumerableSubmodulesDoctorRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "fix")
	if code != exitcode.DoctorFindings {
		t.Fatalf("doctor --action fix exited %d, want %d (DoctorFindings):\n%s\n%s",
			code, exitcode.DoctorFindings, stdout, stderr)
	}
	if !strings.Contains(stderr, "error: enumerating submodules") {
		t.Errorf("the fix path does not state the failure as an error; stderr: %s", stderr)
	}
	if strings.Contains(stderr, "warning: enumerating submodules") {
		t.Errorf("the fix path still files the failure as advice; stderr: %s", stderr)
	}
}

// doctorFindingLine returns the reported line for one check name.
func doctorFindingLine(t *testing.T, stdout, check string) string {
	t.Helper()
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "] "+check) {
			return l
		}
	}
	t.Fatalf("doctor reported no %q finding:\n%s", check, stdout)
	return ""
}
