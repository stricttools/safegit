// Package oplog implements the append-only JSONL operation log that records every mutating operation for undo support and audit trail purposes.
// Each entry appends one JSON line to .git/safegit/log under an exclusive
// flock, which is what makes a concurrent append atomic; entries have no size
// limit.
package oplog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/smm-h/safegit/internal/filelock"
)

// Entry represents a single operation log entry.
type Entry struct {
	Timestamp time.Time              `json:"ts"`
	PID       int                    `json:"pid"`
	SessionID string                 `json:"sid,omitempty"`
	Op        string                 `json:"op"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
}

// Path returns the path to the log file. It is exported so callers can name
// the file in an error a human has to go and inspect.
func Path(safegitDir string) string {
	return filepath.Join(safegitDir, "log")
}

// Append writes a single entry to the log file atomically.
// The entry is serialized as a single JSON line of any length: the exclusive
// flock held across the whole write is the atomicity mechanism, so the 4096-byte
// POSIX O_APPEND guarantee is not what this file relies on and no line cap is
// needed. (Same reasoning as the scrub rewrite-map journal, which holds
// arbitrarily large commit maps under the same lock.)
func Append(safegitDir string, entry Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.PID == 0 {
		entry.PID = os.Getpid()
	}
	if entry.SessionID == "" {
		entry.SessionID = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling log entry: %w", err)
	}

	line := append(data[:len(data):len(data)], '\n')

	lp := Path(safegitDir)
	if err := filelock.LockedAppend(lp, line, false); err != nil {
		return fmt.Errorf("appending to log file: %w", err)
	}

	return nil
}

// Read returns all parseable entries from the log file, plus the number of
// non-empty lines it could not parse.
//
// A nonzero skipped count means the log is incomplete: some operation was
// recorded but cannot be read back. Every caller whose correctness depends on
// the log being complete (undo arithmetic, bypass detection) must refuse
// rather than work from a partial history; callers that only summarize the
// log may report the count instead.
//
// Lines are read with a bufio.Reader rather than a bufio.Scanner: entries have
// no size cap, and a Scanner would turn an over-long line into a read error
// for the whole file.
func Read(safegitDir string) ([]Entry, int, error) {
	lp := Path(safegitDir)
	f, err := os.Open(lp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	skipped := 0
	r := bufio.NewReader(f)

	for {
		line, readErr := r.ReadBytes('\n')
		// A final chunk without a trailing newline is still a line worth
		// parsing (a crash mid-append can leave one), so it is handled
		// before the io.EOF exit.
		if len(line) > 0 {
			line = trimEOL(line)
			if len(line) > 0 {
				var e Entry
				if jsonErr := json.Unmarshal(line, &e); jsonErr != nil {
					skipped++
				} else {
					entries = append(entries, e)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return entries, skipped, fmt.Errorf("reading log file: %w", readErr)
		}
	}

	return entries, skipped, nil
}

// trimEOL strips a trailing "\n" or "\r\n" from a line.
func trimEOL(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line
}

// errSkippedLines reports a log whose completeness cannot be established.
func errSkippedLines(skipped int) error {
	return fmt.Errorf("operation log has %d unparseable line(s); it is incomplete and cannot be trusted (inspect .git/safegit/log)", skipped)
}

// LastRefUpdate finds the most recent oplog entry for a given ref that
// records a new tip SHA. It accepts any op type and tries multiple extra
// keys ("sha", "to", "result") since different ops store the new tip
// under different names.
// Returns nil if no matching entry is found. It FAILS CLOSED on an incomplete
// log: bypass detection asks "is the tip the one safegit last wrote", and a
// log missing lines cannot answer that.
//
// Two entry shapes are deliberately passed over rather than answered with:
//
//   - an entry carrying NO new tip. A guarded operation git refused records the
//     ref it did not move and an empty new tip, so the position safegit really
//     last left the branch at is still the one this returns.
//   - an entry recording a ref DELETION (`deleted: true`), which stops the walk
//     with no answer at all: safegit removed the ref on purpose, and everything
//     older describes a ref that no longer exists.
func LastRefUpdate(safegitDir, ref string) (*Entry, error) {
	entries, skipped, err := Read(safegitDir)
	if err != nil {
		return nil, err
	}
	if skipped > 0 {
		return nil, errSkippedLines(skipped)
	}

	// Walk backwards to find the most recent match
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Extra == nil {
			continue
		}
		entryRef, ok := e.Extra["ref"].(string)
		if !ok || entryRef != ref {
			continue
		}
		// safegit DELETED this ref, and every entry beneath this one describes
		// a ref that no longer exists. Reading past it would hand back a tip
		// the ref cannot resolve to and report safegit's own deletion as
		// something that happened behind its back -- which is exactly what
		// doctor did after a root undo. There is no last update to compare
		// against, so the answer is that there is none.
		if deleted, _ := e.Extra["deleted"].(bool); deleted {
			return nil, nil
		}
		// Ensure the entry has a resolvable SHA in one of the known keys
		if hasTipSHA(e.Extra) {
			return &e, nil
		}
	}

	return nil, nil
}

// hasTipSHA returns true if extra contains a new-tip SHA under any of
// the known keys: "sha" (commit/amend/reword), "to" (a navigation),
// "result" (merge).
func hasTipSHA(extra map[string]interface{}) bool {
	for _, key := range []string{"sha", "to", "result"} {
		if v, ok := extra[key].(string); ok && v != "" {
			return true
		}
	}
	return false
}

// TipSHA extracts the new-tip SHA from an oplog entry's extra map.
// It checks "sha", "to", and "result" in order. Returns "" if none found.
func TipSHA(extra map[string]interface{}) string {
	for _, key := range []string{"sha", "to", "result"} {
		if v, ok := extra[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
