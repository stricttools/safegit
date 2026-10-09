package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// countingGit installs a git wrapper that records every git process safegit
// starts, one argv per line, and returns the PATH entry that puts it first and
// the file it records to.
func countingGit(t *testing.T) (pathEnv, logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "starts.log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> '%s'\nexec '%s' \"$@\"\n", logPath, realGit)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), logPath
}

// gitStarts reads the wrapper's record: the total and a per-subcommand count.
func gitStarts(t *testing.T, logPath string) (int, string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		total++
		sub := "?"
		for _, f := range strings.Fields(line) {
			if !strings.HasPrefix(f, "-") && !strings.Contains(f, "=") {
				sub = f
				break
			}
		}
		counts[sub]++
	}
	var parts []string
	for sub, n := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", sub, n))
	}
	sort.Strings(parts)
	return total, strings.Join(parts, " ")
}

// TestScrubMatchRemapCostIsBoundedPerCommit pins how many git processes an
// entire-history `scrub match --remap-shas-in` starts. A rewrite used to read
// every tree of every commit with its own `git ls-tree` while remapping, and
// every commit, tree, and blob with its own `git cat-file`, so the process
// count grew with commits times directories: a few thousand commits took over
// ten minutes. Reads now go through one long-running cat-file, tree writes
// through one long-running mktree, and the remap descends only into the
// directories its globs can reach, so the count grows with commits alone.
//
// The fixture has many directories, so the old cost shows up as a multiple of
// the bound; the bound sits just above what the rewrite needs now.
func TestScrubMatchRemapCostIsBoundedPerCommit(t *testing.T) {
	dir := newRepo(t)
	const commits = 40
	const dirs = 12

	for d := 0; d < dirs; d++ {
		testutil.WriteFile(t, dir, fmt.Sprintf("pkg%02d/inner/leaf/file.txt", d), fmt.Sprintf("content %d\n", d))
	}
	testutil.WriteFile(t, dir, "pkg00/inner/leaf/secret.txt", "token hunter2 here\n")
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "commit", "-q", "-m", "initial with hunter2 in the message")

	const changelog = ".meta/changelog/proj/changes.jsonl"
	var lines strings.Builder
	for i := 1; i < commits; i++ {
		prev := testutil.Rev(t, dir, "HEAD")
		fmt.Fprintf(&lines, "{\"commits\":[%q]}\n", prev)
		testutil.WriteFile(t, dir, changelog, lines.String())
		testutil.WriteFile(t, dir, fmt.Sprintf("pkg%02d/inner/leaf/file.txt", i%dirs), fmt.Sprintf("content %d\n", i))
		testutil.Git(t, dir, "add", "-A")
		testutil.Git(t, dir, "commit", "-q", "-m", fmt.Sprintf("change %d", i))
	}

	pathEnv, logPath := countingGit(t)
	env := append(append([]string(nil), scrubEnv...), pathEnv)
	stdout, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "--json", "scrub", "match",
		"--pattern", "hunter2", "--replace", "REDACTED", "--entire-history",
		"--remap-shas-in", ".meta/changelog/*/*.jsonl", "--reason", "cost bound")
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s\n%s", code, stderr, stdout)
	}
	var result scrubFileJSON
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	if len(result.Rewrites) != commits {
		t.Fatalf("rewrote %d commits, want %d", len(result.Rewrites), commits)
	}
	assertChangelogSelfConsistent(t, dir, changelog, result.Rewrites)

	total, breakdown := gitStarts(t, logPath)
	// Two per commit (its commit-tree, and the hash-object of its remapped
	// changelog) plus the fixed work of a rewrite, a little above what the
	// rewrite measured. Before reads, tree writes, and existence checks were
	// batched, the same rewrite started about twenty times as many.
	const bound = 2*commits + 50
	if total > bound {
		t.Errorf("scrub match over %d commits started %d git processes, above the bound of %d (%s)", commits, total, bound, breakdown)
	}
	t.Logf("git processes: %d (%s)", total, breakdown)
}
