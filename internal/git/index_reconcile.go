package git

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// indexSlot is one line of `git ls-files -s`: one slot of the shared index,
// identified by its path AND its stage. A resolved path occupies stage 0; an
// unmerged path occupies stages 1 (base), 2 (ours) and 3 (theirs), any subset
// of which may be present.
type indexSlot struct {
	Mode  string
	SHA   string
	Stage int
	Path  string
}

// treeEntry is one line of `git ls-tree -r`: what a commit's tree says about a
// path. Only the mode and object are compared against the index.
type treeEntry struct {
	Mode string
	SHA  string
}

// ReconcileMainIndex rebuilds the shared .git/index after a ref the working
// tree is on has moved, preserving everything the index holds that the
// pre-operation tip does not account for.
//
// This is the SINGLE index-reconciliation authority: commit, amend, reword and
// undo all reconcile through this one function, so "what happens to the shared
// index when safegit moves a ref" has exactly one answer, and the continue
// commands that conclude an interrupted operation reconcile the same way.
//
// beforeTip is the commit-ish the index was last reconciled against -- the
// ref's value BEFORE the operation. Empty means there was none (a root
// commit), so the whole index counts as delta. afterTreeish is the state to
// sync to; empty means clear the index entirely (undoing a root commit).
//
// What survives the sync:
//
//   - foreign staged work: a stage-0 entry that differs from beforeTip (a
//     staged modification or an addition beforeTip never had), and a path
//     beforeTip has that the index has no slot for at all (a staged deletion,
//     e.g. `git rm --cached`);
//   - unmerged stage 1/2/3 entries, replayed intact, so a conflict another
//     session is resolving is still a conflict afterwards;
//   - skip-worktree flags, re-set on every flagged path still present at
//     stage 0.
//
// The whole delta is replayed in ONE `git update-index --index-info` batch.
// Within that batch an unmerged path is preceded by a zero-mode removal line,
// because the read-tree wrote a stage-0 entry for it and git refuses to hold
// stage 0 and a higher stage for the same path at once.
//
// Every failure is HARD. A half-replayed index is a corrupted view of somebody
// else's staged work; reporting that as a warning and returning success is
// exactly how staged state disappears silently.
func ReconcileMainIndex(ctx context.Context, beforeTip, afterTreeish string) error {
	before, err := readIndexSlots(ctx)
	if err != nil {
		return fmt.Errorf("reading the shared index before reconciling it: %w", err)
	}
	tip, err := readTreeEntries(ctx, beforeTip)
	if err != nil {
		return fmt.Errorf("reading the pre-operation tip %q to compute the index delta: %w", beforeTip, err)
	}
	skipWorktree, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		return fmt.Errorf("reading skip-worktree flags before reconciling the index: %w", err)
	}

	replay := indexReplayBatch(before, tip)

	if afterTreeish == "" {
		if _, _, err := Run(ctx, "read-tree", "--empty"); err != nil {
			return fmt.Errorf("clearing the shared index: %w", err)
		}
	} else {
		if _, _, err := Run(ctx, "read-tree", afterTreeish); err != nil {
			return fmt.Errorf("syncing the shared index to %s: %w", afterTreeish, err)
		}
	}

	if len(replay) > 0 {
		stdin := []byte(strings.Join(replay, ""))
		if _, _, err := RunWithEnvStdin(ctx, nil, stdin, "update-index", "--index-info"); err != nil {
			return fmt.Errorf("replaying %d preserved index line(s) after syncing to %s "+
				"(the index no longer holds the staged state it held before): %w",
				len(replay), afterTreeishName(afterTreeish), err)
		}
	}

	return restoreSkipWorktree(ctx, skipWorktree)
}

// afterTreeishName spells the sync target for an error message, including the
// empty one.
func afterTreeishName(treeish string) string {
	if treeish == "" {
		return "the empty tree"
	}
	return treeish
}

// indexReplayBatch renders the index's delta against the tip as
// `git update-index --index-info` input: the lines that, applied to an index
// freshly read from any tree, put the delta back.
//
// The slots arrive in git's own index order (sorted by path, then stage), and
// the output preserves it, with the staged deletions -- which have no slot to
// order against -- appended in path order. The batch is therefore a pure
// function of the index and the tip.
func indexReplayBatch(slots []indexSlot, tip map[string]treeEntry) []string {
	byPath := make(map[string][]indexSlot, len(slots))
	var order []string
	for _, s := range slots {
		if _, seen := byPath[s.Path]; !seen {
			order = append(order, s.Path)
		}
		byPath[s.Path] = append(byPath[s.Path], s)
	}

	var lines []string
	for _, path := range order {
		group := byPath[path]
		if len(group) == 1 && group[0].Stage == 0 {
			// A resolved path the tip already explains is not delta: the
			// read-tree either restores it or deliberately changes it.
			if t, ok := tip[path]; ok && t.Mode == group[0].Mode && t.SHA == group[0].SHA {
				continue
			}
			lines = append(lines, indexInfoLine(group[0]))
			continue
		}
		// Unmerged. The read-tree writes a stage-0 entry for this path, and git
		// will not hold stage 0 alongside a higher stage, so the stage-0 entry
		// is cleared with a zero-mode removal line before the stages are
		// written. Stages ascend, matching git's own index order.
		sort.SliceStable(group, func(i, j int) bool { return group[i].Stage < group[j].Stage })
		lines = append(lines, indexRemovalLine(path))
		for _, s := range group {
			lines = append(lines, indexInfoLine(s))
		}
	}

	// A path the tip has and the index has no slot for at all is a staged
	// deletion. Nothing in the index records it, so it has to be derived from
	// the tip's side and replayed as a removal.
	var deleted []string
	for path := range tip {
		if _, ok := byPath[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	sort.Strings(deleted)
	for _, path := range deleted {
		lines = append(lines, indexRemovalLine(path))
	}

	return lines
}

// indexInfoLine renders one slot in `--index-info`'s stage-carrying form:
// "mode SP object SP stage TAB path".
func indexInfoLine(s indexSlot) string {
	return fmt.Sprintf("%s %s %d\t%s\n", s.Mode, s.SHA, s.Stage, quotePathForIndexInfo(s.Path))
}

// indexRemovalLine renders the zero-mode form that removes every slot a path
// has: "0 SP <all-zero object> TAB path". Removing a path the index does not
// hold is a no-op, not an error.
func indexRemovalLine(path string) string {
	return fmt.Sprintf("0 %s\t%s\n", ZeroSHA, quotePathForIndexInfo(path))
}

// quotePathForIndexInfo renders a path in git's C-quoted form, always.
//
// `git update-index --index-info` reads LF-terminated lines and unquotes a path
// only when it begins with a double quote; -z applies to --stdin and not to
// --index-info, so there is no NUL-delimited form of this input to reach for.
// Quoting unconditionally is what makes a path containing a space, a quote, a
// newline or a non-UTF-8 byte survive the round trip -- and it removes the
// ambiguity a path that itself starts with a quote would otherwise create.
func quotePathForIndexInfo(path string) string {
	var b strings.Builder
	b.Grow(len(path) + 2)
	b.WriteByte('"')
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c >= 0x7f:
			// Three octal digits, always: git's unquoting reads at most three,
			// so a literal digit that follows can never be absorbed.
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// readIndexSlots reads every slot of the shared index, all stages included.
//
// The NUL-delimited form is mandatory, not a nicety: without -z git C-quotes
// any path that is not plain ASCII, and a reader that missed that would hand
// back a path no filesystem call and no later index write could resolve.
func readIndexSlots(ctx context.Context) ([]indexSlot, error) {
	out, _, err := Run(ctx, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	var slots []indexSlot
	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("unparseable `git ls-files -s` record (no tab): %q", record)
		}
		fields := strings.Fields(record[:tab])
		if len(fields) != 3 {
			return nil, fmt.Errorf("unparseable `git ls-files -s` record (want mode, object, stage): %q", record)
		}
		stage, serr := strconv.Atoi(fields[2])
		if serr != nil {
			return nil, fmt.Errorf("unparseable stage in `git ls-files -s` record %q: %w", record, serr)
		}
		slots = append(slots, indexSlot{
			Mode:  fields[0],
			SHA:   fields[1],
			Stage: stage,
			Path:  record[tab+1:],
		})
	}
	return slots, nil
}

// readTreeEntries reads every path a commit-ish's tree holds, recursively.
// An empty treeish means "no tip at all" and yields an empty set, which makes
// every index slot delta -- the root-commit case.
//
// --full-tree is mandatory for the same reason it is on LsTreeAll: without it
// git resolves the listing against the process working directory PREFIX, so
// from a subdirectory the reconciler would compare the index against that
// subdirectory's entries with the prefix stripped and read every path outside
// it as a delta.
func readTreeEntries(ctx context.Context, treeish string) (map[string]treeEntry, error) {
	entries := make(map[string]treeEntry)
	if treeish == "" {
		return entries, nil
	}
	out, _, err := Run(ctx, "ls-tree", "--full-tree", "-r", "-z", treeish)
	if err != nil {
		return nil, err
	}
	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("unparseable `git ls-tree -r` record (no tab): %q", record)
		}
		fields := strings.Fields(record[:tab])
		if len(fields) != 3 {
			return nil, fmt.Errorf("unparseable `git ls-tree -r` record (want mode, type, object): %q", record)
		}
		entries[record[tab+1:]] = treeEntry{Mode: fields[0], SHA: fields[2]}
	}
	return entries, nil
}

// restoreSkipWorktree re-sets the skip-worktree flag on the paths that carried
// it before the sync, in one batch.
//
// Only paths the reconciled index holds at stage 0 are marked: the flag is an
// attribute of an index entry, so a path the operation legitimately removed
// from the index (or left unmerged) has nothing to carry it, and git refuses to
// mark it. Failing to mark a path that IS there is a hard error -- an
// unrestored skip-worktree flag means git starts reporting a file the operator
// deliberately hid as modified, and the operator gets no say in it.
func restoreSkipWorktree(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	slots, err := readIndexSlots(ctx)
	if err != nil {
		return fmt.Errorf("re-reading the shared index to restore skip-worktree flags: %w", err)
	}
	resolved := make(map[string]struct{}, len(slots))
	for _, s := range slots {
		if s.Stage == 0 {
			resolved[s.Path] = struct{}{}
		}
	}
	var mark []string
	for _, p := range paths {
		if _, ok := resolved[p]; ok {
			mark = append(mark, p)
		}
	}
	if len(mark) == 0 {
		return nil
	}
	stdin := []byte(strings.Join(mark, "\x00") + "\x00")
	if _, _, err := RunWithEnvStdin(ctx, nil, stdin, "update-index", "--skip-worktree", "-z", "--stdin"); err != nil {
		return fmt.Errorf("restoring skip-worktree on %s: %w", describePaths(mark), err)
	}
	return nil
}

// describePaths names a path set for an error message, naming them all while
// the list is short enough to read.
func describePaths(paths []string) string {
	const shown = 5
	if len(paths) <= shown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:shown], ", "), len(paths)-shown)
}
