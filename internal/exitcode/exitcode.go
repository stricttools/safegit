// Package exitcode is safegit's single registry of process exit codes.
//
// Every numeric exit code safegit produces is a named constant here, and every
// exit site in the tool -- die(), os.Exit(), a handler's int return, a
// commit.CommitError's Code field -- names one of these constants rather than a
// bare literal. `scripts/exit-inventory` enumerates those sites mechanically
// from the AST; it is how the registry was derived and how a later reviewer
// re-derives it.
//
// There are two carve-outs, both deliberate.
//
// The first: the guarded passthroughs --
// switch, pull, merge, rebase, reset, bisect, cherry-pick and revert -- exit
// with the wrapped git command's OWN exit code once git has run. Those codes
// are git's (1 for a conflicted merge, 128 or 129 for a fatal error), they are
// foreign to safegit, and they are deliberately NOT registered here: safegit
// reports git's verdict verbatim rather than translating it, and a registry row
// would claim ownership of a number safegit does not choose. A code from one of
// those commands is safegit's own only when the failure happened before git ran
// -- the coordination guard, an uninitialized repository, a rejected argument.
// docs/commands-guide.md states the same split above the generated table.
//
// The second: a signal. When a SIGINT or a SIGTERM reaches a safegit process
// that holds a lock, internal/lock's handler releases the lock and exits
// 128 + the signal number -- 130 for SIGINT, 143 for SIGTERM -- which is the
// Unix shell convention every shell, supervisor and CI runner already reads
// that way. Those numbers are the convention's, not safegit's, and registering
// them would claim ownership of a number safegit does not choose; the exit-site
// guard does not see them either, because the status is computed rather than
// written as a literal. A signal exit says nothing about what the command was
// doing: it says the process was ended from outside, with its locks released.
//
// # Standing rule for the redesign campaign
//
// Every later campaign phase that introduces a new hard error registers its
// exit code HERE, in the same subphase that introduces it -- a new constant
// with a doc comment saying what the code means and which commands produce it,
// plus its row in All(). A phase that ships a new refusal without its registry
// entry is incomplete. exitcode_test.go enforces the half of this that a test
// can see: a constant declared and left out of All() (or the reverse) fails.
// The documentation table in docs/commands-guide.md is generated from All() by
// `scripts/gen-exit-table`, so it cannot drift from the registry.
//
// # What the framework owns
//
// strictcli refuses a malformed command line before dispatch -- an unknown
// flag, an unknown command, a missing required flag, an out-of-choice value --
// and those refusals exit 1, which is the framework's code and not safegit's to
// route. safegit's OWN argument validation, reached after a successful parse,
// exits Usage (2). The split is deliberate but not currently reconcilable: it
// awaits an upstream strictcli ruling on a usage-error code.
package exitcode

import (
	"fmt"
	"strings"
)

// The registry. Codes are stable: a released code never changes meaning.
const (
	// OK is a successful run. Produced by every command, and by the per-command
	// help output that `--help` and `-h` reach.
	OK = 0

	// General is an operation that failed for a reason with no more specific
	// code: a git invocation that returned an error, a file that could not be
	// read, a ref that would not resolve, a declined public-remote backup
	// confirmation. Produced by every command except version, which reads
	// nothing and cannot fail.
	General = 1

	// Usage is a command line safegit itself rejects after strictcli has
	// accepted it: mutually exclusive flags, a missing message, a hunk spec
	// that does not parse, an empty file list, a non-positive --count, a
	// malformed glob or --target value, a `mv` pair that does not parse or that
	// speaks for a path another pair already claims. Produced by commit, mv,
	// undo, scan, scrub file/match/run, author check, and the three guarded
	// passthroughs that take a bare positional argument (switch, merge,
	// rebase). Note that a
	// refusal by the framework's own parser exits General (1) instead -- see
	// the package comment.
	Usage = 2

	// NoRepository means the working directory is not inside a git repository
	// (or git is not installed). Produced by every command that resolves the
	// git directory, at that point -- which is every command except version,
	// author list and author check, none of which resolve it (they read git
	// log, and a failure there is General).
	NoRepository = 3

	// NotInitialized means safegit's own state directory could not be created
	// or read. Produced by every command that needs .git/safegit: commit,
	// push, undo, unlock, config, scan, hook, backup, scrub verify, the
	// guarded passthroughs, and the four rewrite commands.
	NotInitialized = 4

	// CoordinationBusy means the coordination guard refused: another safegit
	// operation, or an in-progress git sequencer state, owns the working tree.
	// Produced by switch, pull, merge, rebase, reset, bisect, cherry-pick,
	// revert and backup restore, and by commit (including its --amend and
	// reword forms), mv and undo, which refuse outright while git has a merge,
	// cherry-pick, revert, rebase or mailbox application in flight -- naming
	// the operation and the command that ends it. mv asks the question before
	// the first rename rather than leaving it to the commit pipeline, so a mv
	// run against a sequencer state moves nothing at all.
	//
	// The three conclusion commands -- merge-continue, cherry-pick-continue and
	// revert-continue -- produce it from the other direction, for the two ways a
	// conclusion can be asked for against the wrong state: run against an
	// operation it does not conclude (merge-continue during a cherry-pick), and
	// run when nothing is in flight at all. It is the same verdict as the
	// declaration check inside the commit pipeline gives for the same two
	// mismatches, so it is the same code.
	CoordinationBusy = 5

	// CASExhausted means the ref moved under every compare-and-swap attempt,
	// so the commit could not converge. Produced by commit, including its
	// --amend and reword forms.
	CASExhausted = 7

	// LockTimeout means a safegit lock could not be acquired within
	// lock.acquireTimeoutSeconds because a live holder still owns it. It covers
	// every safegit lock and every command that takes one, whichever lock and
	// wherever in the command the acquisition happens:
	//
	//   - the repo-wide rewrite lock: scrub file, scrub match, scrub run,
	//     author rewrite;
	//   - the worktree operation lock: switch, pull, merge, rebase, reset,
	//     bisect, cherry-pick, revert, commit, commit --amend, reword, mv, undo,
	//     and the three conclusion commands -- merge-continue,
	//     cherry-pick-continue and revert-continue;
	//   - a per-ref CAS lock: undo, and the commit pipeline's own acquisition
	//     inside the operation lock, which commit, commit --amend and reword
	//     reach through pipelineExitCode.
	//
	// The situation is one situation -- a live holder owns a lock this command
	// needs -- and the remedy is one remedy: wait for the holder, or release
	// the lock once it is provably gone. Which of safegit's locks it was, and
	// how deep in the command the acquisition sat, does not change either, so
	// it does not change the code.
	LockTimeout = 8

	// WriteTree means `git write-tree` failed against the per-invocation index
	// -- most often a full disk. Produced by commit, including --amend.
	WriteTree = 9

	// CommitTree means `git commit-tree` failed. Produced by commit, including
	// its --amend and reword forms.
	CommitTree = 10

	// PathMatchedNothing means a path or directory the caller named contributes
	// nothing to the commit: it is absent from disk and untracked in the tree
	// the commit is built on, it is a directory holding neither files on disk
	// nor paths in that tree, it is a file whose content the commit would not
	// change, or it is an --untrack target the commit's parent does not track,
	// which leaves no index entry to remove. Naming a path is a statement about
	// what the commit contains, so a path that cannot affect it is a refusal
	// rather than a silent omission. Produced by commit, including --amend.
	PathMatchedNothing = 11

	// BinaryHunkSpec means a hunk spec (--hunks file:1,3) was given for a file
	// git reports as binary, where only whole-file staging exists. Produced by
	// commit, including --amend.
	BinaryHunkSpec = 14

	// SymlinkHunkSpec means a hunk spec (--hunks link:1) named a symlink. A
	// symlink's whole content is the path it points at -- one line the
	// filesystem produces, with no hunks to choose between -- so the selection
	// could only ever select nothing. It is a separate code from BinaryHunkSpec
	// because the reason differs: a binary file HAS content git will not split,
	// while a symlink has nothing to split at all. Produced by commit,
	// including --amend.
	SymlinkHunkSpec = 15

	// CommitHookRejected means one of the repository's own git hooks refused the
	// commit: a pre-commit hook that exited nonzero against the staged content,
	// or a commit-msg hook that exited nonzero on the message. Both are one
	// situation -- the repository's own policy said no -- and the remedy is one
	// remedy: satisfy the hook, or take it out of .git/hooks. No commit, amend
	// or reword is created when it fires. Produced by commit, including its
	// --amend and reword forms. The post-commit hook cannot produce it: it runs
	// after the ref has moved and its exit status is ignored.
	CommitHookRejected = 16

	// ConclusionUnresolved means a conclusion command's declared resolutions do
	// not match the conflict actually in the index: a conflicted path no
	// --resolve or --resolve-file entry names, or an entry naming a path that is
	// not conflicted. Both halves are one situation -- the set of paths the
	// caller resolved is not the set of paths git left unmerged -- and the
	// refusal lists the paths on whichever side is wrong. Nothing is committed;
	// the operation is still in flight and the same command re-run with the
	// missing (or without the surplus) entries concludes it. Produced by
	// merge-continue, cherry-pick-continue and revert-continue -- and by
	// `safegit revert` of a single commit, which reaches the same conclusion
	// engine after computing the inverse patch, and therefore refuses on a
	// foreign unmerged entry the same way.
	ConclusionUnresolved = 17

	// ConclusionMarkerSurvived means the content a conclusion was about to
	// commit still holds a complete conflict region: the paths match the
	// conflict exactly (that is code 17's question), but one of them carries
	// the markers the resolution was supposed to remove. The refusal names each
	// path and line. Nothing is committed and the operation is still in flight,
	// so editing the file -- or resolving the path to a stage, whose content
	// cannot carry a survived region -- and re-running concludes it. A path
	// whose real content legitimately holds marker-shaped lines is declared
	// with the `safegit-conflict-markers` attribute, read from the first
	// parent's tree. Produced by merge-continue, cherry-pick-continue and
	// revert-continue -- and by `safegit revert` of a single commit, whose
	// staged result goes through the same verification before it is committed.
	ConclusionMarkerSurvived = 18

	// MoveNotBorneOut means a claim about a move is contradicted by the
	// repository.
	//
	// A declared move (--moved) reaches it when the old path is not tracked in
	// the tree the commit is built on, when the old path is still sitting on
	// disk, or when the new path is neither on disk nor in that tree. A
	// retraction (--moved-retract) reaches it when the id names no record in
	// the history the commit is built on, or names one that is already
	// retracted. Both are the same verdict: a record and a retraction are each
	// a claim every later reader resolves against the repository, so writing
	// one the repository already disagrees with would send those readers to a
	// path -- or to a record -- that was never there.
	// It is a separate code from PathMatchedNothing, which is about a named
	// path CONTRIBUTING nothing to the commit's content: a declaration stages
	// nothing and changes no content at all, and its failure is a claim the
	// world does not support rather than an argument that had no effect.
	// Nothing is committed when it fires. Produced by commit, including its
	// --amend and reword forms.
	//
	// `mv` produces it for the same class of verdict read the other way round:
	// a pair whose source is untracked or absent from disk, whose destination
	// is already occupied, or whose file/subtree spelling disagrees with what
	// the path actually is. The refusal names EVERY pair that is wrong, and
	// nothing has been moved or committed when it fires.
	//
	// Two declarations that contradict EACH OTHER -- nested sources, nested
	// destinations, one pair chaining into another -- exit Usage instead, which
	// is where every other argument-against-argument contradiction in the
	// commit family exits.
	MoveNotBorneOut = 19

	// PushHookFailed means a pre-pre-push hook exited nonzero, so no network
	// I/O was attempted. Produced by push and by `hook run`.
	PushHookFailed = 20

	// PushHookTimeout means a pre-pre-push hook exceeded
	// hooks.preprepush.timeoutSeconds and was killed. Produced by push and by
	// `hook run`.
	PushHookTimeout = 21

	// BackupDiverged means the remote backup slot holds commits the local
	// history does not contain, so backing up would discard them. Produced by
	// `backup backup`.
	BackupDiverged = 22

	// BackupNoSlot means the current branch has no backup slot on the remote.
	// Produced by `backup restore`.
	BackupNoSlot = 23

	// HooksNotMigrated means hook discovery found hooks still sitting in the
	// pre-migration location -- the `pre-pre-push` file or the
	// `pre-pre-push.d/` directory inside git's own .git/hooks -- after safegit
	// moved its live hook store to the tool-owned .git/safegit/hooks. Running
	// them from there would make the store safegit executes from depend on
	// where a file happened to be left, and skipping them would stop an
	// operator's checks in silence, so discovery refuses. The remedy is one
	// command: `safegit hook migrate`. Produced by push, `hook run`,
	// `hook list` and `hook remove` -- remove reaches it when the named hook
	// exists only in the legacy location, where it is not this command's to
	// delete until migration has moved it.
	HooksNotMigrated = 24

	// HookNotExecutable means a discovered pre-pre-push hook is not executable,
	// in EITHER store: the tool-owned live one under .git/safegit/hooks, or the
	// one the CHECKOUT provides in .safegit/hooks. A hook is disabled by
	// REMOVING it -- deleting the file, and committing that deletion for the
	// checkout-provided store -- never by dropping its mode, so a lost
	// executable bit is treated as the accident it almost always is rather than
	// as an intentional disabling that would stop the checks in silence.
	// (Membership of either store is the directory itself, not git's tracking:
	// an uncommitted file there runs too.) The remedy is `chmod +x`, plus a
	// commit of the mode change where the store is the checkout's. Discovery
	// answers for the whole set, so the healthy hooks beside the offender do not
	// run either. Produced by push and `hook run`.
	HookNotExecutable = 25

	// CommitStands is the family code for every outcome in which safegit's own
	// commit is REAL -- the ref moved, the object is the branch's tip, `safegit
	// undo` can reverse it -- and a step that runs after the ref update did not
	// finish.
	//
	// It exists because the two halves of such a run answer opposite questions,
	// and a single General (1) answers neither. "Did the operation happen?" is
	// yes; "did everything it owes finish?" is no. A caller that reads 1 has to
	// guess which, and the guess that costs the most is the one a script makes by
	// default -- retrying an operation that already succeeded.
	//
	// The members of the family are the steps that can only run once a commit
	// exists: reconciling the shared index with the new tip, removing the
	// concluded operation's state files, writing the declared resolutions into
	// the working tree, bumping a parent repository's gitlink, and putting back
	// the autostash git set aside before a merge. Every one of them leaves the
	// commit standing, and every one of them names in its own message what was
	// left behind.
	//
	// The whole family emits its report: under --json the envelope is emitted
	// with the payload the run would have carried, so a machine consumer is never
	// told nothing about a ref that moved. Produced by commit (including its
	// --amend and reword forms), mv, undo, the three conclusion commands --
	// merge-continue, cherry-pick-continue and revert-continue -- and the
	// commands that conclude an operation they started themselves: merge, pull,
	// cherry-pick and revert.
	CommitStands = 26

	// ConclusionWouldOverwrite means a conclusion's working-tree write would
	// destroy content on disk that no side of the conflict accounts for.
	//
	// Resolving a path to a stage REPLACES the file on disk (git's own `checkout
	// --ours`) and resolving it to `delete` removes it, so a file an operator
	// hand-edited between the conflict and the conclusion would be overwritten
	// with content they never chose. safegit refuses instead: the accepted set
	// for a path is the three index stages plus the blob git itself emitted into
	// the working tree, and a file matching none of them is a hand edit.
	//
	// Nothing is committed and the operation is still in flight, so the edit can
	// be inspected, kept (by resolving that path to `worktree`) or thrown away.
	// `--discard-unmatched-worktree` is the election that destroys it anyway.
	// Produced by merge-continue, cherry-pick-continue and revert-continue.
	ConclusionWouldOverwrite = 27

	// UnmergedIndex means the repository's shared index carries an unmerged
	// entry, so the commit safegit was asked to make would be built beside a
	// conflict nobody resolved. git refuses every commit in that state and so
	// does safegit, naming the paths and `safegit doctor --action fix`, which
	// re-stages the working tree's own content when no operation is in flight.
	//
	// The conclusion commands are exempt by construction: an unmerged index is
	// the state they exist to conclude, and their declared resolutions are what
	// resolve it. Produced by commit (including its --amend and reword forms)
	// and by mv, which reaches the same pipeline.
	UnmergedIndex = 28

	// EscapingSymlinkTarget means a commit named a symlink whose target leaves
	// the repository, and the caller did not elect to record it.
	//
	// git records a symlink as the link TEXT and nothing else, so a link
	// pointing outside the repository is an object that resolves to nothing in
	// anyone else's checkout -- and, where it resolves at all, resolves to a
	// file the repository never carried. safegit refuses it rather than
	// recording a reference to a place only this machine has, and the refusal
	// names the literal target so the operator can see what the link says.
	// `--allow-escaping-targets` is the election that commits it anyway, and
	// restores the one-line notice the refusal replaced.
	//
	// The scope is ADDING or STAGING escaping link content: the refusal is made
	// at intake, before anything is staged, so commit and its --amend form both
	// inherit it and nothing is written when it fires. `safegit mv` moving an
	// existing tracked escaping link is not covered -- a move-only commit
	// carries the blob its parent held across and restages no link content at
	// all. Produced by commit, including its --amend form.
	EscapingSymlinkTarget = 29

	// RewriteRefused means a history rewrite was refused by the verification
	// that runs BEFORE any ref moves: the rewritten commits existed only as
	// unreachable objects, and the check found the rewrite did not do what the
	// operation declared it would (a commit changed a path no operation asked
	// to change, a declared change is missing, the scrubbed content survived in
	// the rewritten trees, the named file appears in no commit at all), or the
	// working tree acquired foreign state while the rewrite was running. In
	// every case NOTHING moved: no ref, no tag, no rewrite-journal record, and
	// the original history is exactly as it was. Produced by scrub file, scrub
	// match, scrub run and author rewrite.
	RewriteRefused = 30

	// RewriteIncomplete means the rewrite itself STANDS -- refs moved, the
	// journal is complete, the new history is the repository's history -- but
	// something after it did not finish cleanly: an old object survived the
	// prune, the scrubbed pattern is still reachable somewhere the rewrite does
	// not cover (a stash, a note), a ref still points at a pre-rewrite SHA, or
	// the working-tree sync was skipped because foreign staged state appeared
	// while the rewrite ran. It is a separate code from RewriteRefused because
	// the two ask for opposite things: RewriteRefused says nothing happened and
	// the command can be re-run, this says the rewrite happened and the named
	// residue is what still needs attention. Produced by scrub file, scrub
	// match, scrub run and author rewrite.
	RewriteIncomplete = 31

	// PushFailed means the push did not get through. It covers `git push`
	// itself failing after safegit's retry policy was exhausted, and the three
	// ways the window around a push can defeat it:
	//
	//   - the remote could not be OBSERVED, before the first attempt or when
	//     re-reading it before a retry. safegit pins --force-with-lease to the
	//     SHA it observed, so an unreadable remote is not an answer it may
	//     substitute a guess for;
	//   - the re-read found no refs to push at all;
	//   - the re-read found a LOCAL ref at a SHA the pre-pre-push hooks never
	//     saw. safegit refuses rather than publish un-validated content, and it
	//     does not re-run the hooks mid-retry.
	//
	// Produced by push and by `backup backup`.
	PushFailed = 40

	// PushLeaseRejected means git refused the push because a --force-with-lease
	// expectation did not match: between safegit observing the remote ref and
	// the push reaching it, somebody else moved it. The lease did its job -- the
	// other session's commits are still there -- so this is a verdict about the
	// world, not a failure to retry: it is terminal, and the remedy is to fetch,
	// look at what arrived, and decide again. It is a separate code from
	// PushFailed because the two ask for different things: PushFailed says the
	// push did not get through, this says it got through and was refused.
	//
	// Produced by push and by `backup backup`, the two commands that pin leases
	// from observations they took themselves. For a backup it means another
	// machine wrote the same branch's slot inside that window. It is distinct
	// from BackupDiverged, which is the ANCESTRY refusal: that one is decided
	// from a slot safegit read and found to hold unfamiliar commits, and nothing
	// is pushed at all.
	PushLeaseRejected = 41

	// DoctorFindings means `doctor` ran its checks and at least one
	// ERROR-severity check failed: safegit cannot work correctly in this
	// repository until the named finding is dealt with. Warn-severity findings
	// are advisory and never reach this code -- a repository whose only
	// findings are warnings exits 0. Under `--action fix` the code reflects
	// what the fix LEFT behind: a finding the fix repaired does not produce it,
	// one it could not repair does. Produced by doctor.
	DoctorFindings = 50

	// Internal marks an invariant safegit believes cannot be violated -- a
	// switch over a closed set of framework-validated choices reaching its
	// default arm. Produced by push. Seeing it is a bug report.
	Internal = 70
)

// Entry is one registered code, as the generated documentation renders it.
type Entry struct {
	// Code is the numeric status the process exits with.
	Code int
	// Const is the constant's name in this package. exitcode_test.go checks it
	// against the package's own source, so a constant cannot be added without a
	// row here.
	Const string
	// Meaning is the one-line description the documentation table shows.
	Meaning string
}

// All returns every registered code in ascending numeric order. It is the
// authority the documentation table is generated from.
func All() []Entry {
	return []Entry{
		{OK, "OK", "Success"},
		{General, "General", "General error"},
		{Usage, "Usage", "Argument error safegit itself rejected (the framework's own parse refusals exit 1)"},
		{NoRepository, "NoRepository", "Not a git repository"},
		{NotInitialized, "NotInitialized", "safegit not initialized"},
		{CoordinationBusy, "CoordinationBusy", "Coordination guard refused (another operation owns the working tree)"},
		{CASExhausted, "CASExhausted", "CAS retries exhausted"},
		{LockTimeout, "LockTimeout", "Timed out acquiring a lock a live holder still owns"},
		{WriteTree, "WriteTree", "write-tree failed"},
		{CommitTree, "CommitTree", "commit-tree failed"},
		{PathMatchedNothing, "PathMatchedNothing", "A named path or directory contributes nothing to the commit"},
		{BinaryHunkSpec, "BinaryHunkSpec", "Hunk spec given for a binary file"},
		{SymlinkHunkSpec, "SymlinkHunkSpec", "Hunk spec given for a symlink, which has no hunks to select"},
		{CommitHookRejected, "CommitHookRejected", "A pre-commit or commit-msg hook refused the commit"},
		{ConclusionUnresolved, "ConclusionUnresolved", "A conclusion's declared resolutions do not match the conflicted paths in the index"},
		{ConclusionMarkerSurvived, "ConclusionMarkerSurvived", "A conclusion's content still holds a complete conflict region"},
		{MoveNotBorneOut, "MoveNotBorneOut", "A claim about a move (--moved, --moved-retract, a `mv` pair) is contradicted by the repository"},
		{PushHookFailed, "PushHookFailed", "Pre-pre-push hook failed"},
		{PushHookTimeout, "PushHookTimeout", "Pre-pre-push hook timed out"},
		{BackupDiverged, "BackupDiverged", "The remote backup slot holds work missing from the local history"},
		{BackupNoSlot, "BackupNoSlot", "The branch has no backup slot on the remote"},
		{HooksNotMigrated, "HooksNotMigrated", "Hooks are still in the pre-migration .git/hooks location (run `safegit hook migrate`)"},
		{HookNotExecutable, "HookNotExecutable", "A discovered hook is not executable, in either store"},
		{CommitStands, "CommitStands", "The commit was created and the ref moved, but a step after the ref update did not finish"},
		{ConclusionWouldOverwrite, "ConclusionWouldOverwrite", "A conclusion's working-tree write would destroy a hand edit no side of the conflict accounts for"},
		{UnmergedIndex, "UnmergedIndex", "The shared index carries an unmerged entry, so no commit can be built beside it"},
		{EscapingSymlinkTarget, "EscapingSymlinkTarget", "A named symlink's target leaves the repository (`--allow-escaping-targets` records it anyway)"},
		{RewriteRefused, "RewriteRefused", "A history rewrite was refused before any ref moved (nothing changed)"},
		{RewriteIncomplete, "RewriteIncomplete", "A history rewrite stands, but post-rewrite verification found residue or skipped the working-tree sync"},
		{PushFailed, "PushFailed", "The push did not get through: git push failed, or the refs could not be safely re-read around it"},
		{PushLeaseRejected, "PushLeaseRejected", "The remote ref moved after safegit observed it, so the --force-with-lease expectation no longer matched"},
		{DoctorFindings, "DoctorFindings", "doctor found at least one error-severity problem (warnings alone exit 0)"},
		{Internal, "Internal", "Internal invariant violated (a bug)"},
	}
}

// Defined reports whether code is registered here.
func Defined(code int) bool {
	for _, e := range All() {
		if e.Code == code {
			return true
		}
	}
	return false
}

// MarkdownTable renders the registry as the two-column table the documentation
// carries. Both the generator and the test that checks the documentation for
// staleness call this, so there is one rendering and one authority.
func MarkdownTable() string {
	var b strings.Builder
	b.WriteString("| Code | Meaning |\n|------|---------|\n")
	for _, e := range All() {
		fmt.Fprintf(&b, "| %d | %s |\n", e.Code, e.Meaning)
	}
	return b.String()
}
