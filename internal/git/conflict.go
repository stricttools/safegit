package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/gitversion"
)

// This file holds the plumbing a conflicted operation is concluded through:
// reading the index's unmerged stages, asking what the attributes say about a
// path, reproducing git's own conflict-marked file with merge-file, cleaning a
// message with stripspace, and reading a git configuration value. Everything
// here is a thin, single-purpose wrapper over one git invocation; the domain
// composition on top of them lives in internal/conflict.

// UnmergedEntry is one unmerged index entry: a path at one of the three merge
// stages. A conflicted path has up to three of them (1 = the merge base,
// 2 = ours, 3 = theirs), and a stage is ABSENT when the path did not exist on
// that side -- an add/add conflict has no stage 1, a delete/modify conflict has
// no stage 2 or no stage 3.
type UnmergedEntry struct {
	Mode  string
	SHA   string
	Stage int
	Path  string
}

// UnmergedStages lists the unmerged entries of an index, in git's own order.
//
// indexPath names the index to read; an empty indexPath reads the repository's
// shared index. The listing is NUL-delimited, so a path holding a newline, a
// quote or a non-UTF-8 byte arrives exactly as it is stored.
func UnmergedStages(ctx context.Context, indexPath string) ([]UnmergedEntry, error) {
	var env []string
	if indexPath != "" {
		env = []string{"GIT_INDEX_FILE=" + indexPath}
	}
	out, _, err := RunWithEnv(ctx, env, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}

	var entries []UnmergedEntry
	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		// "<mode> <sha> <stage>\t<path>"
		meta, path, found := strings.Cut(record, "\t")
		if !found {
			return nil, fmt.Errorf("ls-files -u produced a record with no path separator: %q", record)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, fmt.Errorf("ls-files -u produced a record with %d metadata fields, want 3: %q", len(fields), record)
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("ls-files -u produced an unreadable stage number in %q: %w", record, err)
		}
		entries = append(entries, UnmergedEntry{Mode: fields[0], SHA: fields[1], Stage: stage, Path: path})
	}
	return entries, nil
}

// AbbrevSHA returns the abbreviated object name git itself would print for a
// revision, honoring core.abbrev exactly as git's own conflict-marker labels do
// (git names the merge base on a diff3 marker line by this abbreviation, so a
// reconstruction that abbreviates differently is not byte-identical).
func AbbrevSHA(ctx context.Context, rev string) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--short", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// AttrUnspecified is what check-attr answers for a path no attributes file
// says anything about. CheckAttr passes it through rather than dropping the
// entry, because "the path was asked about and nothing was set" and "the path
// was never asked about" are different facts.
const AttrUnspecified = "unspecified"

// CheckAttr answers what the attributes files say about paths.
//
// attrSource, when non-empty, is a tree-ish whose .gitattributes files are read
// INSTEAD of the working tree's (git's --attr-source). That is the whole reason
// this wrapper exists: during a conflicted merge the working tree's
// .gitattributes may itself be conflicted -- marker-laden and meaningless -- so
// an attribute that decides how safegit treats the conflict has to be read from
// a committed tree, where it necessarily predates the conflict.
//
// The result is keyed by path, then by attribute name. A path git answers for
// is always present in the map; an attribute git says nothing about carries
// AttrUnspecified. A set-but-valueless attribute reads "set", an unset one
// "unset", exactly as git spells them.
//
// Paths travel on stdin, so a path that looks like an option or holds a special
// byte is never re-interpreted.
func CheckAttr(ctx context.Context, attrSource string, attrs []string, paths []string) (map[string]map[string]string, error) {
	if len(attrs) == 0 {
		return nil, errors.New("check-attr needs at least one attribute name")
	}
	if len(paths) == 0 {
		return map[string]map[string]string{}, nil
	}

	var args []string
	if attrSource != "" {
		if err := RequireFeature(ctx, gitversion.AttrSource); err != nil {
			return nil, err
		}
		// The joined spelling: a separate element would have to survive every
		// argv scan between here and the process, and the value is a tree name
		// that can look like anything.
		args = append(args, "--attr-source="+attrSource)
	}
	args = append(args, "check-attr", "-z", "--stdin")
	args = append(args, attrs...)

	var stdin bytes.Buffer
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}

	out, _, err := RunWithEnvStdin(ctx, nil, stdin.Bytes(), args...)
	if err != nil {
		return nil, err
	}

	// The output is a flat stream of NUL-terminated triplets:
	// <path> NUL <attribute> NUL <value> NUL
	fields := strings.Split(out, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%3 != 0 {
		return nil, fmt.Errorf("check-attr -z produced %d fields, which is not a whole number of path/attribute/value triplets", len(fields))
	}

	result := make(map[string]map[string]string, len(paths))
	for i := 0; i < len(fields); i += 3 {
		path, attr, value := fields[i], fields[i+1], fields[i+2]
		if result[path] == nil {
			result[path] = make(map[string]string, len(attrs))
		}
		result[path][attr] = value
	}
	return result, nil
}

// ConflictStyle is git's merge.conflictStyle vocabulary: which shape git writes
// a conflicted region in.
type ConflictStyle string

const (
	// StyleMerge is git's default: the two sides separated by "=======".
	StyleMerge ConflictStyle = "merge"
	// StyleDiff3 adds the merge base between them, under "|||||||".
	StyleDiff3 ConflictStyle = "diff3"
	// StyleZdiff3 is diff3 with lines common to both sides hoisted out of the
	// conflicted region.
	StyleZdiff3 ConflictStyle = "zdiff3"
)

// ParseConflictStyle reads a merge.conflictStyle configuration value. An empty
// value is git's own default. An unrecognized value is an error rather than a
// silent fall back to the default: safegit would otherwise reconstruct a
// conflict in a shape git never wrote, and compare it against the real file.
func ParseConflictStyle(value string) (ConflictStyle, error) {
	switch strings.TrimSpace(value) {
	case "":
		return StyleMerge, nil
	case string(StyleMerge):
		return StyleMerge, nil
	case string(StyleDiff3):
		return StyleDiff3, nil
	case string(StyleZdiff3):
		return StyleZdiff3, nil
	}
	return "", fmt.Errorf("merge.conflictStyle is %q, which is not one of %s, %s or %s", value, StyleMerge, StyleDiff3, StyleZdiff3)
}

// MergeFileOptions shapes one merge-file reconstruction.
type MergeFileOptions struct {
	// OursLabel, BaseLabel and TheirsLabel are the names git writes on the
	// conflict marker lines. They are part of the file's bytes, so a
	// reconstruction that has to match git's own output byte for byte has to
	// reproduce them (see internal/conflict for how git derives them).
	OursLabel   string
	BaseLabel   string
	TheirsLabel string

	// Style selects the conflicted-region shape. The zero value is StyleMerge.
	Style ConflictStyle

	// MarkerSize is the conflict marker length. Zero means git's default (7).
	MarkerSize int
}

// MergeFile runs git's three-way file merge over three blob contents and
// returns the merged result, plus whether the merge conflicted.
//
// This is how safegit reproduces the conflict-marked file git itself wrote into
// the working tree: given the index's stage 1/2/3 blobs and the attributes that
// were in force, merge-file emits the same bytes, because it is the same
// engine.
//
// The three sides are written into a throwaway directory and merge-file is run
// with -p, so the result comes back on stdout and nothing in the repository or
// the working tree is touched. A missing side (an add/add conflict has no base)
// is passed as an empty file, which is what git's own merge does.
//
// A conflicted merge is a NORMAL return, not an error: merge-file's exit status
// is the number of conflicts it left, and only a negative status (255 in
// practice) means it failed.
func MergeFile(ctx context.Context, ours, base, theirs []byte, opts MergeFileOptions) (merged []byte, conflicted bool, err error) {
	dir, err := os.MkdirTemp("", "safegit-merge-file-")
	if err != nil {
		return nil, false, fmt.Errorf("creating a scratch directory for merge-file: %w", err)
	}
	defer os.RemoveAll(dir)

	paths := make([]string, 3)
	for i, content := range [][]byte{ours, base, theirs} {
		p := filepath.Join(dir, []string{"ours", "base", "theirs"}[i])
		if err := os.WriteFile(p, content, 0600); err != nil {
			return nil, false, fmt.Errorf("writing the %s side for merge-file: %w", filepath.Base(p), err)
		}
		paths[i] = p
	}

	args := []string{"merge-file", "-p"}
	switch opts.Style {
	case StyleDiff3:
		args = append(args, "--diff3")
	case StyleZdiff3:
		args = append(args, "--zdiff3")
	}
	if opts.MarkerSize > 0 {
		args = append(args, "--marker-size="+strconv.Itoa(opts.MarkerSize))
	}
	args = append(args,
		"-L", opts.OursLabel,
		"-L", opts.BaseLabel,
		"-L", opts.TheirsLabel,
		paths[0], paths[1], paths[2],
	)

	stdout, _, runErr := RunWithEnv(ctx, nil, args...)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			// merge-file reports the conflict count as its exit status, capped
			// at 127; anything above that band is a real failure.
			if code := exitErr.ExitCode(); code > 0 && code <= 127 {
				return []byte(stdout), true, nil
			}
		}
		return nil, false, runErr
	}
	return []byte(stdout), false, nil
}

// StripComments removes comment lines from a commit message the way git does
// when it commits one: it runs `git stripspace --strip-comments`, so the
// repository's own core.commentChar (or core.commentString) decides what a
// comment is, and blank-line collapsing matches git's.
//
// The conclusion commands need it because the MERGE_MSG git leaves behind
// carries the "# Conflicts:" block, which is a comment in the draft and must
// not reach the commit object.
func StripComments(ctx context.Context, message string) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, []byte(message), "stripspace", "--strip-comments")
	if err != nil {
		return "", err
	}
	return out, nil
}

// ConfigGet reads one git configuration value. set reports whether the key is
// configured at all: git exits 1 with no output for an absent key, which is an
// answer rather than a failure, and a caller that needs a default applies its
// own.
func ConfigGet(ctx context.Context, key string) (value string, set bool, err error) {
	out, _, runErr := RunWithEnv(ctx, nil, "config", "--get", key)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, runErr
	}
	return strings.TrimRight(out, "\n"), true, nil
}
