package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
)

// rewriteMapsFile is the filename for the JSONL rewrite-map log. Its lines
// hold whole commit maps and have no size limit: the flock held across each
// append is what makes a concurrent write atomic, so the 4096-byte POSIX
// O_APPEND guarantee is not what any of these files rely on. The oplog is
// written the same way, for the same reason.
const rewriteMapsFile = "rewrite-maps.jsonl"

// Rewrite-map record phases. Each rewrite appends up to three lines sharing
// one ID, in this order:
//
//	start    — written at Finalize entry, BEFORE any refs move. Contains the
//	           full commit map and the pre-rewrite remote-tracking state, so a
//	           crash at any later step leaves the mapping recoverable. When the
//	           commit map is all-identity but tags were still rewritten (e.g.
//	           the annotation pass scrubbed a tag body), the start record is
//	           written right after the tag pass instead, with an empty commit
//	           map — refs never move unrecorded. Pure no-ops write no records.
//	refs     — written right after updateRefs and the tag-annotation pass.
//	           Contains every tag rewrite (ref-level and annotation-pass).
//	complete — written after cleanup and HEAD resolution. Contains the new
//	           HEAD and the machine-readable cleanup status.
const (
	rewriteMapPhaseStart    = "start"
	rewriteMapPhaseRefs     = "refs"
	rewriteMapPhaseComplete = "complete"
)

// RewriteMapStart is the phase-"start" record.
type RewriteMapStart struct {
	Phase             string            `json:"phase"`
	ID                string            `json:"id"`
	Op                string            `json:"op"`
	Reason            string            `json:"reason,omitempty"`
	CreatedAt         string            `json:"created_at"`
	OldHead           string            `json:"old_head"`
	CommitMap         map[string]string `json:"commit_map"`
	PreRewriteRemotes map[string]string `json:"pre_rewrite_remotes"`
}

// RewriteMapRefs is the phase-"refs" record.
type RewriteMapRefs struct {
	Phase       string       `json:"phase"`
	ID          string       `json:"id"`
	CreatedAt   string       `json:"created_at"`
	TagRewrites []TagRewrite `json:"tag_rewrites"`
}

// RewriteMapComplete is the phase-"complete" record.
type RewriteMapComplete struct {
	Phase         string   `json:"phase"`
	ID            string   `json:"id"`
	CreatedAt     string   `json:"created_at"`
	NewHead       string   `json:"new_head"`
	CleanupOK     bool     `json:"cleanup_ok"`
	CleanupErrors []string `json:"cleanup_errors"`
}

// rewriteMapsPath returns <sgDir>/rewrite-maps.jsonl.
func rewriteMapsPath(sgDir string) string {
	return filepath.Join(sgDir, rewriteMapsFile)
}

// newRewriteMapID builds a unique ID tying the three phase records of one
// rewrite together: <old-head-prefix>.<random-hex>.
func newRewriteMapID(oldHeadSHA string) string {
	prefix := oldHeadSHA
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to a timestamp-based suffix; uniqueness within one repo
		// only needs to distinguish rewrites, not be cryptographic.
		return fmt.Sprintf("%s.%d", prefix, time.Now().UnixNano())
	}
	return prefix + "." + hex.EncodeToString(buf)
}

// appendRewriteMapRecord marshals v and appends it as one line to
// rewrite-maps.jsonl using the flock-guarded append pattern.
func appendRewriteMapRecord(sgDir string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling rewrite map record: %w", err)
	}
	if err := appendJSONLLine(sgDir, rewriteMapsFile, data); err != nil {
		return fmt.Errorf("writing rewrite map record: %w", err)
	}
	return nil
}

// nowRFC3339 returns the current UTC time in RFC 3339 format.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// nonNilStringMap returns m, or an empty map when m is nil, so JSON output
// serializes as {} instead of null.
func nonNilStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// nonNilStrings returns s, or an empty slice when s is nil, so JSON output
// serializes as [] instead of null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// captureRemoteTrackingState snapshots refs/remotes/* (refname -> SHA) before
// updateRefs rewrites them. updateRefs moves remote-tracking refs to the new
// SHAs, destroying the only local record of what the remote held; orchestrators
// need the pre-rewrite state for --force-with-lease expectations. Symbolic
// refs like refs/remotes/origin/HEAD are skipped (updateRefs skips them too).
func captureRemoteTrackingState(ctx context.Context) (map[string]string, error) {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/")
	if err != nil {
		return nil, fmt.Errorf("listing remote-tracking refs: %w", err)
	}
	remotes := make(map[string]string)
	for _, line := range git.SplitNonEmpty(out) {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refname := parts[0]
		if strings.HasSuffix(refname, "/HEAD") {
			continue
		}
		remotes[refname] = parts[1]
	}
	return remotes, nil
}
