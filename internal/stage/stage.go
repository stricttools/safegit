// Package stage implements hunk-level staging against temporary indexes by parsing unified diffs and applying selective patches for partial-file commits.
package stage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// ErrBinaryFile reports that a hunk spec was given for a file git reports as
// binary, where only whole-file staging exists. It is exported because the
// refusal has its own exit code (exitcode.BinaryHunkSpec): the commit pipeline
// recognizes it with errors.Is and reports it as that code rather than as an
// undifferentiated failure.
var ErrBinaryFile = errors.New("binary file: hunk staging not supported")

// Hunk represents a single change block from a unified diff.
type Hunk struct {
	Index    int // 1-based
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Header   string   // the @@ line
	Body     []string // the +/- /space lines
}

// ExtractHunks diffs the working tree against a tmp index for a single file.
// Returns the diff header lines and parsed hunks.
func ExtractHunks(ctx context.Context, indexPath, file string) (header []string, hunks []Hunk, err error) {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	// --no-color --no-ext-diff --no-renames ensures stable, parseable output
	out, _, diffErr := git.RunWithEnv(ctx, env, "diff", "--no-color", "--no-ext-diff", "--no-renames", "--", file)

	if diffErr != nil {
		// git diff exits 1 when there are differences -- that's normal.
		// Only fail if there's no output at all (real error).
		if out == "" {
			return nil, nil, fmt.Errorf("git diff failed for %s: %w", file, diffErr)
		}
	}

	if out == "" {
		// No differences
		return nil, nil, nil
	}

	// Check for binary file
	if strings.Contains(out, "Binary files") {
		return nil, nil, ErrBinaryFile
	}

	header, hunks = ParseDiff(out)
	return header, hunks, nil
}

// ParseDiff splits unified diff output into header lines and hunks.
// It handles a single file's diff block (one "diff --git" header + hunks).
func ParseDiff(raw string) (header []string, hunks []Hunk) {
	lines := strings.Split(raw, "\n")
	// Remove trailing empty line from split
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	i := 0
	// Collect header lines (diff --git, index, ---, +++)
	for i < len(lines) {
		if strings.HasPrefix(lines[i], "@@") {
			break
		}
		header = append(header, lines[i])
		i++
	}

	// Parse hunks
	hunkIdx := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
		hunkIdx++
		h := Hunk{
			Index:  hunkIdx,
			Header: lines[i],
		}
		h.OldStart, h.OldCount, h.NewStart, h.NewCount = parseHunkHeader(lines[i])
		i++

		// Collect body lines until next hunk or EOF
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			h.Body = append(h.Body, lines[i])
			i++
		}

		hunks = append(hunks, h)
	}
	return header, hunks
}

// parseHunkHeader extracts start/count from "@@ -old_start,old_count +new_start,new_count @@".
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int) {
	// Format: @@ -A,B +C,D @@ optional context
	// or:    @@ -A +C @@  (count=1 implied)
	atIdx := strings.Index(line, "@@")
	if atIdx < 0 {
		return
	}
	rest := line[atIdx+2:]
	endAt := strings.Index(rest, "@@")
	if endAt < 0 {
		return
	}
	range_ := strings.TrimSpace(rest[:endAt])
	parts := strings.Fields(range_)
	if len(parts) < 2 {
		return
	}

	// Parse -A,B
	old := strings.TrimPrefix(parts[0], "-")
	oldStart, oldCount = parseRange(old)

	// Parse +C,D
	new_ := strings.TrimPrefix(parts[1], "+")
	newStart, newCount = parseRange(new_)
	return
}

// parseRange parses "start,count" or "start" (count=1 implied).
func parseRange(s string) (start, count int) {
	if idx := strings.IndexByte(s, ','); idx >= 0 {
		start, _ = strconv.Atoi(s[:idx])
		count, _ = strconv.Atoi(s[idx+1:])
	} else {
		start, _ = strconv.Atoi(s)
		count = 1
	}
	return
}

// BuildPatch builds a synthetic unified diff from header + selected hunks.
// selected is a slice of 1-based hunk indices.
func BuildPatch(header []string, hunks []Hunk, selected []int) ([]byte, error) {
	if len(selected) == 0 {
		return nil, errors.New("no hunks selected")
	}

	selSet := make(map[int]bool, len(selected))
	for _, idx := range selected {
		selSet[idx] = true
	}

	var b strings.Builder
	for _, h := range header {
		b.WriteString(h)
		b.WriteByte('\n')
	}

	for _, h := range hunks {
		if !selSet[h.Index] {
			continue
		}
		b.WriteString(h.Header)
		b.WriteByte('\n')
		for _, line := range h.Body {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	return []byte(b.String()), nil
}

// ApplyPatch applies a patch to a tmp index using git apply --cached.
//
// A patch that does not apply is a hard error. It used to be answered by
// silently running the same patch again with --3way, which is not the same
// staging action: the direct apply stages exactly the hunks the caller
// selected, while the three-way retry MERGES the patch into whatever the index
// holds. A caller who asked for one hunk could be given a merge result nobody
// named, and nothing in the output said a retry had happened.
//
// The error carries git's own reason and no path: the caller attaches the
// repo-relative spelling (internal/commit's stagingHunksError), which is the
// only spelling safegit says back to a caller -- this package is handed the
// absolute path.
func ApplyPatch(ctx context.Context, indexPath string, patch []byte) error {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if err := gitApply(ctx, env, patch); err != nil {
		return fmt.Errorf("patch apply failed: %w", err)
	}
	return nil
}

// gitApply runs git apply --cached.
func gitApply(ctx context.Context, env []string, patch []byte) error {
	args := []string{"apply", "--cached", "--recount", "--whitespace=nowarn", "-"}

	_, _, err := git.RunWithEnvStdin(ctx, env, patch, args...)
	return err
}

// StageHunks stages only specific hunks of a file into a tmp index.
// hunkIndices are 1-based.
func StageHunks(ctx context.Context, indexPath, file string, hunkIndices []int) error {
	header, hunks, err := ExtractHunks(ctx, indexPath, file)
	if err != nil {
		return fmt.Errorf("extracting hunks: %w", err)
	}
	if len(hunks) == 0 {
		return errors.New("no hunks found (file has no changes)")
	}

	// Validate indices
	for _, idx := range hunkIndices {
		if idx < 1 || idx > len(hunks) {
			return fmt.Errorf("hunk index %d out of range (file has %d hunks)", idx, len(hunks))
		}
	}

	patch, err := BuildPatch(header, hunks, hunkIndices)
	if err != nil {
		return fmt.Errorf("building patch: %w", err)
	}

	return ApplyPatch(ctx, indexPath, patch)
}

// ParseHunkSpec parses a hunk specifier string like "1,3,5" or "2-4" or "1,3-5".
// Returns 1-based hunk indices.
func ParseHunkSpec(spec string) ([]int, error) {
	if spec == "" {
		return nil, errors.New("empty hunk specifier")
	}

	var indices []int
	parts := strings.Split(spec, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if dashIdx := strings.IndexByte(part, '-'); dashIdx >= 0 {
			// Range: "2-4"
			startStr := part[:dashIdx]
			endStr := part[dashIdx+1:]
			start, err := strconv.Atoi(startStr)
			if err != nil {
				return nil, fmt.Errorf("invalid hunk range %q: %w", part, err)
			}
			end, err := strconv.Atoi(endStr)
			if err != nil {
				return nil, fmt.Errorf("invalid hunk range %q: %w", part, err)
			}
			if start > end {
				return nil, fmt.Errorf("invalid hunk range %q: start > end", part)
			}
			for i := start; i <= end; i++ {
				indices = append(indices, i)
			}
		} else {
			// Single index
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid hunk index %q: %w", part, err)
			}
			indices = append(indices, idx)
		}
	}

	if len(indices) == 0 {
		return nil, errors.New("no hunk indices parsed")
	}
	return indices, nil
}
