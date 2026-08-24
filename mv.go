package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/coord"
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
//
// Which is why a path carrying UNCOMMITTED CONTENT CHANGES is refused rather
// than moved: carrying the parent's blob across would leave the edit behind,
// uncommitted, at a path the operator never named. git mv moves it anyway; this
// is one of the deliberate divergences, and the refusal names the two routes
// that exist instead (see dirtyMoveReason).

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

// pair is this move as the shared overlap check takes it.
func (p mvPair) pair() trailer.Pair { return trailer.Pair{Old: p.old, New: p.new} }

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
	// Residue is every step this move owed AFTER its ref update and did not
	// finish, never nil -- the same aftercare every commit-authoring route owes,
	// reached through the same pipeline. An empty list is a move that finished
	// everything; anything in it is what the commit-stands exit code (see
	// aftercare.go) is about.
	Residue []residueEntry `json:"residue"`
	DryRun  bool           `json:"dry_run"`
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
		"residue": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"step":   strictcli.SchemaType("string"),
				"detail": strictcli.SchemaType("string"),
			},
			[]string{"step", "detail"},
			false,
		)),
		"dry_run": strictcli.SchemaType("boolean"),
	},
	[]string{"ref", "parents", "tree", "sha", "moves", "files", "attempts", "residue", "dry_run"},
	false,
)

// mvOplogOp is the operation name `mv` records, and the key undo reads to
// reverse it -- see undoableOps.
const mvOplogOp = "mv"

// runMv performs the whole command. createMissingDirs is the caller's election
// to have the destination's parent directories minted when they are not there;
// without it a destination whose directory does not exist is a refusal. The
// election is mv-local -- it reaches the validation and the filesystem half and
// nothing else, because there is no commit-pipeline behaviour it changes.
func runMv(flags globalFlags, messages []string, args []string, createMissingDirs bool) int {
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

	// Taken here rather than left to the commit pipeline's own declaration
	// check, which every other commit path relies on. That check runs when the
	// commit is built -- which for this command is AFTER the renames, so a `mv`
	// run while git has a merge, cherry-pick, revert, rebase or mailbox
	// application in flight would move every file and then refuse to commit
	// them. Read inside the operation lock, so no passthrough in this worktree
	// can start one between the check and the renames.
	if err := coord.GuardInFlight(gitDir, mvOplogOp, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.CoordinationBusy
	}

	ctx := flags.ctx()
	repoRoot, err := git.AnchorRoot(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving the repository root: %v\n", err)
		return exitcode.General
	}

	// Read ONCE, before validation, and used by both halves of the command: the
	// check decides whether a case-only pair has a destination at all, and the
	// rename decides whether it has to go out through a temporary name. Both
	// questions are the same question, so they must not be able to get different
	// answers within one invocation.
	ignoreCase := gitIgnoreCase(ctx)

	pairs, code := parseMvPairs(repoRoot, args)
	if code != 0 {
		return code
	}
	if code := checkMvWorld(ctx, repoRoot, ignoreCase, createMissingDirs, pairs); code != 0 {
		return code
	}

	if err := performMvMoves(flags, ignoreCase, createMissingDirs, repoRoot, pairs); err != nil {
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
// The question is trailer.Overlap's -- the ONE implementation, shared with the
// `--moved` refusal, which judges the same declarations by another spelling.
// All this does is say the verdict in the terms an `mv` caller typed.
func refuseMvOverlaps(pairs []mvPair) int {
	for i := range pairs {
		for j := i + 1; j < len(pairs); j++ {
			a, b := pairs[i], pairs[j]
			switch kind, x, _ := trailer.Overlap(a.pair(), b.pair()); kind {
			case trailer.SameSource:
				fmt.Fprintf(os.Stderr, "error: %s and %s both move %s; say one thing about each path\n",
					a.arg, b.arg, x)
				return exitcode.Usage
			case trailer.SameDestination:
				fmt.Fprintf(os.Stderr, "error: %s and %s both land on %s; say one thing about each path\n",
					a.arg, b.arg, x)
				return exitcode.Usage
			case trailer.Chained:
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
func checkMvWorld(ctx context.Context, repoRoot string, ignoreCase, createMissingDirs bool, pairs []mvPair) int {
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
		if why := checkMvPair(ctx, repoRoot, ignoreCase, createMissingDirs, tracked, sorted, &pairs[i]); why != "" {
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
func checkMvPair(ctx context.Context, repoRoot string, ignoreCase, createMissingDirs bool, tracked map[string]git.TreeEntry, sorted []string, p *mvPair) string {
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
	// destination LOOKS occupied -- by the source itself -- and that is true
	// only where the filesystem folds case, i.e. where the two spellings really
	// are one file. So the exemption is conditional on git's own
	// core.ignorecase: where it is off, the two spellings are two files and an
	// occupied destination is exactly what it appears to be. The tree question
	// is asked either way, because a tree distinguishes the two spellings
	// wherever the filesystem does not -- but it only ever sees paths tracked in
	// HEAD, so it is no substitute for the disk question: an UNTRACKED file at
	// the destination is content that exists nowhere else, and a rename over it
	// destroys it.
	if !p.caseOnly || !ignoreCase {
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

	// The destination's parent has to BE there, and has to be a DIRECTORY.
	// safegit invents no place for content to land in: a destination naming a
	// directory that does not exist is far more often a typo than an intention,
	// and the move that "worked" left the operator with a directory they never
	// asked for and no way to tell it apart from one they already had.
	// --create-missing-directories is how the other intention is said out loud.
	//
	// docs/divergences.md carried an entry for the old creates-it-anyway
	// behavior; the refusal converges safegit with git (git mv refuses too), so
	// that entry is deleted rather than rewritten.
	if why := destinationParentReason(repoRoot, absNew, p.newPrefix(), createMissingDirs); why != "" {
		return why
	}

	// Last, because it is the one question about the SOURCE's content rather
	// than about where the move lands: a file carrying uncommitted edits cannot
	// be moved, because this command commits the rename and nothing else.
	return dirtyMoveReason(ctx, repoRoot, p)
}

// dirtyMoveReason is the refusal for a pair whose content has been edited and
// not committed. An empty answer means every path it carries is clean.
//
// `mv` commits the RENAME AND NOTHING ELSE: each path is carried across as the
// exact blob its parent held. For an edited file that is the wrong commit --
// the move succeeds, the edit is silently left behind as an uncommitted change
// at a path the operator never named, and the commit records content that is
// not what is on disk. There is no flag for it, because both things the
// operator might have meant already have a route and the refusal names them.
//
// EVERY dirty path is named, not the first: a subtree move is one pair over
// many files, and a refusal naming one of them is a discovery loop.
func dirtyMoveReason(ctx context.Context, repoRoot string, p *mvPair) string {
	var dirty []string
	for _, e := range p.entries {
		changed, err := mvEntryIsDirty(ctx, repoRoot, e)
		if err != nil {
			return fmt.Sprintf("%s cannot be read, so whether it carries uncommitted changes is unanswerable: %v",
				e.old, err)
		}
		if changed {
			dirty = append(dirty, e.old)
		}
	}
	if len(dirty) == 0 {
		return ""
	}

	what := dirty[0] + " carries uncommitted content changes."
	if len(dirty) > 1 {
		what = "these paths carry uncommitted content changes:\n         " +
			strings.Join(dirty, "\n         ")
	}
	// The two routes, in the order the intents divide: the edit is its own
	// change, or the edit belongs with the move.
	return fmt.Sprintf("%s\n"+
		"       A move is a move: this command commits the rename and nothing else, so the edit\n"+
		"       would be left behind uncommitted at a path you did not name. Either commit the\n"+
		"       content first and then move it, or move it on disk yourself and commit both at\n"+
		"       once with: safegit commit --moved '%s' -- %s",
		what, trailer.EncodePair(p.old, p.new), p.newPrefix())
}

// mvEntryIsDirty reports whether the working tree's copy of one moved path
// differs in CONTENT from the blob the parent commit holds.
//
// The comparison is filter-aware, and the attributes are resolved under the NEW
// path: that is where the content is about to live, so that is the name git
// itself would decide the conversion by. Without it, a checkout on which git
// converts line endings would have every file look changed and no move would
// ever be allowed.
//
// Three things are deliberately not dirtiness here. A path tracked in the
// parent but absent from disk is a DELETION, which the move leaves uncommitted
// exactly as it found it (and for the file form, an absent source was already
// refused above). A gitlink is a submodule's own recorded commit, not content
// this tree holds. And a mode change is not content -- `mv` carries the
// parent's mode across, and the changed bit stays uncommitted where it was.
func mvEntryIsDirty(ctx context.Context, repoRoot string, e mvEntry) (bool, error) {
	if e.mode == "160000" {
		return false, nil
	}
	abs := git.Anchor(repoRoot, e.old)
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// A symlink's blob IS its target text, and git applies no filter to it, so
	// it is hashed raw rather than as the path it will live at.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return false, err
		}
		sha, err := git.HashObjectBytes(ctx, []byte(target))
		if err != nil {
			return false, err
		}
		return sha != e.sha, nil
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	sha, err := git.HashObjectBytesAsPath(ctx, e.new, data)
	if err != nil {
		return false, err
	}
	return sha != e.sha, nil
}

// destinationParentReason is the refusal a move's destination earns from the
// place it would land in, or the empty string when that place is a directory
// that is there (the repository root always is).
//
// There are two distinct wrong worlds, and only one of them is an election's to
// cure. A parent that is ABSENT is what --create-missing-directories elects to
// mint, so with the election it is no reason at all. A path on the way that is
// on disk and is NOT a directory is a reason either way: no mkdir can turn an
// existing file into a directory, so electing creation over it would only move
// the failure from validation to the rename, where it arrives as a general
// error with a rollback instead of as this collected refusal.
func destinationParentReason(repoRoot, absNew, newPrefix string, createMissingDirs bool) string {
	parent := filepath.Dir(absNew)
	rel, err := filepath.Rel(repoRoot, parent)
	if err != nil || rel == "." || rel == "" {
		return ""
	}
	if blocker := nonDirectoryAncestor(repoRoot, parent); blocker != "" {
		return fmt.Sprintf("%s is not a directory, so %s has nowhere to land; "+
			"a move never turns a file into a directory", blocker, newPrefix)
	}
	if createMissingDirs {
		return ""
	}
	if _, err := os.Stat(parent); err == nil {
		return ""
	}
	return fmt.Sprintf("%s does not exist, so %s has nowhere to land; make the directory first, "+
		"or pass --create-missing-directories to have this command make it",
		filepath.ToSlash(rel), newPrefix)
}

// nonDirectoryAncestor returns the repo-relative path of the nearest ancestor of
// dir -- dir itself included -- that is on disk and is not a directory, or the
// empty string when the chain up to the repository root holds no such thing.
//
// The walk is what the DEEP shape needs: `a.txt -> b.txt/deeper/a.txt` asks
// about b.txt/deeper, which cannot be stat'ed at all because b.txt is a file, so
// the answer is only found one level up.
//
// Existence is asked with Lstat and directoryness with Stat, on purpose: a
// symlink pointing at a directory IS a directory to land in, while a dangling
// one is a name that is taken by something that is not.
func nonDirectoryAncestor(repoRoot, dir string) string {
	sep := string(filepath.Separator)
	for cur := dir; ; {
		rel, err := filepath.Rel(repoRoot, cur)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			return ""
		}
		if _, err := os.Lstat(cur); err == nil {
			if info, serr := os.Stat(cur); serr != nil || !info.IsDir() {
				return filepath.ToSlash(rel)
			}
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
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
	// ignoreCase is git's own core.ignorecase answer for this repository, read
	// once per invocation by the caller and shared with the validation that
	// decided a case-only destination was free. See gitIgnoreCase.
	ignoreCase bool
	// createMissingDirs is the caller's election to have a destination's absent
	// parent directories minted. Without it the validation has already refused
	// every pair whose parent is not there, so this half never has one to make.
	createMissingDirs bool
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
func performMvMoves(flags globalFlags, ignoreCase, createMissingDirs bool, repoRoot string, pairs []mvPair) error {
	fs := &mvFilesystem{
		flags:             flags,
		ignoreCase:        ignoreCase,
		createMissingDirs: createMissingDirs,
		ensured:           map[string]bool{},
	}
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

	// Only when the caller elected it. Without the election the validation
	// refused every pair whose parent was absent, so there is nothing here to
	// make and no directory this command could mint unasked.
	if fs.createMissingDirs {
		if err := fs.ensureParent(absNew); err != nil {
			return err
		}
	}
	if p.caseOnly && fs.ignoreCase {
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

// gitIgnoreCase reports git's own core.ignorecase answer for this repository:
// whether the filesystem folds case, i.e. whether two spellings of one name are
// one file.
//
// It is read from the repository rather than probed from the filesystem so that
// safegit and git agree about which world they are in, and it is the single
// authority for that question in this command -- both the destination check and
// the two-step rename divide on it, and an invocation in which they divided
// differently would validate one world and act in another. An unset or
// unreadable key is `false`, which is git's own default answer.
func gitIgnoreCase(ctx context.Context) bool {
	out, _, err := git.Run(ctx, "config", "--get", "core.ignorecase")
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
		record, err := trailer.NewRecord(p.old, p.new, trailer.OriginDeclared)
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
	// The commit-stands verdict is the opposite of the refusal below it: the ref
	// moved, so the files are at their new paths AND the commit that records the
	// moves exists. Reporting it as "the commit failed" would send the operator
	// to make a second one. See aftercare.go.
	var residue []residueEntry
	if partial := commitStands(err); partial != nil && result != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		residue = recordAftercareFailure(residue, partial.Step, err.Error())
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "error: the paths were moved, but the commit failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  the files are at their new paths. Commit them with 'safegit commit --moved' once the cause is fixed.\n")
		return pipelineExitCode(err)
	}

	if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, mvOplogOp, firstLine(message)); err != nil {
		residue = reportAftercareFailure(residue, stepParentBump, err)
	}

	flags.payload(mvPayload{
		Ref:      result.Ref,
		Parents:  orEmpty(result.Parents),
		Tree:     result.Tree,
		SHA:      realSHA(flags, result.SHA),
		Moves:    moves,
		Files:    orEmpty(result.Files),
		Attempts: result.Attempts,
		Residue:  orEmptyResidue(residue),
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
	return aftercareExit(residue)
}
