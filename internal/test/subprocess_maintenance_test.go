package test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestSafegitSubprocessFetchStartsNoMaintenance: a safegit process this package
// spawns must not start git's background maintenance. `git fetch` starts
// `git maintenance run --auto` after every fetch unless maintenance.auto is
// off, and that process keeps writing into .git after safegit returns, which
// races the removal of the test's temporary directory. The spawned safegit's
// git config must therefore be the throwaway one testisolation wrote for the
// test (which turns maintenance.auto off), not a path that does not exist.
//
// The trace must record the fetch itself, so the absence of a maintenance
// child is not just the absence of a trace.
func TestSafegitSubprocessFetchStartsNoMaintenance(t *testing.T) {
	dir := newBehindRemoteRepo(t)

	trace := filepath.Join(t.TempDir(), "trace2.json")
	_, stderr, code := runSafegitEnv(t, dir, []string{"GIT_TRACE2_EVENT=" + trace},
		"pull", "--merge-strategy", "ff-only", "origin", "main")
	if code != 0 {
		t.Fatalf("safegit pull exited %d: %s", code, stderr)
	}

	f, err := os.Open(trace)
	if err != nil {
		t.Fatalf("the trace was not written: %v", err)
	}
	defer f.Close()

	sawFetch := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unparseable trace line %q: %v", scanner.Text(), err)
		}
		switch ev.Event {
		case "start":
			if slices.Contains(ev.Argv, "fetch") {
				sawFetch = true
			}
		case "child_start":
			if slices.Contains(ev.Argv, "maintenance") {
				t.Errorf("the fetch safegit ran started a maintenance child: %v", ev.Argv)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawFetch {
		t.Fatal("the trace does not record a fetch, so the absence of a maintenance child proves nothing")
	}
}
