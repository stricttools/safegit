package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// oplogPath returns the operation log's path inside a test repo.
func oplogPath(dir string) string {
	return filepath.Join(dir, ".git", "safegit", "log")
}

// corruptOplog appends a line that is not JSON, the shape a crashed or
// half-written append leaves behind.
func corruptOplog(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(oplogPath(dir), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("opening the oplog: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("{\"op\":\"commit\",\"extra\":{\"ref\":\"refs/hea\n"); err != nil {
		t.Fatalf("corrupting the oplog: %v", err)
	}
}

// TestOplogAcceptsOversizedEntry pins the removal of the 4096-byte line cap
// end to end. `scrub match` records its operator-supplied --reason in the
// entry, so a long reason is a caller-reachable way to write an oplog line far
// past the old limit; it used to be dropped with "log entry exceeds 4096
// bytes". The written entry must also read back: doctor's oplog check counts
// unparseable lines, so an OK there means the whole log parsed.
func TestOplogAcceptsOversizedEntry(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "secret.txt", "SECRET=production_key\n")
	safegitCommit(t, dir, "add secret", "secret.txt")

	// 20 KB of reason: five times the old cap.
	reason := "oversized reason " + strings.Repeat("r", 20_000)
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "production_key",
		"--replace", "REDACTED",
		"--entire-history",
		"--reason", reason,
	)
	if code != 0 {
		t.Fatalf("scrub match with a %d-byte reason failed (code %d): %s", len(reason), code, stderr)
	}

	logData, err := os.ReadFile(oplogPath(dir))
	if err != nil {
		t.Fatalf("reading the oplog: %v", err)
	}
	if !strings.Contains(string(logData), strings.Repeat("r", 5_000)) {
		t.Error("the oplog does not carry the oversized entry; it was rejected or truncated")
	}

	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor failed (code %d)", code)
	}
	if !strings.Contains(stdout, "[OK] oplog") {
		t.Errorf("the oversized entry did not read back cleanly:\n%s", stdout)
	}
}

// TestUndoRefusesCorruptedOplog pins the fail-closed contract: undo reverses
// whatever the log says happened last, so a log with unreadable lines is a
// hard error rather than an undo of the wrong operation.
func TestUndoRefusesCorruptedOplog(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "add a", "a.txt")
	head := testutil.Rev(t, dir, "HEAD")

	corruptOplog(t, dir)

	_, stderr, code := runSafegit(t, dir, "undo", "--bypass-session")
	if code == 0 {
		t.Fatal("undo must refuse on an oplog with unparseable lines")
	}
	if !strings.Contains(stderr, "unparseable") {
		t.Errorf("the refusal should name the unparseable lines, got: %s", stderr)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("a refused undo moved HEAD: %s -> %s", head, now)
	}
}

// TestDoctorReportsCorruptedOplog pins both halves of doctor's response to a
// corrupted log: the oplog check reports the skipped-line count, and bypass
// detection reports that it cannot run rather than silently disappearing --
// a corrupted log is exactly when bypass detection is most wanted.
func TestDoctorReportsCorruptedOplog(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "add a", "a.txt")

	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor on a healthy repo failed (code %d)", code)
	}
	if !strings.Contains(stdout, "[OK] oplog") {
		t.Errorf("a healthy repo should report an OK oplog check, got:\n%s", stdout)
	}

	corruptOplog(t, dir)

	stdout, _, code = runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor after corrupting the oplog failed (code %d)", code)
	}
	if !strings.Contains(stdout, "[FAIL] oplog") || !strings.Contains(stdout, "1 unparseable line(s)") {
		t.Errorf("doctor should report the unparseable line count, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "bypass_detect") {
		t.Errorf("bypass detection must still report itself on a corrupted oplog, got:\n%s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "bypass_detect") && strings.HasPrefix(line, "[OK]") {
			t.Errorf("bypass detection reported OK over a corrupted oplog: %s", line)
		}
	}
}
