package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// scrubPolicyFile is the filename for the JSONL policy log.
const scrubPolicyFile = "scrub-policies.jsonl"

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

// scrubPolicyPath returns the path to the scrub-policies.jsonl file
// at <sgDir>/scrub-policies.jsonl (.git/safegit/scrub-policies.jsonl).
func scrubPolicyPath(sgDir string) string {
	return filepath.Join(sgDir, scrubPolicyFile)
}

// appendScrubPolicy appends a single policy entry to the JSONL policy file.
// Uses O_APPEND with flock for concurrency safety, following the same pattern
// as oplog.Append. The sgDir parameter is the .git/safegit directory.
func appendScrubPolicy(sgDir string, policy ScrubPolicy) error {
	if policy.CreatedAt == "" {
		policy.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("marshaling scrub policy: %w", err)
	}

	line := append(data[:len(data):len(data)], '\n')

	pp := scrubPolicyPath(sgDir)

	// Ensure the .git/safegit/ directory exists.
	if err := os.MkdirAll(sgDir, 0755); err != nil {
		return fmt.Errorf("creating safegit directory: %w", err)
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

// readScrubPolicies reads all policy entries from the JSONL file.
// Returns an empty slice (not an error) if the file does not exist.
func readScrubPolicies(sgDir string) ([]ScrubPolicy, error) {
	pp := scrubPolicyPath(sgDir)
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
