package testutil

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCommitInATestRepositoryStartsNoMaintenance: a commit in a repository
// these helpers build must not start `git maintenance run --auto --detach`.
// That background process keeps writing into .git after the commit returns and
// races the removal of the test's temporary directory ("unlinkat .git:
// directory not empty"). rerere is enabled because an rr-cache directory is
// what makes recent git's automatic maintenance find work to do after a
// commit, which is the shape the sequencer fixtures build.
//
// The trace must record the commit itself, so the absence of a maintenance
// child is not just the absence of a trace.
func TestCommitInATestRepositoryStartsNoMaintenance(t *testing.T) {
	dir := InitBareRepo(t)
	Git(t, dir, "config", "rerere.enabled", "true")
	WriteFile(t, dir, "f.txt", "base\n")
	Git(t, dir, "add", "f.txt")

	trace := filepath.Join(t.TempDir(), "trace2.json")
	if out, code := GitTryEnv(t, dir, []string{"GIT_TRACE2_EVENT=" + trace}, "commit", "-q", "-m", "base"); code != 0 {
		t.Fatalf("git commit exited %d: %s", code, out)
	}

	f, err := os.Open(trace)
	if err != nil {
		t.Fatalf("the trace was not written: %v", err)
	}
	defer f.Close()

	sawCommit := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
			if slices.Contains(ev.Argv, "commit") {
				sawCommit = true
			}
		case "child_start":
			if slices.Contains(ev.Argv, "maintenance") {
				t.Errorf("the commit started a maintenance child: %v", ev.Argv)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawCommit {
		t.Fatal("the trace does not record the commit, so the absence of a maintenance child proves nothing")
	}
}
