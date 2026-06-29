// Package test contains integration-level concurrency tests that exercise
// safegit as a subprocess (the built binary) under parallel load.
package test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// safegitBin is the path to the built safegit binary, set in TestMain.
var safegitBin string

func TestMain(m *testing.M) {
	// Build safegit binary once for all tests in this package.
	tmpDir, err := os.MkdirTemp("", "safegit-test-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	binPath := filepath.Join(tmpDir, "safegit")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = projectRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build safegit: %v\n%s\n", err, out)
		os.Exit(1)
	}

	safegitBin = binPath
	os.Exit(m.Run())
}

// projectRoot returns the absolute path to the safegit project root.
func projectRoot() string {
	// This test file lives at internal/test/concurrent_test.go
	// so project root is two levels up.
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..")
}

// evalTempDir creates a temp directory and resolves symlinks in the path.
// On macOS, t.TempDir() returns paths like /var/folders/... which is a
// symlink to /private/var/folders/...; git resolves symlinks, causing
// path comparison mismatches in test assertions.
func evalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return resolved
}

// newRepo creates a temp git repo with an initial commit (safegit auto-inits).
func newRepo(t *testing.T) string {
	t.Helper()
	dir := evalTempDir(t)

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Seed file + initial commit
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Bump CAS max attempts high for concurrent tests to avoid exhaustion.
	// (safegit config auto-initializes .git/safegit/ on first run)
	// 200 is enough for TestStress100 (100 parallel commits) with headroom.
	runSafegit(t, dir, "config", "set", "commit.casMaxAttempts", "200")

	return dir
}

// parallel spawns n goroutines calling fn(i) and waits for all to finish.
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			fn(idx)
		}(i)
	}
	wg.Wait()
}

// runSafegit executes the safegit binary in repoDir with the given args.
// Returns stdout, stderr, and exit code.
func runSafegit(t *testing.T, repoDir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(safegitBin, args...)
	cmd.Dir = repoDir

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return
}

// gitLog returns the number of commits on the given ref.
func gitLog(t *testing.T, dir, ref string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --count %s: %v", ref, err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// T1: 10 tracked files, 10 parallel safegit commit invocations each committing
// their own file. All 10 should succeed. Verify 10 commits land on main.
func TestStageDifferentFiles(t *testing.T) {
	dir := newRepo(t)

	const N = 10

	// Create 10 distinct files
	for i := 0; i < N; i++ {
		fname := fmt.Sprintf("file%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("content %d\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	beforeCount := gitLog(t, dir, "main")

	// Run N parallel commits, each committing its own file
	results := make([]string, N)
	errors := make([]string, N)
	codes := make([]int, N)

	parallel(N, func(i int) {
		fname := fmt.Sprintf("file%02d.txt", i)
		msg := fmt.Sprintf("commit file %d", i)
		stdout, stderr, code := runSafegit(t, dir, "commit", "-m", msg, "--", fname)
		results[i] = stdout
		errors[i] = stderr
		codes[i] = code
	})

	// All 10 should succeed
	failCount := 0
	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			failCount++
			t.Errorf("commit %d failed (code %d): stdout=%s stderr=%s", i, codes[i], results[i], errors[i])
		}
	}
	if failCount > 0 {
		t.Fatalf("%d/%d commits failed", failCount, N)
	}

	// Verify 10 new commits landed
	afterCount := gitLog(t, dir, "main")
	newCommits := afterCount - beforeCount
	if newCommits != N {
		t.Errorf("expected %d new commits on main, got %d", N, newCommits)
	}

	// Verify all files are in the HEAD tree
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	treeFiles := strings.Split(strings.TrimSpace(string(out)), "\n")
	fileSet := make(map[string]bool)
	for _, f := range treeFiles {
		fileSet[f] = true
	}
	for i := 0; i < N; i++ {
		fname := fmt.Sprintf("file%02d.txt", i)
		if !fileSet[fname] {
			t.Errorf("file %s not found in HEAD tree", fname)
		}
	}
}

// T2: 10 branches, 10 parallel commits each to their own branch. All succeed,
// no lock contention (different refs -> different lock files).
func TestCommitDifferentBranches(t *testing.T) {
	dir := newRepo(t)

	const N = 10

	// Create N branches and N files
	for i := 0; i < N; i++ {
		branchName := fmt.Sprintf("branch%02d", i)
		cmd := exec.Command("git", "branch", branchName)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", branchName, err, out)
		}

		fname := fmt.Sprintf("file%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("content %d\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Run N parallel commits, each to its own branch
	results := make([]string, N)
	errs := make([]string, N)
	codes := make([]int, N)

	parallel(N, func(i int) {
		branchName := fmt.Sprintf("branch%02d", i)
		fname := fmt.Sprintf("file%02d.txt", i)
		msg := fmt.Sprintf("commit to %s", branchName)
		stdout, stderr, code := runSafegit(t, dir, "commit", "-m", msg, "--branch", branchName, "--", fname)
		results[i] = stdout
		errs[i] = stderr
		codes[i] = code
	})

	// All should succeed
	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			t.Errorf("commit %d to branch%02d failed (code %d): stdout=%s stderr=%s", i, i, codes[i], results[i], errs[i])
		}
	}

	// Verify each branch has exactly one new commit
	for i := 0; i < N; i++ {
		branchName := fmt.Sprintf("branch%02d", i)
		ref := "refs/heads/" + branchName
		count := gitLog(t, dir, ref)
		// Initial commit (1) + seed (initial) + new commit = 2
		// Actually the newRepo has 1 commit ("initial"), so the branch should have 2 total
		if count != 2 {
			t.Errorf("branch %s has %d commits, expected 2", branchName, count)
		}
	}
}

// T3: 10 parallel commits to main, all committing their own file.
// All 10 should land linearly on main. Git log should show 10 new commits.
func TestCommitSameBranch(t *testing.T) {
	dir := newRepo(t)

	const N = 10

	// Create N files
	for i := 0; i < N; i++ {
		fname := fmt.Sprintf("concurrent%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("data %d\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	beforeCount := gitLog(t, dir, "main")

	codes := make([]int, N)
	stdouts := make([]string, N)
	stderrs := make([]string, N)

	parallel(N, func(i int) {
		fname := fmt.Sprintf("concurrent%02d.txt", i)
		msg := fmt.Sprintf("concurrent commit %d", i)
		stdout, stderr, code := runSafegit(t, dir, "commit", "-m", msg, "--", fname)
		stdouts[i] = stdout
		stderrs[i] = stderr
		codes[i] = code
	})

	// All should succeed
	failCount := 0
	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			failCount++
			t.Errorf("commit %d failed (code %d): stdout=%s stderr=%s", i, codes[i], stdouts[i], stderrs[i])
		}
	}

	if failCount > 0 {
		t.Fatalf("%d/%d commits failed", failCount, N)
	}

	// Verify N new commits on main
	afterCount := gitLog(t, dir, "main")
	newCommits := afterCount - beforeCount
	if newCommits != N {
		t.Errorf("expected %d new commits on main, got %d", N, newCommits)
	}

	// Verify linear history (no merges) by checking each commit has exactly 1 parent
	cmd := exec.Command("git", "log", "--format=%H %P", fmt.Sprintf("-%d", N), "main")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Errorf("commit %s has %d parents (expected 1): %q", parts[0][:8], len(parts)-1, line)
		}
	}
}

// T4: Stale lock recovery. Create a lock file with a dead PID, then verify
// the next safegit commit can acquire the lock and succeed.
func TestKillMidCommit(t *testing.T) {
	dir := newRepo(t)

	// Write a file to commit
	if err := os.WriteFile(filepath.Join(dir, "recover.txt"), []byte("recover\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Start a helper process that just sleeps, get its PID, then kill it
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperPID := helper.Process.Pid
	helper.Process.Kill()
	helper.Wait()

	// Create a lock file manually with the dead PID
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	os.MkdirAll(lockDir, 0755)
	lockFile := filepath.Join(lockDir, "main.lock")
	hostname, _ := os.Hostname()
	lockContent := fmt.Sprintf("pid=%d\nts=%s\nop=commit\nhost=%s\n",
		helperPID, time.Now().UTC().Format(time.RFC3339Nano), hostname)
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify lock file exists
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}

	// Now run safegit commit -- it should detect the dead PID and recover
	start := time.Now()
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "recovered", "--", "recover.txt")
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("commit failed (code %d) after stale lock: stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Should have been fast (< 5s, well under the 30s lock timeout)
	if elapsed > 5*time.Second {
		t.Errorf("commit took %v, expected fast recovery from stale lock", elapsed)
	}

	// Lock file should be gone
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Error("lock file still exists after successful commit")
	}
}

// T5: Create orphan tmp dirs with dead PIDs, run doctor --fix, verify cleanup.
func TestTmpDirGc(t *testing.T) {
	dir := newRepo(t)

	// Start a helper process, capture its PID, then kill it
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := helper.Process.Pid
	helper.Process.Kill()
	helper.Wait()

	// Create orphan tmp dirs using the dead PID
	tmpBase := filepath.Join(dir, ".git", "safegit", "tmp")
	for i := 0; i < 3; i++ {
		orphanDir := filepath.Join(tmpBase, fmt.Sprintf("%d-deadbeef%d", deadPID, i))
		if err := os.MkdirAll(orphanDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Write a dummy index file inside
		if err := os.WriteFile(filepath.Join(orphanDir, "index"), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Verify they exist
	entries, _ := os.ReadDir(tmpBase)
	if len(entries) < 3 {
		t.Fatalf("expected at least 3 tmp dirs, got %d", len(entries))
	}

	// Run doctor --fix (replaces the former gc subcommand)
	stdout, stderr, code := runSafegit(t, dir, "doctor", "--fix")
	if code != 0 {
		t.Fatalf("doctor --fix failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify dirs are gone
	entries, _ = os.ReadDir(tmpBase)
	for _, e := range entries {
		if strings.Contains(e.Name(), fmt.Sprintf("%d-", deadPID)) {
			t.Errorf("orphan dir %s still exists after gc", e.Name())
		}
	}
}

// T8: Create a lock file with a dead PID, verify next acquire replaces it quickly.
func TestStaleLockReplace(t *testing.T) {
	dir := newRepo(t)

	// Create a file to commit
	if err := os.WriteFile(filepath.Join(dir, "fast.txt"), []byte("fast\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Start+kill a process to get a dead PID
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := helper.Process.Pid
	helper.Process.Kill()
	helper.Wait()

	// Plant a stale lock
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	os.MkdirAll(lockDir, 0755)
	lockFile := filepath.Join(lockDir, "main.lock")
	hostname2, _ := os.Hostname()
	lockContent := fmt.Sprintf("pid=%d\nts=%s\nop=commit\nhost=%s\n",
		deadPID, time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), hostname2)
	os.WriteFile(lockFile, []byte(lockContent), 0644)

	// Time the commit -- should be fast (immediate stale detection + replacement)
	start := time.Now()
	_, _, code := runSafegit(t, dir, "commit", "-m", "fast recovery", "--", "fast.txt")
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("commit failed (code %d) with stale lock", code)
	}

	// Should be much faster than the 30s lock timeout (< 3s is generous)
	if elapsed > 3*time.Second {
		t.Errorf("stale lock recovery took %v, expected < 3s", elapsed)
	}
}

// T10: Run several parallel commits, verify op log has the correct number of
// commit entries, all parseable as JSON.
func TestOpLogIntegrity(t *testing.T) {
	dir := newRepo(t)

	const N = 10

	// Create files
	for i := 0; i < N; i++ {
		fname := fmt.Sprintf("oplog%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("oplog %d\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Run N parallel commits
	codes := make([]int, N)
	parallel(N, func(i int) {
		fname := fmt.Sprintf("oplog%02d.txt", i)
		msg := fmt.Sprintf("oplog commit %d", i)
		_, _, code := runSafegit(t, dir, "commit", "-m", msg, "--", fname)
		codes[i] = code
	})

	// All should succeed
	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			t.Errorf("commit %d failed with code %d", i, codes[i])
		}
	}

	// Read and verify the op log
	logPath := filepath.Join(dir, ".git", "safegit", "log")
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4096)

	commitEntries := 0
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Every line must be valid JSON
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nraw: %s", lineNum, err, line)
			continue
		}

		// Check required fields
		if _, ok := entry["ts"]; !ok {
			t.Errorf("line %d missing 'ts' field", lineNum)
		}
		if _, ok := entry["pid"]; !ok {
			t.Errorf("line %d missing 'pid' field", lineNum)
		}
		if _, ok := entry["op"]; !ok {
			t.Errorf("line %d missing 'op' field", lineNum)
		}

		if entry["op"] == "commit" {
			commitEntries++
			// Verify commit entries have expected extra fields
			extra, _ := entry["extra"].(map[string]interface{})
			if extra == nil {
				t.Errorf("line %d: commit entry missing 'extra' map", lineNum)
				continue
			}
			for _, key := range []string{"ref", "sha", "parent", "tree", "attempts"} {
				if _, ok := extra[key]; !ok {
					t.Errorf("line %d: commit entry missing extra.%s", lineNum, key)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning log file: %v", err)
	}

	if commitEntries != N {
		t.Errorf("expected %d commit entries in oplog, got %d", N, commitEntries)
	}
}

// T15: Dirty working tree, checkout refused with code 5.
func TestCheckoutRefusedDirty(t *testing.T) {
	dir := newRepo(t)

	// Create a branch to checkout to
	cmd := exec.Command("git", "branch", "other")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	// Modify a tracked file to make the working tree "dirty" in safegit's eyes.
	// safegit's coord guard uses `git status --porcelain` which shows modifications.
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("dirty content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// safegit checkout should refuse
	stdout, stderr, code := runSafegit(t, dir, "checkout", "other")
	if code != 5 {
		t.Errorf("expected exit code 5 (dirty tree), got %d: stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify we're still on main (checkout didn't happen)
	headCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	headCmd.Dir = dir
	headOut, _ := headCmd.Output()
	if strings.TrimSpace(string(headOut)) != "main" {
		t.Errorf("HEAD moved to %s despite refusal", strings.TrimSpace(string(headOut)))
	}
}

// TestDoctorFixCleansMainRepoStaleLocks verifies that `doctor --fix` removes
// stale lock files from the main repo's safegit directory (not just submodules).
func TestDoctorFixCleansMainRepoStaleLocks(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	// newRepo runs `safegit config set` which auto-initializes .git/safegit/.
	// Plant a stale lock file with a PID that cannot exist.
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	os.MkdirAll(lockDir, 0755)
	lockFile := filepath.Join(lockDir, "main.lock")
	hostname, _ := os.Hostname()
	lockContent := fmt.Sprintf("pid=999999999\nts=%s\nop=commit\nhost=%s\n",
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), hostname)
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify the lock file exists before running doctor.
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		t.Fatal("stale lock file not created")
	}

	// Run doctor --fix --yes.
	stdout, stderr, code := runSafegit(t, dir, "doctor", "--fix", "--yes")
	if code != 0 {
		t.Fatalf("doctor --fix failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// The lock file should be gone.
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Errorf("stale lock file still exists at %s", lockFile)
	}

	// Output should mention stale lock cleanup.
	if !strings.Contains(stdout, "stale lock") {
		t.Errorf("doctor --fix output should mention stale lock cleanup, got: %s", stdout)
	}
}

// TestDoctorDiagnosesMainRepoStaleLocks verifies that `doctor` (without --fix)
// reports stale locks in the main repo's safegit directory.
func TestDoctorDiagnosesMainRepoStaleLocks(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	// Plant a stale lock file.
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	os.MkdirAll(lockDir, 0755)
	lockFile := filepath.Join(lockDir, "main.lock")
	hostname, _ := os.Hostname()
	lockContent := fmt.Sprintf("pid=999999999\nts=%s\nop=commit\nhost=%s\n",
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), hostname)
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run doctor --diagnose (no --fix).
	stdout, _, code := runSafegit(t, dir, "doctor", "--diagnose")
	if code != 0 {
		t.Fatalf("doctor --diagnose failed (code %d)", code)
	}

	// Output should report stale lock(s).
	if !strings.Contains(stdout, "stale lock") {
		t.Errorf("doctor output should report stale lock(s), got: %s", stdout)
	}

	// The lock file should still exist (doctor without --fix doesn't remove).
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		t.Errorf("lock file was removed by doctor without --fix")
	}
}
