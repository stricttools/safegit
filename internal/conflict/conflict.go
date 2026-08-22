// Package conflict reads and reproduces what git recorded about a conflicted
// path, so a conclusion can verify a resolution against what git actually
// wrote instead of against a guess.
//
// Four facts live here, each with one authority:
//
//   - the ATTRIBUTES in force on a path (the conflict marker size and the
//     conflict style), resolved from a named tree so a conflicted
//     .gitattributes cannot decide how its own conflict is read;
//   - the index's three STAGES per conflicted path (base, ours, theirs), with
//     an absent stage reported as absent rather than as an empty file;
//   - git's AUTO_MERGE tree, which for an ort merge holds the conflict-marked
//     content git wrote into the working tree, verbatim;
//   - the RECONSTRUCTION of that content from the stages, via git's own
//     merge-file, for the cases where AUTO_MERGE does not exist.
//
// Everything runs through internal/git, safegit's git boundary. The package
// holds no policy: it does not decide whether a surviving marker is an error,
// which conflicts are exempt, or what a conclusion should refuse -- those
// decisions belong to the conclusion engine and its marker verification, which
// call this. A caller that needs another attribute (the `merge` driver, an
// exemption attribute) asks git.CheckAttr with the same attribute source.
//
// One limit is structural rather than a matter of effort, and octopus_test.go
// asserts it against a real conflicted octopus merge: an octopus records no
// AUTO_MERGE, its index stages describe only the LAST pairwise step (stage 2 is
// an intermediate blob belonging to no commit), and its marker labels are
// random temporary file names. Region CONTENT is reproducible there; a
// byte-identical file is not.
package conflict

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitversion"
)

// DefaultMarkerSize is the conflict marker length git uses when the
// conflict-marker-size attribute says nothing.
const DefaultMarkerSize = 7

// MarkerSizeAttr is the attribute name git reads a per-path marker length from.
const MarkerSizeAttr = "conflict-marker-size"

// ConflictStyleKey is the configuration key that decides the shape of a
// conflicted region.
const ConflictStyleKey = "merge.conflictStyle"

// Attrs is everything that shaped the conflict markers git wrote for one path:
// how long the markers are, and which of git's conflicted-region styles was in
// force.
//
// The marker size is per path (an attribute); the style is per repository (a
// configuration value) and is copied onto every path so a consumer holds one
// complete answer per path rather than two half-answers.
type Attrs struct {
	MarkerSize int
	Style      git.ConflictStyle
}

// Resolve answers the conflict attributes for paths.
//
// attrSource is a tree-ish whose .gitattributes files are read instead of the
// working tree's. During a conclusion it is the FIRST PARENT's tree: an
// attribute that decides how safegit treats a conflict has to predate the
// conflict, and the working tree's own .gitattributes may be conflicted --
// marker-laden and meaningless -- at exactly the moment the question is asked.
// An empty attrSource reads the working tree, which is only right outside a
// conflict.
//
// An unreadable or non-positive conflict-marker-size is DELIBERATELY treated
// the way git treats it -- as absent, so the default applies. The purpose of
// this answer is to reproduce the bytes git wrote; being stricter than git
// would produce a faithful reading of the operator's intent and an unfaithful
// reconstruction of the file.
func Resolve(ctx context.Context, attrSource string, paths []string) (map[string]Attrs, error) {
	style, err := Style(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]Attrs, len(paths))
	if len(paths) == 0 {
		return out, nil
	}

	answers, err := git.CheckAttr(ctx, attrSource, []string{MarkerSizeAttr}, paths)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		out[p] = Attrs{MarkerSize: markerSize(answers[p][MarkerSizeAttr]), Style: style}
	}
	return out, nil
}

// Style reports the conflicted-region shape this repository writes, from
// merge.conflictStyle. An unset key is git's default; an unrecognized value is
// an error, never a silent default.
func Style(ctx context.Context) (git.ConflictStyle, error) {
	value, set, err := git.ConfigGet(ctx, ConflictStyleKey)
	if err != nil {
		return "", err
	}
	if !set {
		return git.StyleMerge, nil
	}
	return git.ParseConflictStyle(value)
}

// markerSize reads a conflict-marker-size attribute value, falling back to
// git's default for everything git itself ignores.
func markerSize(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return DefaultMarkerSize
	}
	return n
}

// Sides holds the three merge stages of one conflicted path. A nil field means
// the path did not exist on that side: an add/add conflict has no Base, and a
// delete/modify conflict has no Ours or no Theirs.
type Sides struct {
	Base   *git.UnmergedEntry // stage 1
	Ours   *git.UnmergedEntry // stage 2
	Theirs *git.UnmergedEntry // stage 3
}

// ContentConflict reports whether both sides are present, which is the only
// case that has a marked-up conflict region to reason about. A delete/modify or
// add/delete conflict has stages but nothing merge-file could have written.
func (s Sides) ContentConflict() bool { return s.Ours != nil && s.Theirs != nil }

// Stages groups an index's unmerged entries by path.
//
// indexPath names the index to read; an empty indexPath reads the repository's
// shared index -- which is where git leaves a conflict, and which safegit only
// ever reads.
func Stages(ctx context.Context, indexPath string) (map[string]Sides, error) {
	entries, err := git.UnmergedStages(ctx, indexPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Sides)
	for i := range entries {
		e := entries[i]
		sides := out[e.Path]
		switch e.Stage {
		case 1:
			sides.Base = &e
		case 2:
			sides.Ours = &e
		case 3:
			sides.Theirs = &e
		default:
			return nil, fmt.Errorf("index entry for %q carries stage %d, which is not one of 1, 2 or 3", e.Path, e.Stage)
		}
		out[e.Path] = sides
	}
	return out, nil
}

// AutoMergeRef is the ref git's ort merge writes: a tree holding, per
// conflicted path, exactly the bytes git put in the working tree.
const AutoMergeRef = "AUTO_MERGE"

// AutoMergeTree resolves AUTO_MERGE and reports whether it exists.
//
// Absence is a FACT, not a failure: git writes no AUTO_MERGE for an octopus
// merge (recorded and pinned in this package's tests), and it is gone entirely
// once an operation concludes. A caller that needs the recorded content for a
// content conflict and finds none must say so rather than verify less.
//
// The version floor is checked here because on a git older than the ort merge
// that introduced AUTO_MERGE the ref is absent for EVERY merge, and reporting
// that as "this merge simply has none" would silently downgrade every
// verification built on it.
//
// PRESENCE is not evidence of an operation in flight. A completed rebase leaves
// AUTO_MERGE behind -- git's own `rebase --continue` does it too, so it is not
// an artifact of how safegit finishes anything (asserted by
// internal/git's TestRebaseContinueControl). Anything that reads a leftover
// AUTO_MERGE as an interrupted operation will fire on a repository whose rebase
// finished normally; the operation's OWN state files are what say an operation
// is in flight.
func AutoMergeTree(ctx context.Context) (tree string, present bool, err error) {
	if err := git.RequireFeature(ctx, gitversion.MergeTreeWriteTree); err != nil {
		return "", false, err
	}
	// Presence and peeling are two questions, asked separately on purpose: the
	// first is answered quietly because absence is expected, the second loudly,
	// so an AUTO_MERGE that exists but does not name a tree is an error rather
	// than another spelling of "there is none".
	if _, _, runErr := git.Run(ctx, "rev-parse", "--verify", "--quiet", AutoMergeRef); runErr != nil {
		return "", false, nil
	}
	out, _, runErr := git.Run(ctx, "rev-parse", "--verify", AutoMergeRef+"^{tree}")
	if runErr != nil {
		return "", false, runErr
	}
	return strings.TrimSpace(out), true, nil
}

// AutoMergeBlob returns the conflict-marked content git recorded for one path,
// and whether AUTO_MERGE holds that path at all. A clean path in a conflicted
// merge is present in the tree with its merged content; a path AUTO_MERGE does
// not carry reports present=false.
func AutoMergeBlob(ctx context.Context, path string) (content []byte, present bool, err error) {
	tree, ok, err := AutoMergeTree(ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	sha, runErr := git.RevParse(ctx, tree+":"+path)
	if runErr != nil {
		return nil, false, nil
	}
	blob, err := git.CatFileBlob(ctx, sha)
	if err != nil {
		return nil, false, err
	}
	return blob, true, nil
}

// Labels are the three names git writes onto the conflict marker lines. They
// are part of the file's bytes, so a reconstruction that has to match git's own
// output byte for byte has to reproduce them.
//
// How git derives them, recorded from observation (git 2.54) because none of it
// is documented in a form a program can read:
//
//   - Ours is always "HEAD".
//   - For a MERGE, Theirs is the name the operator wrote on the command line
//     ("feature", or a raw object name). Git records it NOWHERE machine-readable
//     -- not in MERGE_HEAD, not in MERGE_MODE -- so a caller that needs byte
//     identity for a merge must supply it (MERGE_MSG's "Merge branch 'x'" is the
//     only trace, and it is prose). Base is the abbreviated object name of the
//     single merge base, "merged common ancestors" when there are several, and
//     "empty tree" when there is none.
//   - For a CHERRY-PICK, Theirs is "<abbrev> (<subject>)" of the commit being
//     applied and Base is "parent of <abbrev> (<subject>)".
//   - For a REVERT the two swap: Base names the commit and Theirs names its
//     parent, which is the concrete meaning of "theirs = the result of undoing
//     that commit".
type Labels struct {
	Ours   string
	Base   string
	Theirs string
}

// MergeLabels derives the labels a conflicted merge wrote. theirs is the name
// the merge named on the command line; bases are the merge bases, in any order.
func MergeLabels(ctx context.Context, theirs string, bases []string) (Labels, error) {
	l := Labels{Ours: "HEAD", Theirs: theirs}
	switch len(bases) {
	case 0:
		l.Base = "empty tree"
	case 1:
		short, err := git.AbbrevSHA(ctx, bases[0])
		if err != nil {
			return Labels{}, err
		}
		l.Base = short
	default:
		l.Base = "merged common ancestors"
	}
	return l, nil
}

// PickLabels derives the labels a conflicted cherry-pick of source wrote.
func PickLabels(ctx context.Context, source string) (Labels, error) {
	described, err := Describe(ctx, source)
	if err != nil {
		return Labels{}, err
	}
	return Labels{Ours: "HEAD", Base: "parent of " + described, Theirs: described}, nil
}

// RevertLabels derives the labels a conflicted revert of source wrote. It is
// PickLabels with the base and theirs sides exchanged, because a revert applies
// the inverse patch: the incoming side is what the commit's PARENT held.
func RevertLabels(ctx context.Context, source string) (Labels, error) {
	described, err := Describe(ctx, source)
	if err != nil {
		return Labels{}, err
	}
	return Labels{Ours: "HEAD", Base: described, Theirs: "parent of " + described}, nil
}

// Describe renders a commit the way git's sequencer names it on a marker line:
// the abbreviated object name, then the subject in parentheses.
//
// It is exported because the conclusion commands say the same thing in prose:
// a listing that explains what `theirs` resolves to for a revert names the
// commit being undone in exactly the spelling its conflict markers used.
func Describe(ctx context.Context, sha string) (string, error) {
	out, _, err := git.Run(ctx, "log", "-1", "--format=%h (%s)", sha)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// Reconstruct reproduces the conflict-marked content git wrote for one path,
// from the index's stages plus the attributes that were in force.
//
// It is git's own three-way file merge, so for a two-sided content conflict the
// result is byte-identical to what git put in the working tree and recorded in
// AUTO_MERGE -- provided the labels match, which is why Labels is an explicit
// argument and not something this function guesses.
//
// A side the conflict does not have is passed as empty content, which is what
// git's own merge does for an add/add conflict.
func Reconstruct(ctx context.Context, sides Sides, attrs Attrs, labels Labels) (content []byte, conflicted bool, err error) {
	ours, err := blobOf(ctx, sides.Ours)
	if err != nil {
		return nil, false, err
	}
	base, err := blobOf(ctx, sides.Base)
	if err != nil {
		return nil, false, err
	}
	theirs, err := blobOf(ctx, sides.Theirs)
	if err != nil {
		return nil, false, err
	}

	size := attrs.MarkerSize
	if size == 0 {
		size = DefaultMarkerSize
	}
	return git.MergeFile(ctx, ours, base, theirs, git.MergeFileOptions{
		OursLabel:   labels.Ours,
		BaseLabel:   labels.Base,
		TheirsLabel: labels.Theirs,
		Style:       attrs.Style,
		MarkerSize:  size,
	})
}

// blobOf reads one stage's content; an absent stage is empty content.
func blobOf(ctx context.Context, e *git.UnmergedEntry) ([]byte, error) {
	if e == nil {
		return nil, nil
	}
	return git.CatFileBlob(ctx, e.SHA)
}
