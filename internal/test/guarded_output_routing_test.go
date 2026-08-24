package test

import (
	"strings"
	"testing"
)

// TestGuardedCommandStreamsToStdoutInHumanMode is the human-mode control for
// the machine-mode routing: at a terminal git's own narration belongs on
// stdout, where an operator reads it, and moving it to stderr for everyone
// would be the wrong fix for a machine-mode problem.
//
// It is the guard against the easy simplification -- capture always, re-emit on
// stderr always -- which would turn every safegit merge's output into stderr
// text and break every human pipeline reading it.
func TestGuardedCommandStreamsToStdoutInHumanMode(t *testing.T) {
	dir := newDivergedBranchRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code == 0 {
		t.Fatalf("fixture is wrong: the conflicting merge succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "CONFLICT") {
		t.Errorf("git's conflict narration is not on stdout in human mode.\n  stdout: %s\n  stderr: %s",
			oneLine(stdout), oneLine(stderr))
	}
}
