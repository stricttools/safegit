package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// `safegit mv` -- the move that performs itself.
//
// Everything else in the move vocabulary is a DECLARATION about a move somebody
// already made: `--moved` states that content moved and safegit checks the
// statement against the repository. This command is the other half. It renames
// the paths, mints the records for what it renamed, and commits the result, in
// one invocation, so the move and its record can never be out of step -- which
// is the failure the declaration form exists to correct after the fact.
//
// Three properties hold it together:
//
//   - EVERY pair is checked before the FIRST filesystem mutation. A set of
//     moves is one statement, and a set with one bad pair in it must not leave
//     the working tree half-moved while it refuses.
//   - The filesystem steps are minted through the effects handle, so a dry run
//     records the renames instead of performing them and the preview is honest
//     rather than a description of a run that would take a different path.
//   - A failure the checks could not foresee -- the filesystem refusing a
//     rename the repository had no objection to -- puts back everything this
//     invocation had already moved.
//
// The commit is the RENAME AND NOTHING ELSE. Each moved path is carried across
// as the exact blob the parent tree held, through index edits rather than
// through staging from disk, which is what `git mv` followed by `git commit`
// produces and what makes a preview and an execution compute the same tree.
// Uncommitted content changes at a moved path stay uncommitted, and are a
// separate commit -- a move is a move.

// mvEntry is one path the move carries across: where it was, where it lands,
// and the tree entry that travels with it unchanged.
type mvEntry struct {
	old  string
	new  string
	mode string
	sha  string
}

// mvPair is one validated `old -> new` argument.
type mvPair struct {
	// arg is the argument verbatim, so a refusal names what the caller typed.
	arg string
	// old and new are canonical repo-relative paths. A trailing slash on both
	// marks the subtree form, exactly as in the record that gets written.
	old string
	new string
	// caseOnly marks a pair whose two paths differ only in case. On a
	// case-insensitive filesystem that rename cannot be performed directly.
	caseOnly bool
	// entries are the tree entries this pair moves: one for a file, and every
	// path under the prefix for a subtree.
	entries []mvEntry
}

func (p mvPair) subtree() bool     { return strings.HasSuffix(p.old, "/") }
func (p mvPair) oldPrefix() string { return strings.TrimSuffix(p.old, "/") }
func (p mvPair) newPrefix() string { return strings.TrimSuffix(p.new, "/") }

// mvMove is one performed move as the payload reports it.
type mvMove struct {
	ID  string `json:"id"`
	Old string `json:"old"`
	New string `json:"new"`
}

// mvPayload is what `mv` puts in the envelope's payload.
//
// moves are the records the commit carries, in the order the pairs were given;
// files are the repo-relative paths the commit actually changed, read off the
// objects by the pipeline rather than counted from the arguments.
type mvPayload struct {
	Ref      string   `json:"ref"`
	Parents  []string `json:"parents"`
	Tree     string   `json:"tree"`
	SHA      *string  `json:"sha"`
	Moves    []mvMove `json:"moves"`
	Files    []string `json:"files"`
	Attempts int      `json:"attempts"`
	DryRun   bool     `json:"dry_run"`
}

var mvPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"ref":     strictcli.SchemaType("string"),
		"parents": strictcli.SchemaArray(strictcli.SchemaType("string")),
		"tree":    strictcli.SchemaType("string"),
		"sha":     strictcli.SchemaType("string", "null"),
		"moves": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"id":  strictcli.SchemaType("string"),
				"old": strictcli.SchemaType("string"),
				"new": strictcli.SchemaType("string"),
			},
			[]string{"id", "old", "new"},
			false,
		)),
		"files":    strictcli.SchemaArray(strictcli.SchemaType("string")),
		"attempts": strictcli.SchemaType("integer"),
		"dry_run":  strictcli.SchemaType("boolean"),
	},
	[]string{"ref", "parents", "tree", "sha", "moves", "files", "attempts", "dry_run"},
	false,
)

// mvOplogOp is the operation name `mv` records, and the key undo reads to
// reverse it -- see undoableOps.
const mvOplogOp = "mv"

func runMv(flags globalFlags, messages []string, args []string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	message := joinMessages(messages)

	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	// Outermost, around the whole operation: the renames and the commit are one
	// step, and no passthrough in this worktree may run between them.
	release, code := acquireOperationLock(flags, gitDir, mvOplogOp)
	if code != 0 {
		return code
	}
	defer release()

	ctx := flags.ctx()
	repoRoot, err := git.AnchorRoot(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving the repository root: %v\n", err)
		return exitcode.General
	}

	pairs, code := parseMvPairs(repoRoot, args)
	if code != 0 {
		return code
	}
	if code := checkMvWorld(ctx, repoRoot, pairs); code != 0 {
		return code
	}

	if err := performMvMoves(flags, repoRoot, pairs); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	return commitMvMoves(flags, gitDir, message, pairs)
}

// parseMvPairs reads every argument through the ONE pair parser, canonicalizes
// both sides against the caller's own directory, and refuses a set of pairs
// that speak about each other's paths.
//
// Everything it refuses is a contradiction between ARGUMENTS -- a pair that
// does not parse, two pairs that both claim one path -- which is Usage, the
// same code every other argument-against-argument refusal in the commit family
// exits. Nothing here has looked at the repository yet.
func parseMvPairs(repoRoot string, args []string) ([]mvPair, int) {
	pairs := make([]mvPair, 0, len(args))
	for _, arg := range args {
		old, new, err := trailer.ParsePair(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", arg, err)
			return nil, exitcode.Usage
		}
		subtree := strings.HasSuffix(old, "/")
		oldRel, err := commit.CanonicalRel(repoRoot, old, subtree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", arg, err)
			return nil, exitcode.Usage
		}
		newRel, err := commit.CanonicalRel(repoRoot, new, subtree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", arg, err)
			return nil, exitcode.Usage
		}
		if oldRel == "" || newRel == "" {
			fmt.Fprintf(os.Stderr, "error: %s names the repository root, which cannot move\n", arg)
			return nil, exitcode.Usage
		}
		if subtree {
			oldRel += "/"
			newRel += "/"
		}
		if err := trailer.ValidatePair(oldRel, newRel); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", arg, err)
			return nil, exitcode.Usage
		}
		pairs = append(pairs, mvPair{
			arg:      arg,
			old:      oldRel,
			new:      newRel,
			caseOnly: oldRel != newRel && strings.EqualFold(oldRel, newRel),
		})
	}
	if code := refuseMvOverlaps(pairs); code != 0 {
		return nil, code
	}
	return pairs, 0
}

// refuseMvOverlaps rejects a set of pairs that cannot be performed as one
// statement.
//
// Two kinds, both argument-against-argument:
//
//   - NESTING, on either side: `src/ -> lib/` alongside `src/one.txt -> x` says
//     two different things about one file, and a writer picking between them
//     would be the silent precedence rule this tool does not have. It is the
//     same refusal `--moved` makes, through the same nesting rule.
//   - CHAINING: one path that is both a destination and a source, as in
//     `a -> b` beside `b -> c`. The result would depend on the order the
//     renames happened to be performed in, which is not something a caller
//     stated.
func refuseMvOverlaps(pairs []mvPair) int {
	for i := range pairs {
		for j := i + 1; j < len(pairs); j++ {
			a, b := pairs[i], pairs[j]
			if trailer.Nests(a.oldPrefix(), b.oldPrefix()) {
				fmt.Fprintf(os.Stderr, "error: %s and %s both move %s; say one thing about each path\n",
					a.arg, b.arg, a.oldPrefix())
				return exitcode.Usage
			}
			if trailer.Nests(a.newPrefix(), b.newPrefix()) {
				fmt.Fprintf(os.Stderr, "error: %s and %s both land on %s; say one thing about each path\n",
					a.arg, b.arg, a.newPrefix())
				return exitcode.Usage
			}
			if trailer.Nests(a.newPrefix(), b.oldPrefix()) || trailer.Nests(b.newPrefix(), a.oldPrefix()) {
				fmt.Fprintf(os.Stderr, "error: %s and %s chain: one moves a path the other moves away from,\n", a.arg, b.arg)
				fmt.Fprintf(os.Stderr, "       so the result would depend on which was performed first. Make them two commands.\n")
				return exitcode.Usage
			}
		}
	}
	return 0
}

// checkMvWorld asks the repository about every pair and fills in the tree
// entries each one carries across.
//
// It collects EVERY failure before refusing. A caller who typed three pairs and
// got one path wrong should be told about all three verdicts in one go, not
// made to discover them one command at a time -- and a set of moves that is
// refused must leave the working tree exactly as it was, so there is no reason
// to stop at the first.
func checkMvWorld(ctx context.Context, repoRoot string, pairs []mvPair) int {
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil || head == "" {
		fmt.Fprintf(os.Stderr, "error: this branch has no commit yet, so nothing is tracked for a move to come out of\n")
		return exitcode.MoveNotBorneOut
	}
	entries, err := git.LsTreeRecursive(ctx, head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the tree of HEAD: %v\n", err)
		return exitcode.General
	}
	tracked := make(map[string]git.TreeEntry, len(entries))
	var sorted []string
	for _, e := range entries {
		tracked[e.Path] = e
		sorted = append(sorted, e.Path)
	}
	sort.Strings(sorted)

	var refusals []string
	for i := range pairs {
		if why := checkMvPair(repoRoot, tracked, sorted, &pairs[i]); why != "" {
			refusals = append(refusals, fmt.Sprintf("%s: %s", pairs[i].arg, why))
		}
	}
	if len(refusals) > 0 {
		fmt.Fprintf(os.Stderr, "error: %d of %d move(s) are not borne out by the repository:\n", len(refusals), len(pairs))
		for _, r := range refusals {
			fmt.Fprintf(os.Stderr, "  %s\n", r)
		}
		fmt.Fprintf(os.Stderr, "  nothing was moved and nothing was committed.\n")
		return exitcode.MoveNotBorneOut
	}
	return 0
}

// checkMvPair is the whole check one pair gets, and it fills in the entries the
// move carries. An empty answer means the pair holds.
func checkMvPair(repoRoot string, tracked map[string]git.TreeEntry, sorted []string, p *mvPair) string {
	absOld := git.Anchor(repoRoot, p.oldPrefix())
	absNew := git.Anchor(repoRoot, p.newPrefix())

	info, err := os.Lstat(absOld)
	if err != nil {
		return fmt.Sprintf("%s is not on disk; a move takes content that is there to somewhere else", p.oldPrefix())
	}

	if p.subtree() {
		if !info.IsDir() {
			return fmt.Sprintf("%s is not a directory, so it cannot be moved as a subtree; write it as %s",
				p.oldPrefix(), trailer.EncodePair(p.oldPrefix(), p.newPrefix()))
		}
		under := pathsUnder(sorted, p.oldPrefix())
		if len(under) == 0 {
			return fmt.Sprintf("%s holds no paths tracked in HEAD", p.oldPrefix())
		}
		for _, path := range under {
			e := tracked[path]
			p.entries = append(p.entries, mvEntry{
				old:  path,
				new:  p.newPrefix() + path[len(p.oldPrefix()):],
				mode: e.Mode,
				sha:  e.SHA,
			})
		}
	} else {
		if info.IsDir() {
			return fmt.Sprintf("%s is a directory; a directory moves as a subtree, written %s",
				p.oldPrefix(), trailer.EncodePair(p.old+"/", p.new+"/"))
		}
		e, ok := tracked[p.old]
		if !ok {
			return fmt.Sprintf("%s is not tracked in HEAD", p.old)
		}
		p.entries = append(p.entries, mvEntry{old: p.old, new: p.new, mode: e.Mode, sha: e.SHA})
	}

	// The destination has to be free. A case-only rename is the one pair whose
	// destination LOOKS occupied on a case-insensitive filesystem -- by the
	// source itself -- so the disk question is not asked of it; the tree
	// question still is, because a tree distinguishes the two spellings
	// wherever the filesystem does not.
	if !p.caseOnly {
		if _, err := os.Lstat(absNew); err == nil {
			return fmt.Sprintf("%s is already on disk; a move never overwrites what is there", p.newPrefix())
		}
	}
	if p.subtree() {
		if under := pathsUnder(sorted, p.newPrefix()); len(under) > 0 {
			return fmt.Sprintf("%s already holds paths tracked in HEAD", p.newPrefix())
		}
	} else if _, ok := tracked[p.new]; ok {
		return fmt.Sprintf("%s is already tracked in HEAD", p.new)
	}
	return ""
}

// pathsUnder returns every sorted tree path inside a directory prefix.
func pathsUnder(sorted []string, prefix string) []string {
	p := prefix + "/"
	i := sort.SearchStrings(sorted, p)
	var out []string
	for ; i < len(sorted) && strings.HasPrefix(sorted[i], p); i++ {
		out = append(out, sorted[i])
	}
	return out
}

// mvFilesystem performs the filesystem half of a move and remembers how to take
// each step back.
//
// Every step is minted through the effects handle, which is what makes a dry
// run record the renames instead of performing them. The undo stack is only
// ever unwound on an executing run: a preview performs nothing, so nothing it
// recorded can fail.
type mvFilesystem struct {
	flags globalFlags
	// undo is the inverse of every step taken so far, newest last.
	undo []func() error
	// ensured is the set of directories this invocation has already created (or
	// recorded creating), so a second pair landing in the same place does not
	// record a second creation of it.
	ensured map[string]bool
	// counter distinguishes the temporary names a case-only rename passes
	// through when several are performed in one invocation.
	counter int
}

// performMvMoves renames every pair, rolling back what it already did when one
// of them fails.
func performMvMoves(flags globalFlags, repoRoot string, pairs []mvPair) error {
	fs := &mvFilesystem{flags: flags, ensured: map[string]bool{}}
	for _, p := range pairs {
		if err := fs.move(repoRoot, p); err != nil {
			fs.rollback()
			return fmt.Errorf("moving %s: %w\n  every move this command had already made was put back; "+
				"nothing was committed", p.arg, err)
		}
	}
	return nil
}

// move performs one pair: the destination's parent directory when it is
// missing, then the rename itself.
func (fs *mvFilesystem) move(repoRoot string, p mvPair) error {
	absOld := git.Anchor(repoRoot, p.oldPrefix())
	absNew := git.Anchor(repoRoot, p.newPrefix())

	if err := fs.ensureParent(absNew); err != nil {
		return err
	}
	if p.caseOnly && fs.ignoreCase() {
		return fs.renameThroughTemp(absOld, absNew)
	}
	return fs.rename(absOld, absNew)
}

// ensureParent creates the destination's parent directory when it is missing,
// and records the TOPMOST directory it created so a rollback removes exactly
// what this invocation added and nothing that was already there.
func (fs *mvFilesystem) ensureParent(absPath string) error {
	dir := filepath.Dir(absPath)
	if fs.ensured[dir] {
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	top := dir
	for {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		if _, err := os.Stat(parent); err == nil {
			break
		}
		top = parent
	}
	if _, err := fs.flags.effects().Mkdir(dir, strictcli.Resource("path:"+dir)); err != nil {
		return err
	}
	fs.ensured[dir] = true
	fs.undo = append(fs.undo, func() error {
		_, err := fs.flags.effects().Remove(top, strictcli.Resource("path:"+top))
		return err
	})
	return nil
}

// rename is one minted rename plus its inverse.
func (fs *mvFilesystem) rename(from, to string) error {
	if _, err := fs.flags.effects().Rename(from, to, strictcli.Resource("path:"+to)); err != nil {
		return err
	}
	fs.undo = append(fs.undo, func() error {
		_, err := fs.flags.effects().Rename(to, from, strictcli.Resource("path:"+from))
		return err
	})
	return nil
}

// renameThroughTemp performs a case-only rename on a filesystem that folds
// case, where renaming a path onto a spelling of its own name is either a no-op
// or an error. The path goes out to a name that differs by more than case and
// comes back at the spelling that was asked for; the temporary sits in the
// destination's own directory so the two steps are both plain renames within
// one filesystem.
func (fs *mvFilesystem) renameThroughTemp(from, to string) error {
	fs.counter++
	tmp := filepath.Join(filepath.Dir(to), fmt.Sprintf(".safegit-mv-%d-%d", os.Getpid(), fs.counter))
	if err := fs.rename(from, tmp); err != nil {
		return err
	}
	return fs.rename(tmp, to)
}

// ignoreCase reports git's own answer for this repository. It is the condition
// the two-step rename exists for, and it is read from the repository rather
// than probed from the filesystem so that safegit and git agree about which
// world they are in.
func (fs *mvFilesystem) ignoreCase() bool {
	out, _, err := git.Run(fs.flags.ctx(), "config", "--get", "core.ignorecase")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// rollback unwinds every step taken so far, newest first. A step that cannot be
// taken back is reported and the unwinding continues: the remaining steps are
// independent of it, and stopping would leave more of the tree moved than
// necessary.
func (fs *mvFilesystem) rollback() {
	for i := len(fs.undo) - 1; i >= 0; i-- {
		if err := fs.undo[i](); err != nil {
			fmt.Fprintf(os.Stderr, "warning: putting back part of the move failed: %v\n", err)
		}
	}
}

// commitMvMoves writes the commit: the renamed paths as index edits carrying
// the parent tree's own blobs, and one record per pair.
func commitMvMoves(flags globalFlags, gitDir, message string, pairs []mvPair) int {
	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return exitcode.General
	}

	var edits []commit.IndexEdit
	var records []string
	var moves []mvMove
	for _, p := range pairs {
		record, err := trailer.NewRecord(p.old, p.new)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", p.arg, err)
			return exitcode.Usage
		}
		records = append(records, trailer.RecordLine(record))
		moves = append(moves, mvMove{ID: record.ID, Old: p.old, New: p.new})
		for _, e := range p.entries {
			edits = append(edits,
				commit.IndexEdit{Kind: commit.IndexEditRemove, Path: e.old},
				commit.IndexEdit{Kind: commit.IndexEditBlob, Path: e.new, Mode: e.mode, SHA: e.sha},
			)
		}
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(flags.ctx(), commit.CommitRequest{
		Message:      message,
		DryRun:       flags.dryRun,
		IndexEdits:   edits,
		MovedRecords: records,
		OplogOp:      mvOplogOp,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: the paths were moved, but the commit failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  the files are at their new paths. Commit them with 'safegit commit --moved' once the cause is fixed.\n")
		return pipelineExitCode(err)
	}

	if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, mvOplogOp, firstLine(message)); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	flags.payload(mvPayload{
		Ref:      result.Ref,
		Parents:  orEmpty(result.Parents),
		Tree:     result.Tree,
		SHA:      realSHA(flags, result.SHA),
		Moves:    moves,
		Files:    orEmpty(result.Files),
		Attempts: result.Attempts,
		DryRun:   flags.dryRun,
	})

	if !flags.silent() {
		if flags.dryRun {
			fmt.Println(wouldWriteHeader(mvOplogOp, result.Ref, result.Tree, firstLine(message)))
			fmt.Printf(" %d move(s) would be recorded, %d path(s) would change\n", len(moves), len(result.Files))
		} else {
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(message))
			fmt.Printf(" %d move(s) recorded, %d path(s) changed\n", len(moves), len(result.Files))
		}
	}
	return exitcode.OK
}
