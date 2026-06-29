package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// scrubPolicyFile is the filename for the JSONL policy log.
const scrubPolicyFile = "scrub-policies.jsonl"

// scrubPolicyDir is the working-tree directory that holds tracked policy files.
const scrubPolicyDir = ".safegit"

// ScrubPolicy records a scrub operation's pattern so that future verification
// can confirm the secret remains absent from the object store.
type ScrubPolicy struct {
	Type        string `json:"type"`                    // "match"
	Pattern     string `json:"pattern"`                 // regex string
	Scope       string `json:"scope,omitempty"`         // glob, optional
	Reason      string `json:"reason"`                  // audit trail
	CreatedAt   string `json:"created_at"`              // ISO 8601
	CreatedByOp string `json:"created_by_op,omitempty"` // oplog operation name
}

// scrubPolicyPath returns the path to the tracked scrub-policies.jsonl file
// at <repoRoot>/.safegit/scrub-policies.jsonl.
func scrubPolicyPath(repoRoot string) string {
	return filepath.Join(repoRoot, scrubPolicyDir, scrubPolicyFile)
}

// oldScrubPolicyPath returns the legacy path at .git/safegit/scrub-policies.jsonl.
func oldScrubPolicyPath(sgDir string) string {
	return filepath.Join(sgDir, scrubPolicyFile)
}

// appendScrubPolicy appends a single policy entry to the JSONL policy file.
// Uses O_APPEND with flock for concurrency safety, following the same pattern
// as oplog.Append. The repoRoot parameter is the repository working tree root.
func appendScrubPolicy(repoRoot string, policy ScrubPolicy) error {
	if policy.CreatedAt == "" {
		policy.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("marshaling scrub policy: %w", err)
	}

	line := append(data[:len(data):len(data)], '\n')

	pp := scrubPolicyPath(repoRoot)

	// Ensure the .safegit/ directory exists.
	if err := os.MkdirAll(filepath.Dir(pp), 0755); err != nil {
		return fmt.Errorf("creating .safegit directory: %w", err)
	}

	f, err := os.OpenFile(pp, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("opening scrub policy file: %w", err)
	}
	defer f.Close()

	// Advisory lock for safety on NFS or non-POSIX filesystems.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking scrub policy file: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing scrub policy entry: %w", err)
	}

	return nil
}

// migrateScrubPolicies copies policy entries from the old location
// (.git/safegit/scrub-policies.jsonl) to the new tracked location
// (<repoRoot>/.safegit/scrub-policies.jsonl). It prints an informational
// message and is idempotent: once the new file exists migration is skipped.
func migrateScrubPolicies(repoRoot, sgDir string) error {
	newPath := scrubPolicyPath(repoRoot)
	oldPath := oldScrubPolicyPath(sgDir)

	// If the new file already exists, nothing to migrate.
	if _, err := os.Stat(newPath); err == nil {
		return nil
	}

	// If the old file doesn't exist either, nothing to do.
	oldF, err := os.Open(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opening legacy scrub policy file: %w", err)
	}
	defer oldF.Close()

	// Ensure the .safegit/ directory exists.
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return fmt.Errorf("creating .safegit directory: %w", err)
	}

	newF, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			// Race: another process created it between our Stat and OpenFile.
			return nil
		}
		return fmt.Errorf("creating new scrub policy file: %w", err)
	}
	defer newF.Close()

	if _, err := io.Copy(newF, oldF); err != nil {
		return fmt.Errorf("copying scrub policies to new location: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Migrated scrub policies from %s to %s\n", oldPath, newPath)
	return nil
}

// readScrubPolicies reads all policy entries from the JSONL file.
// Returns an empty slice (not an error) if the file does not exist.
// The sgDir parameter is used for migration from the old location; pass ""
// to skip migration.
func readScrubPolicies(repoRoot, sgDir string) ([]ScrubPolicy, error) {
	// Migrate from old location if needed.
	if sgDir != "" {
		if err := migrateScrubPolicies(repoRoot, sgDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: migration of scrub policies failed: %v\n", err)
		}
	}

	pp := scrubPolicyPath(repoRoot)
	f, err := os.Open(pp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening scrub policy file: %w", err)
	}
	defer f.Close()

	var policies []ScrubPolicy
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var p ScrubPolicy
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("parsing scrub policy line %d: %w", lineNum, err)
		}
		policies = append(policies, p)
	}

	if err := scanner.Err(); err != nil {
		return policies, fmt.Errorf("reading scrub policy file: %w", err)
	}

	return policies, nil
}
