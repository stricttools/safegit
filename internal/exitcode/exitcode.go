// Package exitcode is safegit's single registry of process exit codes.
//
// Every numeric exit code safegit produces is a named constant here, and every
// exit site in the tool -- die(), os.Exit(), a handler's int return, a
// commit.CommitError's Code field -- names one of these constants rather than a
// bare literal. `scripts/exit-inventory` enumerates those sites mechanically
// from the AST; it is how the registry was derived and how a later reviewer
// re-derives it.
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
	// read, a ref that would not resolve. Produced by every command.
	General = 1

	// Usage is a command line safegit itself rejects after strictcli has
	// accepted it: mutually exclusive flags, a missing message, a hunk spec
	// that does not parse, an empty file list. Produced by commit, scrub
	// file/match/run, author check, and the guarded passthroughs. Note that a
	// refusal by the framework's own parser exits General (1) instead -- see
	// the package comment.
	Usage = 2

	// NoRepository means the working directory is not inside a git repository
	// (or git is not installed). Produced by every command, at the point where
	// it resolves the git directory.
	NoRepository = 3

	// NotInitialized means safegit's own state directory could not be created
	// or read. Produced by every command that needs .git/safegit: commit,
	// undo, unlock, config, scan, hook, backup, the guarded passthroughs, and
	// the four rewrite commands.
	NotInitialized = 4

	// CoordinationBusy means the coordination guard refused: another safegit
	// operation, or an in-progress git sequencer state, owns the working tree.
	// Produced by checkout, pull, merge, rebase, reset, bisect, cherry-pick,
	// revert and backup restore.
	CoordinationBusy = 5

	// CASExhausted means the ref moved under every compare-and-swap attempt,
	// so the commit could not converge. Produced by commit, including its
	// --amend and reword forms.
	CASExhausted = 7

	// LockTimeout means a safegit lock could not be acquired within
	// lock.acquireTimeoutSeconds because a live holder still owns it. Produced
	// by scrub file, scrub match, scrub run and author rewrite (all four
	// contend on the single repo-wide rewrite lock) and by undo (which contends
	// on the ref lock). The commit pipeline's own ref-lock timeout is reported
	// as General (1): it is wrapped into the commit error path, and this
	// subphase did not change which code any commit path produces.
	LockTimeout = 8

	// WriteTree means `git write-tree` failed against the per-invocation index
	// -- most often a full disk. Produced by commit, including --amend.
	WriteTree = 9

	// CommitTree means `git commit-tree` failed. Produced by commit, including
	// its --amend and reword forms.
	CommitTree = 10

	// BinaryHunkSpec means a hunk spec (file:1,3) was given for a file git
	// reports as binary, where only whole-file staging exists. Produced by
	// commit, including --amend.
	BinaryHunkSpec = 14

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

	// PushFailed means `git push` itself failed after safegit's retry policy
	// was exhausted. Produced by push and by `backup backup`.
	PushFailed = 40

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
		{BinaryHunkSpec, "BinaryHunkSpec", "Hunk spec given for a binary file"},
		{PushHookFailed, "PushHookFailed", "Pre-pre-push hook failed"},
		{PushHookTimeout, "PushHookTimeout", "Pre-pre-push hook timed out"},
		{BackupDiverged, "BackupDiverged", "The remote backup slot holds work missing from the local history"},
		{BackupNoSlot, "BackupNoSlot", "The branch has no backup slot on the remote"},
		{PushFailed, "PushFailed", "Git push failed"},
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
