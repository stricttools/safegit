// Package exitcode is safegit's single registry of process exit codes.
//
// Every numeric exit code safegit produces is a named constant here, and every
// exit site in the tool -- die(), os.Exit(), a handler's int return, a
// commit.CommitError's Code field -- names one of these constants rather than a
// bare literal. `scripts/exit-inventory` enumerates those sites mechanically
// from the AST; it is how the registry was derived and how a later reviewer
// re-derives it.
//
// The one carve-out, and it is deliberate: the guarded passthroughs --
// checkout, pull, merge, rebase, reset, bisect, cherry-pick and revert -- exit
// with the wrapped git command's OWN exit code once git has run. Those codes
// are git's (1 for a conflicted merge, 128 or 129 for a fatal error), they are
// foreign to safegit, and they are deliberately NOT registered here: safegit
// reports git's verdict verbatim rather than translating it, and a registry row
// would claim ownership of a number safegit does not choose. A code from one of
// those commands is safegit's own only when the failure happened before git ran
// -- the coordination guard, an uninitialized repository, a rejected argument.
// docs/commands-guide.md states the same split above the generated table.
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
	// malformed glob or --target value. Produced by commit, undo, scan, scrub
	// file/match/run, author check, and the three guarded passthroughs that
	// take a bare positional argument (checkout, merge, rebase). Note that a
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
	// Produced by checkout, pull, merge, rebase, reset, bisect, cherry-pick,
	// revert and backup restore, and by commit (including its --amend and
	// reword forms) and undo, which refuse outright while git has a merge,
	// cherry-pick, revert, rebase or mailbox application in flight -- naming
	// the operation and the command that ends it.
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
	//   - the worktree operation lock: checkout, pull, merge, rebase, reset,
	//     bisect, cherry-pick, revert, commit, commit --amend, reword, undo;
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

	// PushFailed means `git push` itself failed after safegit's retry policy
	// was exhausted. Produced by push and by `backup backup`.
	PushFailed = 40

	// PushLeaseRejected means git refused the push because a --force-with-lease
	// expectation did not match: between safegit observing the remote ref and
	// the push reaching it, somebody else moved it. The lease did its job -- the
	// other session's commits are still there -- so this is a verdict about the
	// world, not a failure to retry: it is terminal, and the remedy is to fetch,
	// look at what arrived, and decide again. It is a separate code from
	// PushFailed because the two ask for different things: PushFailed says the
	// push did not get through, this says it got through and was refused.
	// Produced by push, which is the only command that pins leases from
	// observations it took itself (`backup backup` pins one too, but its slot is
	// tool-owned and its refusal is BackupDiverged).
	PushLeaseRejected = 41

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
		{PushHookFailed, "PushHookFailed", "Pre-pre-push hook failed"},
		{PushHookTimeout, "PushHookTimeout", "Pre-pre-push hook timed out"},
		{BackupDiverged, "BackupDiverged", "The remote backup slot holds work missing from the local history"},
		{BackupNoSlot, "BackupNoSlot", "The branch has no backup slot on the remote"},
		{RewriteRefused, "RewriteRefused", "A history rewrite was refused before any ref moved (nothing changed)"},
		{RewriteIncomplete, "RewriteIncomplete", "A history rewrite stands, but post-rewrite verification found residue or skipped the working-tree sync"},
		{PushFailed, "PushFailed", "Git push failed"},
		{PushLeaseRejected, "PushLeaseRejected", "The remote ref moved after safegit observed it, so the --force-with-lease expectation no longer matched"},
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
