package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/smm-h/safegit/internal/filelock"
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
// The sgDir parameter is the .git/safegit directory.
func appendScrubPolicy(sgDir string, policy ScrubPolicy) error {
	if policy.CreatedAt == "" {
		policy.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("marshaling scrub policy: %w", err)
	}

	if err := appendJSONLLine(sgDir, scrubPolicyFile, data); err != nil {
		return fmt.Errorf("writing scrub policy entry: %w", err)
	}
	return nil
}

// appendJSONLLine appends one pre-marshaled JSON line to <sgDir>/<filename>.
// Uses O_APPEND with advisory locking for concurrency safety, the same pattern
// as oplog.Append: the lock, not POSIX append atomicity, is what guarantees
// line integrity, so lines have no size limit.
func appendJSONLLine(sgDir, filename string, data []byte) error {
	line := append(data[:len(data):len(data)], '\n')
	path := filepath.Join(sgDir, filename)
	return filelock.LockedAppend(path, line, true)
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
