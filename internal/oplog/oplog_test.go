package oplog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupSafegitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sgDir := filepath.Join(dir, "safegit")
	os.MkdirAll(sgDir, 0755)
	// Create the log file
	os.WriteFile(filepath.Join(sgDir, "log"), nil, 0644)
	return sgDir
}

func TestAppendAndRead(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entry := Entry{
		Timestamp: time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		PID:       42,
		Op:        "commit",
		Extra: map[string]interface{}{
			"ref": "refs/heads/main",
			"sha": "abc123",
		},
	}

	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.Op != "commit" {
		t.Errorf("Op = %q, want commit", got.Op)
	}
	if got.PID != 42 {
		t.Errorf("PID = %d, want 42", got.PID)
	}
	if ref, ok := got.Extra["ref"].(string); !ok || ref != "refs/heads/main" {
		t.Errorf("Extra[ref] = %v, want refs/heads/main", got.Extra["ref"])
	}
}

func TestAppendAutoFillsTimestampAndPID(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entry := Entry{Op: "test"}
	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.Timestamp.IsZero() {
		t.Error("timestamp should be auto-filled")
	}
	if got.PID == 0 {
		t.Error("PID should be auto-filled")
	}
}

// TestAppendAcceptsOversizedLine pins the removal of the 4096-byte line cap:
// the flock held across the write is the atomicity mechanism, so an entry far
// larger than the POSIX atomic-append size is written and read back intact.
func TestAppendAcceptsOversizedLine(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Well over the old 4096-byte cap, and over bufio's 4096-byte default
	// reader buffer, so a naive line reader would fail on it too.
	bigValue := strings.Repeat("x", 100_000)
	entry := Entry{
		Op:    "test",
		Extra: map[string]interface{}{"big": bigValue},
	}

	if err := Append(sgDir, entry); err != nil {
		t.Fatalf("append of a %d-byte value should succeed: %v", len(bigValue), err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got, _ := entries[0].Extra["big"].(string)
	if got != bigValue {
		t.Errorf("read back %d bytes, want %d", len(got), len(bigValue))
	}
}

// TestReadCountsUnparseableLines pins the skip count Read reports: parseable
// entries still come back, and the corrupted lines are counted rather than
// silently dropped.
func TestReadCountsUnparseableLines(t *testing.T) {
	sgDir := setupSafegitDir(t)

	if err := Append(sgDir, Entry{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}}); err != nil {
		t.Fatal(err)
	}
	// Two corrupted lines: a truncated JSON object (a crash mid-append) and a
	// line of garbage.
	logFile := filepath.Join(sgDir, "log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"op\":\"comm\n" + strings.Repeat("garbage ", 1000) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries, want 1", len(entries))
	}
}

// TestReadParsesFinalLineWithoutNewline covers a log whose last append was
// interrupted before its newline: the line is still parsed if it is valid JSON.
func TestReadParsesFinalLineWithoutNewline(t *testing.T) {
	sgDir := setupSafegitDir(t)

	logFile := filepath.Join(sgDir, "log")
	line := `{"ts":"2026-04-26T12:00:00Z","pid":42,"op":"commit"}`
	if err := os.WriteFile(logFile, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

// TestLastRefUpdateFailsClosedOnSkippedLines pins the fail-closed contract:
// bypass detection and undo arithmetic need a complete log, so a corrupted one
// is an error, never a shorter answer.
func TestLastRefUpdateFailsClosedOnSkippedLines(t *testing.T) {
	sgDir := setupSafegitDir(t)

	if err := Append(sgDir, Entry{Op: "commit", SessionID: "sess-A", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}}); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(sgDir, "log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json at all\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := LastRefUpdate(sgDir, "refs/heads/main"); err == nil {
		t.Error("LastRefUpdate should fail closed on an incomplete log")
	} else if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error should name the unparseable lines, got: %v", err)
	}
}

func TestReadEmptyLog(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestReadNonExistentLog(t *testing.T) {
	dir := t.TempDir()
	sgDir := filepath.Join(dir, "safegit")
	os.MkdirAll(sgDir, 0755)
	// Don't create the log file

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if entries != nil {
		t.Errorf("expected nil entries for non-existent log, got %v", entries)
	}
}

func TestLastRefUpdate(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Append several entries
	entries := []Entry{
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}},
		{Op: "push", Extra: map[string]interface{}{"ref": "refs/heads/main"}},
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/feature", "sha": "bbb"}},
		{Op: "amend", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "ccc"}},
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/feature", "sha": "ddd"}},
	}
	for _, e := range entries {
		if err := Append(sgDir, e); err != nil {
			t.Fatal(err)
		}
	}

	// Last ref update for main should be the amend with sha=ccc
	got, err := LastRefUpdate(sgDir, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if got.Op != "amend" {
		t.Errorf("Op = %q, want amend", got.Op)
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ccc" {
		t.Errorf("sha = %q, want ccc", sha)
	}

	// Last ref update for feature should be commit with sha=ddd
	got, err = LastRefUpdate(sgDir, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ddd" {
		t.Errorf("sha = %q, want ddd", sha)
	}

	// Non-existent ref returns nil
	got, err = LastRefUpdate(sgDir, "refs/heads/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ref")
	}
}

func TestAppendAutoFillsSessionID(t *testing.T) {
	sgDir := setupSafegitDir(t)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-abc-123")

	entry := Entry{Op: "commit"}
	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].SessionID != "sess-abc-123" {
		t.Errorf("SessionID = %q, want sess-abc-123", entries[0].SessionID)
	}
}

func TestAppendSessionIDEmptyWithoutEnv(t *testing.T) {
	sgDir := setupSafegitDir(t)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	entry := Entry{Op: "commit"}
	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty string", entries[0].SessionID)
	}
}

func TestSessionIDJSONRoundTrip(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Write a raw JSON line without "sid" field to simulate an old entry
	logFile := filepath.Join(sgDir, "log")
	oldJSON := `{"ts":"2026-04-26T12:00:00Z","pid":42,"op":"commit","extra":{"ref":"refs/heads/main","sha":"aaa"}}` + "\n"
	if err := os.WriteFile(logFile, []byte(oldJSON), 0644); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty string for old entry without sid", entries[0].SessionID)
	}
	if entries[0].Op != "commit" {
		t.Errorf("Op = %q, want commit", entries[0].Op)
	}
}

func TestConcurrentAppend(t *testing.T) {
	sgDir := setupSafegitDir(t)

	const goroutines = 20
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			entry := Entry{
				Op:    "commit",
				Extra: map[string]interface{}{"id": id},
			}
			if err := Append(sgDir, entry); err != nil {
				t.Errorf("goroutine %d: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	entries, skipped, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(entries) != goroutines {
		t.Errorf("got %d entries, want %d", len(entries), goroutines)
	}
}
