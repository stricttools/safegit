package test

import (
	"strings"
	"testing"
)

// TestSessionIDHandshakeDeclaredInHelp: CLAUDE_CODE_SESSION_ID is a cross-tool
// protocol signal that safegit reads (undo ownership, commit trailers). It must
// be a declared handshake env var so it shows up in the Infrastructure section
// of the help output instead of being an undocumented ambient read.
func TestSessionIDHandshakeDeclaredInHelp(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "--help")
	if code != 0 {
		t.Fatalf("--help failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Infrastructure:") {
		t.Errorf("help output has no Infrastructure section:\n%s", stdout)
	}
	if !strings.Contains(stdout, "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("help output does not declare CLAUDE_CODE_SESSION_ID:\n%s", stdout)
	}
}

// TestUndoStillReadsDeclaredSessionID: the declaration must not change undo's
// behaviour — the session ID is still read live from the environment.
func TestUndoStillReadsDeclaredSessionID(t *testing.T) {
	dir := newRepo(t)

	// Without a session ID, undo refuses and names the handshake variable.
	_, stderr, code := runSafegit(t, dir, "undo")
	if code == 0 {
		t.Fatal("undo without a session ID should fail")
	}
	if !strings.Contains(stderr, "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("expected the error to name CLAUDE_CODE_SESSION_ID, got: %s", stderr)
	}
}
