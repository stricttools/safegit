package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookMigrationRefusesToOverwriteLiveChecks(t *testing.T) {
	dir := newRepo(t)
	old := filepath.Join(legacyHookDir(dir), "pre-pre-push")
	live := filepath.Join(localHookDir(dir), "pre-pre-push")
	writeHookScript(t, old, "echo legacy")
	writeHookScript(t, live, "echo live")
	beforeOld, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	beforeLive, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "hook", "migrate")
	if code == 0 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("collision not refused: code=%d %s", code, stderr)
	}
	afterOld, err := os.ReadFile(old)
	if err != nil || string(afterOld) != string(beforeOld) {
		t.Fatalf("legacy check changed: %q %v", afterOld, err)
	}
	afterLive, err := os.ReadFile(live)
	if err != nil || string(afterLive) != string(beforeLive) {
		t.Fatalf("live check changed: %q %v", afterLive, err)
	}
}
