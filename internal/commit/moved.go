package commit

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/trailer"
)

// Declared moves.
//
// A move is DECLARED, never detected. safegit used to read a commit that added
// a file whose blob already existed elsewhere as a rename, and stage a deletion
// the caller never named; that guess is gone and nothing replaces it. Blob
// equality decides nothing here either: two identical files are two identical
// files, and one of them being a move of the other is a fact about intent that
// only the caller has.
//
// So `--moved 'old -> new'` is the caller stating it. What safegit does with
// the statement is check it against the repository -- the old path has to be
// something the commit's parent tracked and something the working tree no
// longer has, and the new path has to be somewhere the commit can see -- and
// then write it into the commit message as a record (internal/trailer), where
// it survives every clone and every tool that has never heard of safegit.
//
// The check is not a formality. A declaration the repository contradicts is
// almost always a typo or a stale command line, and a record written from one
// would send every later reader to a path that was never there.

// movedDeclaration is one --moved element after parsing and canonicalization.
type movedDeclaration struct {
	// arg is the element verbatim, so a refusal names what the caller typed.
	arg string
	// old and new are canonical repo-relative paths. A trailing slash on both
	// marks the subtree form, exactly as in the record that gets written.
	old string
	new string
}

func (d movedDeclaration) subtree() bool { return strings.HasSuffix(d.old, "/") }

// oldPrefix and newPrefix are the paths without the subtree form's slash, which
// is the form every tree and filesystem question is asked in.
func (d movedDeclaration) oldPrefix() string { return strings.TrimSuffix(d.old, "/") }
func (d movedDeclaration) newPrefix() string { return strings.TrimSuffix(d.new, "/") }

// movedRefusal is the refusal for a declaration the repository contradicts.
func movedRefusal(format string, args ...interface{}) error {
	return &CommitError{Code: exitcode.MoveNotBorneOut, Message: fmt.Sprintf(format, args...)}
}

// resolveMoved turns the caller's --moved elements into the trailer lines the
// commit will carry, refusing any declaration the repository contradicts.
//
// parentRev is the revision whose tree the commit's parent has -- the branch
// tip for a commit, and the tip's OWN first parent for an amend or a reword,
// because those replace that tip and their records describe the same step it
// described. An empty parentRev is a commit with no parent at all, where
// nothing can have moved.
//
// The ids are minted HERE, once, before the compare-and-swap loop: a retry must
// not change the record a commit carries, and a declaration that will be
// refused must be refused before anything is staged.
func resolveMoved(ctx context.Context, repoRoot, parentRev string, moved []string) ([]string, error) {
	if len(moved) == 0 {
		return nil, nil
	}

	declarations, err := parseMovedArgs(repoRoot, moved)
	if err != nil {
		return nil, err
	}
	if err := refuseOverlappingDeclarations(declarations); err != nil {
		return nil, err
	}

	tree := newTreeIndex(ctx, parentRev)
	lines := make([]string, 0, len(declarations))
	for _, d := range declarations {
		if err := validateMoved(ctx, repoRoot, parentRev, tree, d); err != nil {
			return nil, err
		}
		record, err := trailer.NewRecord(d.old, d.new)
		if err != nil {
			return nil, &CommitError{Code: exitcode.Usage, Message: fmt.Sprintf("--moved %s: %v", d.arg, err)}
		}
		lines = append(lines, trailer.RecordLine(record))
	}
	return lines, nil
}

// parseMovedArgs reads every --moved element through the ONE pair parser and
// canonicalizes both sides.
//
// Canonicalization is what makes `--moved 'a.txt -> b.txt'` mean the same thing
// from a subdirectory as from the root: the same treatment every positional
// path gets at intake, applied here because a record resolved against a tree
// must name the path the tree names. The subtree marker is carried across the
// canonicalization, which strips a trailing slash, so the spelling the caller
// used still decides whether the record speaks for a prefix.
func parseMovedArgs(repoRoot string, moved []string) ([]movedDeclaration, error) {
	out := make([]movedDeclaration, 0, len(moved))
	for _, arg := range moved {
		old, new, err := trailer.ParsePair(arg)
		if err != nil {
			return nil, &CommitError{Code: exitcode.Usage, Message: fmt.Sprintf("--moved %s: %v", arg, err)}
		}
		subtree := strings.HasSuffix(old, "/")
		oldRel, err := canonicalRel(repoRoot, old, subtree)
		if err != nil {
			return nil, &CommitError{Code: exitcode.Usage, Message: fmt.Sprintf("--moved %s: %v", arg, err)}
		}
		newRel, err := canonicalRel(repoRoot, new, subtree)
		if err != nil {
			return nil, &CommitError{Code: exitcode.Usage, Message: fmt.Sprintf("--moved %s: %v", arg, err)}
		}
		if oldRel == "" || newRel == "" {
			return nil, &CommitError{
				Code:    exitcode.Usage,
				Message: fmt.Sprintf("--moved %s names the repository root, which cannot move", arg),
			}
		}
		if subtree {
			oldRel += "/"
			newRel += "/"
		}
		if err := trailer.ValidatePair(oldRel, newRel); err != nil {
			return nil, &CommitError{Code: exitcode.Usage, Message: fmt.Sprintf("--moved %s: %v", arg, err)}
		}
		out = append(out, movedDeclaration{arg: arg, old: oldRel, new: newRel})
	}
	return out, nil
}

// refuseOverlappingDeclarations rejects a set of declarations that speak about
// each other's paths.
//
// Two declarations whose old paths nest -- `src/ -> lib/` alongside
// `src/a.go -> other.go` -- state two different fates for one file. A reader
// can resolve that (the longest match wins), but a WRITER guessing which one
// the caller meant is exactly the kind of silent precedence rule this tool does
// not have: the caller says one thing about each path, and says it once. The
// same holds on the destination side, where two declarations landing inside one
// another describe a result no move produces.
func refuseOverlappingDeclarations(declarations []movedDeclaration) error {
	for i := range declarations {
		for j := i + 1; j < len(declarations); j++ {
			a, b := declarations[i], declarations[j]
			if side, x, y := overlap(a, b); side != "" {
				return &CommitError{
					Code: exitcode.Usage,
					Message: fmt.Sprintf("--moved %s and --moved %s both speak for %s (%s and %s); "+
						"say one thing about each path", a.arg, b.arg, side, x, y),
				}
			}
		}
	}
	return nil
}

// overlap reports which side of two declarations nests, if either does.
func overlap(a, b movedDeclaration) (side, x, y string) {
	if nests(a.oldPrefix(), b.oldPrefix()) {
		return "the same source", a.oldPrefix(), b.oldPrefix()
	}
	if nests(a.newPrefix(), b.newPrefix()) {
		return "the same destination", a.newPrefix(), b.newPrefix()
	}
	return "", "", ""
}

// nests reports whether two paths are the same path or one is inside the other.
func nests(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// validateMoved is the whole check one declaration gets. Three questions, each
// asked of the repository rather than of the caller:
//
//   - is the old path something the commit's parent TRACKED? A move out of a
//     path nothing ever held is not a move.
//   - is the old path GONE from the working tree? A path still sitting there
//     was not moved, whatever the command line says.
//   - is the new path somewhere the commit can SEE it -- on disk, which is what
//     the pipeline stages from, or already in the tree the commit is built on,
//     which is a move recorded after the fact.
//
// Blob equality is asked about nowhere. Two files holding identical bytes are
// two files; whether one is the other moved is the caller's statement to make.
func validateMoved(ctx context.Context, repoRoot, parentRev string, tree *treeIndex, d movedDeclaration) error {
	if parentRev == "" {
		return movedRefusal("--moved %s declares a move out of %s, but this commit has no parent: "+
			"nothing existed for it to move from", d.arg, d.oldPrefix())
	}

	tracked, err := trackedPaths(tree, d)
	if err != nil {
		return err
	}
	if len(tracked) == 0 {
		what := "is not tracked in"
		if d.subtree() {
			what = "holds no paths tracked in"
		}
		return movedRefusal("--moved %s declares a move out of %s, which %s %s",
			d.arg, d.oldPrefix(), what, describeBase(parentRev))
	}

	for _, path := range tracked {
		if _, err := os.Lstat(git.Anchor(repoRoot, path)); err == nil {
			return movedRefusal("--moved %s declares a move out of %s, but %s is still on disk; "+
				"a declared move records where content WENT, so the old path has to be gone",
				d.arg, d.oldPrefix(), path)
		}
	}

	present, err := destinationPresent(repoRoot, tree, d)
	if err != nil {
		return err
	}
	if !present {
		where := "is on neither disk nor"
		if d.subtree() {
			where = "holds no files on disk and no paths in"
		}
		return movedRefusal("--moved %s declares a move into %s, which %s %s",
			d.arg, d.newPrefix(), where, describeBase(parentRev))
	}
	return nil
}

// trackedPaths returns the parent-tree paths a declaration's old side covers:
// the path itself for the file form, and everything under the prefix for the
// subtree form.
func trackedPaths(tree *treeIndex, d movedDeclaration) ([]string, error) {
	if d.subtree() {
		return tree.under(d.oldPrefix())
	}
	if _, ok, err := tree.entry(d.old); err != nil {
		return nil, err
	} else if ok {
		return []string{d.old}, nil
	}
	return nil, nil
}

// destinationPresent reports whether the new side is somewhere the commit can
// see it.
func destinationPresent(repoRoot string, tree *treeIndex, d movedDeclaration) (bool, error) {
	if d.subtree() {
		under, err := tree.under(d.newPrefix())
		if err != nil {
			return false, err
		}
		if len(under) > 0 {
			return true, nil
		}
		listing, err := os.ReadDir(git.Anchor(repoRoot, d.newPrefix()))
		return err == nil && len(listing) > 0, nil
	}
	if _, err := os.Lstat(git.Anchor(repoRoot, d.new)); err == nil {
		return true, nil
	}
	_, ok, err := tree.entry(d.new)
	return ok, err
}

// movedParentRev names the revision whose tree an amend's or a reword's move
// declarations are judged against: the FIRST PARENT of the tip being replaced.
//
// A commit's records describe the step from its parent to itself. An amend
// replaces the tip, so its records describe that same step -- from the tip's
// parent, not from the tip, whose tree already holds the result of whatever the
// caller is now declaring. Getting this wrong would refuse every honest
// `commit --amend --moved` with "the old path is not tracked", because the tip
// being amended is precisely where it stopped being tracked.
func movedParentRev(parents []string) string {
	if len(parents) == 0 {
		return ""
	}
	return parents[0]
}

// commitTrailers is the trailer list one commit carries, in the order the
// message ends up holding them: the caller's own --trailer values, then the
// move records this operation is preserving from the message it replaces, then
// the records it is declaring now.
//
// All of it goes on BEFORE the repository's commit-msg hook runs, because all
// of it is the caller's own content -- a hook that inspects or rewrites the
// message must see the move records the same way it sees everything else the
// caller wrote. safegit's session trailer is the one thing that goes on after
// the hook, so a rewriting hook cannot strip the attribution.
//
// Preserved lines that the operation is re-declaring are dropped rather than
// doubled: two identical records would be two claims where the caller made one.
func commitTrailers(userTrailers, preserved, declared []string) []string {
	out := make([]string, 0, len(userTrailers)+len(preserved)+len(declared))
	out = append(out, userTrailers...)
	seen := make(map[string]bool, len(declared))
	for _, line := range declared {
		seen[line] = true
	}
	for _, line := range preserved {
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return append(out, declared...)
}

// preservedMovedLines are the move records a REPLACEMENT message must carry
// forward.
//
// An amend or a reword with -m replaces the message wholesale, and a record
// that vanished with it would be a record dropped in silence -- while the move
// it describes is still exactly what the commit did. Dropping a record is a
// RETRACTION, which is a thing a caller states; it is never a side effect of
// rewording. When the message is being KEPT rather than replaced (an amend with
// no -m), the lines are already in it and nothing is carried.
func preservedMovedLines(oldMessage string, replaced bool) []string {
	if !replaced {
		return nil
	}
	return trailer.MovedLines(oldMessage)
}
