package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
)

// The subset boundary of every command that forwards a git command line.
//
// safegit implements a deliberate subset of git, and the commands that take
// git's own vocabulary -- switch, merge, cherry-pick, revert, rebase, reset,
// bisect -- are where that subset has to be stated, because an operator can
// type anything git accepts and expect it to arrive.
//
// The tables below are ALLOWLISTS, and the direction is the whole point. A
// refusal list answers "is this one of the things we already thought about and
// decided against"; an option nobody has thought about passes it and reaches
// git, where it can change what git does while safegit's checks, its record of
// the operation and its report are still written for something else. An
// allowlist answers the other question -- "is this one of the things safegit
// can honor" -- and an option nobody has thought about is refused, which is the
// only honest answer for a tool that promises a subset.
//
// Two refusals come out of one table:
//
//   - a NAMED capability carries its own reason, because "safegit does not
//     select merge strategies, and here is why" is an answer an operator can
//     act on, while "unsupported option" is not;
//   - everything else carries the subset law itself.
//
// Both are parser-shaped (exit 2) and both happen BEFORE any lock is taken and
// before any git runs.
//
// DIVERGENCE: every entry in every table below is one git capability safegit
// deliberately does not have. They are cataloged under "The subset boundary" in
// docs/divergences.md -- the default-deny stance itself as "The forwarded
// command line is an allowlist, not a refusal list", and each refused capability
// class as its own entry, with the ALLOWED sets tabulated beside them under
// "What each guarded command allows". A capability added to or removed from a
// table below changes that section in the same commit.

// refusedCapability is one capability the command deliberately lacks, with the
// reason it lacks it. The names are every spelling git accepts for it, so a
// refusal fires whichever one was typed.
type refusedCapability struct {
	names []string
	why   string
}

// argvSubset is one command's slice of git's vocabulary.
type argvSubset struct {
	// command is the safegit command's own name, as an operator types it.
	command string
	// allowed lists every option spelling the command honors. An option outside
	// it is refused, whether or not anybody has considered it.
	allowed []string
	// refused are the capabilities that get their own reason. They are a subset
	// of "not allowed" -- listing one here changes only the message.
	refused []refusedCapability
}

// refuseUnsupportedOptions is the default-deny check. It returns 0 when every
// option on the command line is one this command honors, and the exit code of
// the refusal otherwise.
func (s argvSubset) refuseUnsupportedOptions(parsed gitArgs) int {
	for _, o := range parsed.Options {
		// FIRST, because it is a different answer from either of the two below:
		// this is a flag safegit HAS, written where this command cannot see it.
		if frameworkOwnedFlag(o.Name) {
			return refuseMisplacedFrameworkFlag(s.command, o.Name)
		}
		if why, named := s.refusalReason(o.Name); named {
			return refuseCapability(s.command, o.Name, why)
		}
		if !s.allows(o.Name) {
			return refuseUnlistedOption(s.command, o.Name)
		}
	}
	return 0
}

// frameworkOwnedFlags are the flags strictcli owns on every command safegit
// has: the reserved quartet plus `--json`, all pre-scanned out of argv wherever
// they appear and delivered on the dispatch Context (see the registration
// comment in main.go).
//
// The list is spelled here rather than derived because the framework exposes no
// enumeration of it, and it is short and ratified: the quartet has no short
// forms by design, so a git short option can never collide with one.
var frameworkOwnedFlags = []string{
	"--dry-run",
	"--json",
	"--quiet",
	"--verbose",
	"--approve-consequential",
}

// frameworkOwnedFlag reports whether name is one of them.
func frameworkOwnedFlag(name string) bool { return containsString(frameworkOwnedFlags, name) }

// refuseMisplacedFrameworkFlag is the refusal for a framework-owned flag
// written AFTER a guarded command's name.
//
// The pre-scan that recognizes these flags anywhere in the command line stops
// at a passthrough command's name: everything after it is git's own vocabulary,
// handed to safegit's git-shaped parser, which measures it against this
// command's allowlist and finds a flag that is not in it. The refusal is right
// -- the flag is not part of the command's git vocabulary, and forwarding it to
// git would be worse -- but the REASON the allowlist gives is not: citing the
// subset law for `--json` says safegit deliberately lacks a capability it has
// on every command it has.
//
// So this one names the route instead. It is the whole difference: the operator
// wanted machine mode, or a preview, and has to write it one word earlier.
func refuseMisplacedFrameworkFlag(command, option string) int {
	fmt.Fprintf(os.Stderr, "error: safegit %s does not support %s after the command name\n", command, option)
	fmt.Fprintf(os.Stderr, "  %s is safegit's own flag rather than one of git's, and it is read BEFORE the command\n", option)
	fmt.Fprintf(os.Stderr, "  name. After that name the command line is git's vocabulary, which does not include it.\n")
	fmt.Fprintf(os.Stderr, "  Write it before the command name:\n")
	fmt.Fprintf(os.Stderr, "    safegit %s %s ...\n", option, command)
	return exitcode.Usage
}

// refusalReason reports whether this option names a capability the table
// refuses by name, and with what reason.
func (s argvSubset) refusalReason(name string) (string, bool) {
	for _, r := range s.refused {
		if containsString(r.names, name) {
			return r.why, true
		}
	}
	return "", false
}

// allows reports whether this option is on the allowlist.
func (s argvSubset) allows(name string) bool { return containsString(s.allowed, name) }

// containsString reports membership, matched exactly as the option was spelled.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// refuseCapability is the refusal for a capability the table names.
func refuseCapability(command, option, why string) int {
	fmt.Fprintf(os.Stderr, "error: safegit %s does not support %s\n", command, option)
	fmt.Fprintf(os.Stderr, "  %s.\n", why)
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.Usage
}

// refuseUnlistedOption is the refusal for everything else: the subset law
// itself, stated as the reason.
func refuseUnlistedOption(command, option string) int {
	fmt.Fprintf(os.Stderr, "error: safegit %s does not support %s\n", command, option)
	fmt.Fprintf(os.Stderr, "  safegit %s accepts only the options it can honor, and this is not one of them.\n", command)
	fmt.Fprintf(os.Stderr, "  An option safegit has not considered would change what git does while safegit's own\n")
	fmt.Fprintf(os.Stderr, "  checks, its record of the operation and its report stayed written for something else.\n")
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.Usage
}

// mergeSubset is `safegit merge`'s slice of `git merge`.
//
// What is allowed is what reaches only the compute step or the message draft
// git writes there: the merge is computed by git and committed by safegit's
// pipeline, so an option that changes how the message is DRAFTED is honored
// (the pipeline commits that draft) while one that changes how git would COMMIT
// is not (nothing of git's commit path runs).
//
// Reaching the compute step is what makes an option ELIGIBLE, not what makes it
// allowed: a compute-step option may still be refused for a reason of its own.
// Strategy SELECTION is refused because the conclusion's checks cannot read what
// another strategy stages, while strategy OPTIONS, which tune the same ort
// compute without changing what those checks see, are honored; and two more are
// refused for reasons of a different kind altogether --
// `--allow-unrelated-histories` for the hazard rather than a protection hole, and
// `--rerere-autoupdate` because what it stages is a REMEMBERED resolution
// nobody made in this operation.
var mergeSubset = argvSubset{
	command: "merge",
	allowed: []string{
		// The message the conclusion commits. git writes both into MERGE_MSG at
		// the compute step, which is exactly where the pipeline reads it from.
		"-m", "--message", "-F", "--file",
		// No editor is opened on any path here, so the request is already true.
		"--no-edit",
		// safegit's own fast-forward and commit selections. They never reach git:
		// the compute step is pinned to --no-ff --no-commit and these are removed
		// from the argv it forwards (see withoutMergeSelectors). --commit is NOT
		// among them: it is refused by name, below.
		"--ff", "--no-ff", "--ff-only", "--no-commit",
		// Message-draft flags: git writes the trailer, the shortlog and the
		// "into <name>" wording into MERGE_MSG, and the pipeline commits it.
		"--signoff", "--no-signoff", "--log", "--no-log", "--into-name",
		// Narration only.
		"--stat", "--no-stat",
		// Strategy OPTIONS, in both of git's spellings -- they are two distinct
		// option names rather than one alias of the other. They tune the ort
		// compute's content decisions without changing authorship, parking, or
		// anything a conclusion reads: the compute stays ort, AUTO_MERGE is
		// written exactly as it is without them (probed), and every protection
		// over the staged result sees what it sees today. Strategy SELECTION,
		// which does change that, stays refused below.
		"-X", "--strategy-option",
		// The NEGATIVE spelling only. The row is SPLIT, and the asymmetry is
		// deliberate however odd it looks: --rerere-autoupdate is refused by
		// name below, and --no-rerere-autoupdate -- which turns that same
		// mechanism OFF -- is allowed here, because it is an operator's only
		// per-run switch against the `rerere.autoUpdate` config key, which
		// safegit still honors. Refusing the off-switch would leave an operator
		// whose configuration turns auto-staging on with no way to run one merge
		// without it. cherry-pick's and revert's rows carry the same split and
		// point back at this comment.
		"--no-rerere-autoupdate",
		// The state-control forms, which author nothing. --continue is refused
		// further in, by name, because concluding a merge is safegit's own job.
		"--continue", "--abort", "--quit",
	},
	refused: []refusedCapability{
		{
			[]string{"-s", "--strategy"},
			"safegit's merge does not select a merge strategy: the conclusion's completeness and conflict-marker checks are written against what the default strategy stages, and a strategy they cannot read would be protected by nothing",
		},
		{
			// DIVERGENCE: git stages a remembered resolution when asked;
			// safegit refuses the request. Cataloged in docs/divergences.md as
			// "The rerere auto-update flag is refused, and its config key is
			// not". cherry-pick and revert refuse it in the same words.
			[]string{"--rerere-autoupdate"},
			"--rerere-autoupdate stages a remembered resolution from rerere's cache during the compute step, which is a resolution nobody made in this operation arriving as if somebody had; safegit's conclusion is built on the operator declaring what each conflicted path resolves to. The `rerere.autoUpdate` config key does the same thing and IS still honored -- see 'The rerere auto-update flag is refused, and its config key is not' in docs/divergences.md -- and --no-rerere-autoupdate is allowed, so a run can turn it off",
		},
		{
			// DIVERGENCE: git refuses two unrelated histories by default and
			// offers this flag to elect the merge anyway; safegit refuses the
			// flag, and refuses the merge itself before anything computes. See
			// refuseUnrelatedHistories and the catalog entry it names.
			[]string{"--allow-unrelated-histories"},
			"safegit does not merge histories that share no commit: the state is nearly always reached by accident -- a wrong remote, a wrong branch, a repository re-initialized over another -- and the commit it makes is a permanent second root. For the deliberate import, compute it with 'git merge --no-commit --allow-unrelated-histories <branch>' and commit that with 'safegit merge-continue'",
		},
		{
			[]string{"--squash"},
			"--squash stages a merge's result without recording its parents, which is a commit with a merge's content and none of its history; safegit's merge records a merge commit or nothing",
		},
		{
			[]string{"-e", "--edit"},
			"--edit opens an editor, and safegit's commit surface has none; pass -m to give the merge commit its message",
		},
		{
			// DIVERGENCE: git's merge takes --commit and commits; safegit's
			// refuses it by name. Cataloged in docs/divergences.md as "Options
			// that would hand the commit or the ref back to git are refused".
			//
			// It was accepted and quietly stripped from the forwarded argv,
			// which is the shape safegit refuses everywhere else: an operator
			// who asks for something gets it or gets told they cannot have it.
			// And the request is not a no-op -- git takes the LAST of the pair,
			// so a --commit trailing the --no-commit the compute step is pinned
			// to would hand the commit back to git. cherry-pick and revert
			// refuse it in the same words, for the same reason.
			[]string{"--commit"},
			"--commit is the opposite of the --no-commit the compute step is pinned to, and passing it would hand the commit back to git -- authored by git, with none of safegit's trailers, and moving the ref outside safegit's compare-and-swap",
		},
		{
			[]string{"--autostash"},
			"--autostash is a dead flag through safegit: the coordination check refuses a dirty working tree before git runs, so a merge never reaches git with anything to stash -- and in a shared worktree those changes may be another session's work. Commit your changes first",
		},
		{
			// The compute step never commits, so --no-verify would reach git and
			// do nothing; the commit is the pipeline's, and the pipeline has no
			// way to skip the repository's commit-msg hook.
			[]string{"--no-verify"},
			"--no-verify skips the hooks GIT's own commit path runs, and the commit here is safegit's pipeline's: it runs the repository's commit-msg hook and has no flag that turns it off. Nothing would be skipped, so the request is refused rather than quietly ignored",
		},
		{
			[]string{"-S", "--gpg-sign"},
			"safegit's commit pipeline does not sign commits, so a signature asked for here would simply not be on the result; nothing is silently dropped, so the request is refused instead",
		},
	},
}

// cherryPickSubset is `safegit cherry-pick`'s slice of `git cherry-pick`.
var cherryPickSubset = argvSubset{
	command: "cherry-pick",
	allowed: []string{
		// git writes both into the message draft during the compute step, and
		// the pipeline commits that draft.
		"-x", "-s", "--signoff", "--no-edit",
		// Which parent of a merge commit the pick is relative to: a compute-step
		// question about what patch to apply.
		"-m", "--mainline",
		// Strategy OPTIONS, in both spellings. They tune the ort compute's
		// content decisions and change neither authorship, parking, nor anything
		// a conclusion reads; strategy SELECTION, which does, stays refused
		// below. On this command the SHORT `-s` is signoff rather than strategy
		// selection, which is git's own spelling and is why the refusal below
		// names only the long one.
		"-X", "--strategy-option",
		// The operator asking for the pick to be computed and left staged. It
		// authors nothing, so it is forwarded to git -- but it COMPUTES, so it
		// goes through the computing door, which refuses over an operation git
		// already has in flight (runComputingPassthrough). "Authors nothing" is
		// why it may be forwarded; it is not why it would be exempt from that
		// refusal, and the state-control rows three lines below are exempt for the
		// different reason spelled there.
		"-n", "--no-commit",
		// The NEGATIVE spelling only -- the split mergeSubset's row explains.
		"--no-rerere-autoupdate",
		// The state-control forms, which author nothing AND are the way out of a
		// parked operation -- so they are the ones that must not be refused over
		// the state they exist to clear.
		"--continue", "--abort", "--quit",
	},
	refused: []refusedCapability{
		{
			[]string{"--skip"},
			"--skip moves past one commit of a SEQUENCE and keeps the rest going, and safegit's cherry-pick has no sequence to keep going: it picks one commit. Abandon the pick with 'git cherry-pick --abort' and pick the commits you do want, one invocation each",
		},
		{
			[]string{"-e", "--edit"},
			"--edit opens an editor, and safegit's commit surface has none; conclude with 'safegit cherry-pick-continue -m' to give the commit a message of your own",
		},
		{
			[]string{"-S", "--gpg-sign"},
			"safegit's commit pipeline does not sign commits, so a signature asked for here would simply not be on the result; nothing is silently dropped, so the request is refused instead",
		},
		{
			[]string{"--strategy"},
			"safegit's cherry-pick does not select a merge strategy: the conclusion's completeness and conflict-marker checks are written against what the default strategy stages -- a pick computed with another one parks a content conflict git recorded nowhere the checks can read -- so a strategy they cannot see would be protected by nothing",
		},
		{
			[]string{"--rerere-autoupdate"},
			"--rerere-autoupdate stages a remembered resolution from rerere's cache during the compute step, which is a resolution nobody made in this operation arriving as if somebody had; safegit's conclusion is built on the operator declaring what each conflicted path resolves to. The `rerere.autoUpdate` config key does the same thing and IS still honored -- see 'The rerere auto-update flag is refused, and its config key is not' in docs/divergences.md -- and --no-rerere-autoupdate is allowed, so a run can turn it off",
		},
		{
			[]string{"--ff"},
			"--ff lets git move the branch onto the picked commit outright, which is a ref move outside safegit's compare-and-swap and a commit safegit did not author. safegit's cherry-pick always makes a commit of its own",
		},
		{
			[]string{"--commit"},
			"--commit is the opposite of the --no-commit the compute step is pinned to, and passing it would hand the commit back to git -- authored by git, with none of safegit's trailers, and moving the ref outside safegit's compare-and-swap",
		},
		{
			[]string{"--cleanup"},
			"--cleanup governs how git strips a message at ITS commit time, and safegit's pipeline is what commits here; the message it takes is git's draft with its comment block stripped, or the text you pass to 'safegit cherry-pick-continue -m'",
		},
		{
			[]string{"--allow-empty", "--allow-empty-message", "--keep-redundant-commits", "--empty"},
			"these govern what git commits when a pick produces nothing, and safegit's pipeline refuses a commit that changes nothing outright; there is no flag here that turns that refusal off",
		},
	},
}

// revertSubset is `safegit revert`'s slice of `git revert`.
var revertSubset = argvSubset{
	command: "revert",
	allowed: []string{
		// git writes the signoff trailer into the message draft at the compute
		// step (probe-verified), and the pipeline commits that draft.
		"-s", "--signoff", "--no-edit",
		"-m", "--mainline",
		// Strategy OPTIONS, in both spellings, on the same reasoning as merge's
		// and cherry-pick's rows. Note that a revert applies an INVERSE patch, so
		// `-X theirs` keeps the revert and `-X ours` keeps the commit being
		// reverted -- the same reading the conclusion's stage keywords take.
		// Strategy SELECTION stays refused below, and its SHORT spelling is
		// signoff here, exactly as it is on cherry-pick.
		"-X", "--strategy-option",
		"-n", "--no-commit",
		// The NEGATIVE spelling only -- the split mergeSubset's row explains.
		"--no-rerere-autoupdate",
		// The reference line git puts in the draft naming the reverted commit.
		"--reference",
		"--continue", "--abort", "--quit",
	},
	refused: []refusedCapability{
		{
			[]string{"--skip"},
			"--skip moves past one commit of a SEQUENCE and keeps the rest going, and safegit's revert has no sequence to keep going: it reverts one commit. Conclude the revert you are in with 'safegit revert-continue', or drop it with 'git revert --abort', and then revert the commits you do want, one invocation each",
		},
		{
			[]string{"-e", "--edit"},
			"--edit opens an editor, and safegit's commit surface has none; conclude with 'safegit revert-continue -m' to give the commit a message of your own",
		},
		{
			[]string{"-S", "--gpg-sign"},
			"safegit's commit pipeline does not sign commits, so a signature asked for here would simply not be on the result; nothing is silently dropped, so the request is refused instead",
		},
		{
			[]string{"--strategy"},
			"safegit's revert does not select a merge strategy: the conclusion's completeness and conflict-marker checks are written against what the default strategy stages -- a revert computed with another one parks a content conflict git recorded nowhere the checks can read -- so a strategy they cannot see would be protected by nothing",
		},
		{
			[]string{"--rerere-autoupdate"},
			"--rerere-autoupdate stages a remembered resolution from rerere's cache during the compute step, which is a resolution nobody made in this operation arriving as if somebody had; safegit's conclusion is built on the operator declaring what each conflicted path resolves to. The `rerere.autoUpdate` config key does the same thing and IS still honored -- see 'The rerere auto-update flag is refused, and its config key is not' in docs/divergences.md -- and --no-rerere-autoupdate is allowed, so a run can turn it off",
		},
		{
			[]string{"--commit"},
			"--commit is the opposite of the --no-commit the compute step is pinned to, and passing it would hand the commit back to git -- authored by git, with none of safegit's trailers, and moving the ref outside safegit's compare-and-swap",
		},
		{
			[]string{"--cleanup"},
			"--cleanup governs how git strips a message at ITS commit time, and safegit's pipeline is what commits here; the message it takes is git's draft with its comment block stripped, or the text you pass to 'safegit revert-continue -m'",
		},
	},
}

// resetSubset is `safegit reset`'s slice of `git reset`.
//
// The modes with a commit argument are the whole of it. The PATHSPEC form is
// refused separately, in the handler, because it is a shape rather than an
// option: `git reset <commit> -- <path>` writes the shared index entry by entry,
// which is the one file safegit's whole design keeps out of.
var resetSubset = argvSubset{
	command: "reset",
	allowed: []string{"--hard", "--soft", "--mixed", "--merge", "--keep"},
	refused: []refusedCapability{
		{
			[]string{"-p", "--patch"},
			"--patch opens an interactive hunk session, and safegit's surface has no interactive mode anywhere; to unstage part of a file, reset the whole path and commit the hunks you want with 'safegit commit --hunks'",
		},
		{
			[]string{"--pathspec-from-file", "--pathspec-file-nul"},
			"these read a PATHSPEC from a file, and safegit's reset takes no pathspec at all: the pathspec form writes the shared index entry by entry, which is the one file safegit's design keeps out of",
		},
	},
}

// rebaseSubset is `safegit rebase`'s slice of `git rebase`.
//
// A rebase is the campaign's one declared exception: git performs the replay
// and AUTHORS the replayed commits, uniformly. What the allowlist keeps is the
// forms of that one door -- an upstream, a new base, the interactive session,
// the autostash, the topology of the range being replayed, and git's own
// state-control verbs -- and what it refuses is the apply backend (whose state
// safegit's readers do not speak) and the options that turn a rebase into
// something else.
var rebaseSubset = argvSubset{
	command: "rebase",
	allowed: []string{
		"--onto",
		"-i", "--interactive",
		"--continue", "--abort", "--skip",
		"--autostash",
		// Preserving merge topology through the replay. It creates merge
		// commits, but so does nothing else about this door's answer: the whole
		// replay runs inside the one place git is declared to author, which is
		// scoped to this verb, so a re-created merge is no more git's than a
		// linear commit replayed beside it and safegit checks neither.
		//
		// Its optional value (`--rebase-merges=rebase-cousins`) is ATTACHED
		// ONLY, and no valueFlags entry may be added for it -- see the rule
		// beside that map. ACCEPTED LIMIT: the short cluster spelling
		// `-rno-rebase-cousins` is therefore read letter by letter rather than
		// as one token, and refuses on the first letter that is not allowed.
		// The two spellings that carry a value, `-r no-rebase-cousins` (which
		// git does not accept either) and `--rebase-merges=no-rebase-cousins`,
		// are unaffected.
		"-r", "--rebase-merges",
	},
	refused: []refusedCapability{
		{
			[]string{"--apply"},
			"--apply selects git's apply backend, and safegit's rebase does not select a backend: the merge backend is what safegit's in-flight state reader, its conflict machinery and its refusals are written against, and the apply backend keeps its state under a directory `git am` shares",
		},
		{
			[]string{"--whitespace", "-C"},
			"these are the apply backend's patch-application options, and safegit's rebase does not select that backend",
		},
		{
			[]string{"-x", "--exec"},
			"--exec runs a command of your own after every replayed commit, and a rebase through safegit is a replay and nothing else; run the command yourself when the rebase finishes",
		},
		{
			[]string{"--root"},
			"--root rewrites every commit on the branch including the first, which is a history rewrite rather than a replay onto an upstream; safegit's history-rewriting surface is 'safegit scrub'",
		},
	},
}

// bisectSubset is `safegit bisect`'s slice of `git bisect`.
//
// Its allowlist is a SUBCOMMAND vocabulary rather than an option list (see
// refuseUnsupportedBisect), and the option list is deliberately empty: every
// option git's bisect takes renames its terms, changes what it checks out, or
// limits the walk, and none of them has been considered here.
var bisectSubset = argvSubset{
	command: "bisect",
	allowed: nil,
}

// bisectSubcommands is bisect's vocabulary, read from the git classification
// table rather than restated here: the table's conditional tokens are the
// mutating half of the same list, and the uncommitted-work guard derives from
// them, so what safegit ADMITS and what safegit KNOWS about what it admitted
// cannot drift apart.
func bisectSubcommands() []string {
	v, ok := gitexec.Lookup("bisect")
	if !ok {
		return nil
	}
	return v.Subcommands
}

// refuseUnsupportedRebase refuses the rebase command lines outside the one
// declared door.
func refuseUnsupportedRebase(parsed gitArgs) int {
	if code := rebaseSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}

	if len(parsed.AfterDoubleDash) > 0 {
		fmt.Fprintf(os.Stderr, "error: safegit rebase takes no pathspec\n")
		fmt.Fprintf(os.Stderr, "  a rebase replays whole commits; there is no part of one it can replay.\n")
		fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
		return exitcode.Usage
	}

	// git's own state-control verbs, which take no revision of their own: a
	// rebase's conclusion is git's, and safegit has no verb that finishes one.
	if parsed.Has("--continue", "--abort", "--skip") {
		if len(parsed.Revisions) > 0 {
			fmt.Fprintf(os.Stderr, "error: safegit rebase's --continue, --abort and --skip take no argument\n")
			fmt.Fprintf(os.Stderr, "  they act on the rebase git already has in flight, whatever it was started from.\n")
			return exitcode.Usage
		}
		return 0
	}

	switch len(parsed.Revisions) {
	case 1:
	case 0:
		fmt.Fprintf(os.Stderr, "error: safegit rebase names no upstream\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit rebase <upstream>, or safegit rebase --onto <newbase> <upstream>\n")
		fmt.Fprintf(os.Stderr, "  the upstream is named rather than read from configuration, so what is replayed\n")
		fmt.Fprintf(os.Stderr, "  onto what is on the command line and not in a file somewhere.\n")
		return exitcode.Usage
	default:
		fmt.Fprintf(os.Stderr, "error: safegit rebase takes exactly one upstream, and this names %d\n", len(parsed.Revisions))
		fmt.Fprintf(os.Stderr, "  `git rebase <upstream> <branch>` switches branches first, which is a navigation\n")
		fmt.Fprintf(os.Stderr, "  safegit makes you state: switch to the branch, then rebase it.\n")
		fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
		return exitcode.Usage
	}
	return 0
}

// refuseUnsupportedReset refuses the reset command lines outside the subset,
// from the argv alone: the options it does not implement, and the PATHSPEC form
// in the spellings the argv itself gives away.
//
// The pathspec form is refused rather than forwarded because it writes the
// SHARED INDEX entry by entry -- the one file safegit's whole design keeps out
// of, since a per-invocation temporary index is what makes concurrent sessions
// safe. What it does to a path another session staged is invisible to
// everything safegit records.
func refuseUnsupportedReset(flags globalFlags, parsed gitArgs) int {
	if code := resetSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}
	if len(parsed.AfterDoubleDash) > 0 {
		return refuseResetPathspec(parsed.AfterDoubleDash[0])
	}
	if len(parsed.Revisions) > 1 {
		// The first is the commit; anything after it can only be a path.
		return refuseResetPathspec(parsed.Revisions[1])
	}
	if len(parsed.Revisions) == 1 {
		// The bare form, `git reset <path>`, which git tells from a commit by
		// resolving it. safegit asks the same two questions of the repository
		// rather than guessing from the spelling -- and in the same ORDER git
		// asks them, so an argument that is both is read as the commit.
		//
		// An argument that is NEITHER is deliberately left to git: `safegit reset
		// --hard no-such-ref` must exit with git's own verdict on that argument,
		// which is the convention merge, cherry-pick and switch already follow.
		arg := parsed.Revisions[0]
		if _, err := git.RevParse(flags.ctx(), arg+"^{commit}"); err != nil && namesAPath(flags, arg) {
			return refuseResetPathspec(arg)
		}
	}
	return 0
}

// namesAPath reports whether an argument that does not resolve to a commit is
// nevertheless something git would read as a path: a file or directory on disk,
// or a path the index tracks (which covers one already deleted from the working
// tree).
func namesAPath(flags globalFlags, arg string) bool {
	if _, err := os.Lstat(arg); err == nil {
		return true
	}
	out, _, err := git.Run(flags.ctx(), "ls-files", "--", arg)
	return err == nil && strings.TrimSpace(out) != ""
}

// refuseResetPathspec is the one wording for every spelling of the pathspec
// form.
func refuseResetPathspec(path string) int {
	fmt.Fprintf(os.Stderr, "error: safegit reset does not support a pathspec (%q does not name a commit)\n", path)
	fmt.Fprintf(os.Stderr, "  the pathspec form of reset writes the SHARED index entry by entry, and safegit's whole\n")
	fmt.Fprintf(os.Stderr, "  design keeps out of that file: every commit stages into a temporary index of its own, so\n")
	fmt.Fprintf(os.Stderr, "  that concurrent sessions cannot stage over each other. A reset of one path would be the\n")
	fmt.Fprintf(os.Stderr, "  one exception, invisible to everything safegit records.\n")
	fmt.Fprintf(os.Stderr, "  safegit reset takes a commit: --soft, --mixed, --hard, --merge or --keep.\n")
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.Usage
}

// refuseUnsupportedBisect refuses a bisect command line whose subcommand is
// outside the classified vocabulary, and every option: what safegit forwards is
// the vocabulary its classification table declares, and nothing else.
func refuseUnsupportedBisect(parsed gitArgs) int {
	if code := bisectSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}
	if len(parsed.Revisions) == 0 {
		fmt.Fprintf(os.Stderr, "error: safegit bisect names no subcommand\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit bisect <%s>\n", strings.Join(bisectSubcommands(), "|"))
		return exitcode.Usage
	}
	if sub := parsed.Revisions[0]; !containsString(bisectSubcommands(), sub) {
		fmt.Fprintf(os.Stderr, "error: safegit bisect does not support %s\n", sub)
		fmt.Fprintf(os.Stderr, "  the subcommands safegit forwards are the ones its git classification table declares,\n")
		fmt.Fprintf(os.Stderr, "  because the same declaration is what tells safegit which of them write the working\n")
		fmt.Fprintf(os.Stderr, "  tree and therefore need the uncommitted-work check: %s.\n", strings.Join(bisectSubcommands(), ", "))
		fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
		return exitcode.Usage
	}
	return 0
}

// rebaseUpstream names what a rebase invocation replays onto, for its oplog
// entry. A state-control invocation names none: it continues, abandons or skips
// a step of the rebase git already has in flight.
func rebaseUpstream(parsed gitArgs) string {
	if len(parsed.Revisions) > 0 {
		return parsed.Revisions[0]
	}
	return ""
}
