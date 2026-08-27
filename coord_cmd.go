package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// runGitMutation runs a tree- or ref-mutating git command through the effects
// handle, streaming git's own output straight to the terminal, and returns
// git's own exit code. Routing it here is what makes --dry-run honest for these
// commands: the invocation is recorded in the would-do log and git is never
// started.
// The argv is built by internal/gitexec, safegit's single git-execution
// boundary, so it carries the same --no-optional-locks prefix every other git
// invocation does and its subcommand is checked against the one classification
// table. The invocation is exempt from the repository-root pin: these are the
// operator's own arguments, and git must read any pathspec in them in the
// directory the operator typed it in.
//
// Check(false) is what makes the propagation possible: the framework's checked
// form turns a nonzero child into an error string and hands back an unsettled
// Completed, so git's own code is unreachable there. Unchecked, the code rides
// the Completed and safegit passes it on unchanged. A nonzero return is
// therefore git's verdict, not safegit's; exitcode.General is reserved for the
// two failures that happen before git runs (argv construction, effects-handle
// refusal), and those print their reason because no child ever spoke.
//
// door is this call site's declared permission to let git AUTHOR a commit, and
// it is stated at every call rather than defaulted: the boundary refuses an
// authoring argv that carries no door, so a caller has to have decided. Every
// caller but the rebase passthrough passes gitexec.NoDoor.
func runGitMutation(flags globalFlags, door gitexec.DoorID, args ...string) int {
	argv, err := gitexec.ArgvAny(gitexec.ExemptGitMutation, door, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	// Streaming is HUMAN mode only. Stream(true) wires the child's stdout to
	// safegit's own, which is exactly right at a terminal and exactly wrong in
	// machine mode: stdout there carries one document, the framework's envelope,
	// and git's narration written in front of it ("Auto-merging ...", "CONFLICT
	// ...", "Fast-forward") makes the stream unparseable. Under --json the child
	// is CAPTURED instead and re-emitted afterwards, its stdout joining its
	// stderr on stderr -- the same routing `push` already uses, and nothing is
	// discarded. Check(false) behaves identically either way, so git's own exit
	// code still rides the Completed.
	//
	// The cost, stated because it is real: machine mode loses LIVE output. A long
	// rebase says nothing until it finishes. The framework offers no tee, and a
	// second copy written by safegit would duplicate every line at a terminal.
	opts := []strictcli.EffectOption{strictcli.Check(false)}
	if !flags.json {
		opts = append(opts, strictcli.Stream(true))
	}
	done, err := flags.effects().Run(argv, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if flags.dryRun {
		// The invocation was recorded instead of performed: no child process
		// ran, so the Completed is unsettled and reading its exit code would
		// panic.
		//
		// This keys off the flag rather than off the carrier because strictcli
		// exposes no settled-ness: Completed's `settled` field is unexported
		// and its only public methods (ExitCode, Stdout, Stderr) panic instead
		// of reporting it, and Effects.Recorded() cannot serve as a probe
		// because calling it claims the would-do render.
		//
		// What makes the flag a correct stand-in is that no argv built here can
		// reach Run's OBSERVE branch, which executes the child even in dry mode
		// and returns a settled Completed. safegit's proc-observe allowlist is
		// generated from the classification table's read view and admits only
		// verbs the table declares observe-only UNCONDITIONALLY; every verb this
		// function builds an argv for (switch, fetch, merge, rebase, reset,
		// bisect, cherry-pick, revert) either declares mutating effects in its
		// base or carries conditional ones, and a verb with any conditional
		// effect is excluded from the allowlist however read-only its base is.
		// The two sets are disjoint by construction, and
		// TestObserveAllowlistCannotAdmitAMutation checks the prefixes against
		// the argv this function actually builds rather than trusting the
		// reasoning.
		return 0
	}
	if flags.json {
		// Captured rather than streamed, so it has to be put back. passthroughStdout
		// answers stderr in machine mode, which is where both of the child's streams
		// go: the envelope owns stdout.
		if out := done.Stdout(); out != "" {
			fmt.Fprint(passthroughStdout(flags), out)
		}
		if errText := done.Stderr(); errText != "" {
			fmt.Fprint(os.Stderr, errText)
		}
	}
	return done.ExitCode()
}

// coordGuard runs coord.Check and prints a refusal if dirty. It returns
// exitcode.CoordinationBusy when another operation owns the working tree,
// exitcode.OK when it is clean, and exitcode.General when the check itself
// could not be made.
//
// gitDir, not the safegit dir: the check reads git's own in-flight operation
// state so that a refusal issued mid-merge or mid-rebase names that operation
// and the command that ends it.
func coordGuard(flags globalFlags, gitDir, operation string) int {
	ctx := flags.ctx()
	dirty, err := coord.Check(ctx, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if dirty != nil {
		fmt.Fprint(os.Stderr, dirty.Refuse(operation))
		return exitcode.CoordinationBusy
	}
	return 0
}

// refuseComputeOverInFlight is the entry check every command that COMPUTES an
// operation owes before it computes anything.
//
// Which commands those are: the restructured `merge`, `cherry-pick` and
// `revert`; `pull`, whose merge step is merge's but which reaches it through its
// own handler and so makes the check itself, before its fetch; and the
// `--no-commit` forms of cherry-pick and revert, which are forwarded to git yet
// ask it for exactly the same computation (see runComputingPassthrough).
// `safegit rebase` makes a REFUSAL of its own rather than this one -- it is not
// a compute door, and its predicate has to be kind-scoped so a rebase's own
// --continue is not refused; see refuseRebaseOverAnotherOperation.
//
// Raw git refuses to start one operation over another: `git cherry-pick <c>`
// during a parked revert exits 128 with "a revert is already in progress". The
// restructure does not go through that door. It computes with
// `git <verb> --no-commit`, which git does NOT guard the same way, so a
// restructured command ran its compute over somebody else's parked operation
// and then treated the result as its own -- a pick over a parked revert
// committed and orphaned REVERT_HEAD, and a revert over a parked pick staged an
// inverse patch behind a diagnosis that was not true of the state it found.
//
// So the check is made structurally rather than inherited from git: the same
// coord.GuardInFlight every other author already calls, rendering the same
// refusal `safegit commit` produces in exactly this state -- what is in flight,
// and the way out of it from the single way-out authority.
//
// The dirty-tree guard does NOT subsume it. A parked operation whose result
// equals the current tree -- a pick of an already-applied commit, a revert of an
// already-reverted one -- leaves nothing for `git diff HEAD` to report, so the
// repository looks idle to every check that only asks whether the tree is
// clean. It is placed AFTER coordGuard for the ordinary case, where the tree is
// dirty and the refusal should carry the listing of what is in it, and BEFORE
// the dry-run branch, because a preview of a command that cannot run is not a
// preview of anything (and the preview path computes the operation with git's
// own merge engine, over the very state being refused over).
//
// It is scoped to the COMPUTE forms alone. The state-control verbs
// (--abort/--quit) and the three -continue commands are the way out of the
// state, and a check that refused them would strand the repository in the state
// it was protecting.
func refuseComputeOverInFlight(gitDir, operation string) int {
	if err := coord.GuardInFlight(gitDir, operation, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.CoordinationBusy
	}
	return 0
}

// refuseRebaseOverAnotherOperation refuses a rebase asked for while git holds
// SOMEBODY ELSE'S operation in flight.
//
// A rebase is not a compute door -- git replays and authors, and that is the one
// declared exception to single authorship -- but it runs over whatever state it
// finds. Over a parked revert on a clean working tree it exits 0 and STRANDS the
// revert's state files behind it, so every later `safegit commit` refuses over a
// revert nobody is running. The dirty-tree guard cannot stand in for this: a
// parked operation whose result equals the current tree leaves nothing for
// `git diff HEAD` to report.
//
// THE PREDICATE IS KIND-SCOPED, and that is the whole design. It refuses an
// in-flight state whose kind is not the REBASE kind. The state-control forms --
// `rebase --continue`, `--abort`, `--skip` -- therefore pass by construction,
// because mid-rebase state reports the rebase kind; there is no second,
// argv-based exemption list to keep in step with this one.
//
// It is deliberately NOT coord.GuardInFlight with a declared rebase context:
// that path hard-errors when a context is declared and nothing is in flight, so
// it would refuse every clean-repository rebase. The refusal text is still
// coord's own (coord.RefuseInFlight), so it cannot name a different way out from
// the one the sibling refusals name.
func refuseRebaseOverAnotherOperation(gitDir string) int {
	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: refusing rebase: cannot read git's in-flight operation state: %v\n", err)
		return exitcode.CoordinationBusy
	}
	if !state.InProgress() || state.Kind == sequencer.KindRebase {
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", coord.RefuseInFlight("rebase", state))
	return exitcode.CoordinationBusy
}

// announceWayOut prints safegit's own next step when a passthrough failed and
// left git with an operation in flight.
//
// git's own failure text ends at "fix conflicts and then commit the result",
// and that instruction has no safegit-conformant execution: `safegit commit` is
// pathspec-only and refuses mid-merge, and `git commit` is unavailable to an
// agent under the git-add/git-commit blocking hooks. So safegit owes the
// operator the command that DOES conclude the operation, rendered from
// coord.WayOutOf -- the single authority every in-flight refusal already reads,
// so no two messages can name different commands for the same state.
//
// It prints on stderr, unconditionally: this is the tail of a failure, and an
// operator who asked for --quiet asked for less noise on the happy path, not
// for the way out to be withheld.
func announceWayOut(flags globalFlags, gitDir string) {
	if flags.dryRun {
		// Nothing ran, so nothing is in flight that this invocation put there.
		return
	}
	state, err := sequencer.Read(gitDir)
	if err != nil || !state.InProgress() {
		return
	}
	w := coord.WayOutOf(state)
	if w.Conclude == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "\nsafegit: %s is in progress.\n", state.String())
	fmt.Fprintf(os.Stderr, "  conclude it:  %s\n", w.Conclude)
	if w.Abandon != "" {
		fmt.Fprintf(os.Stderr, "  abandon it:   %s\n", w.Abandon)
	}
}

// The oplog baseline every operation records.
//
// A commit entry has carried the three facts that make an audit trail readable
// since the pipeline was written -- the full ref name, the tip it moved from and
// the tip it moved to (internal/commit's Step 8, spelled `ref`/`parent`/`sha`).
// The guarded commands recorded none of them: a merge logged the branch NAME the
// operator typed and the result, a rebase logged the upstream, and reset, bisect
// and every guarded passthrough logged a verbatim copy of the argv. Nothing said
// where a branch stood before the operation, so undo arithmetic and bypass
// detection had nothing to work from, and a passthrough git REFUSED logged an
// entry indistinguishable from one git performed.
//
// Two shapes, and the difference between them is which thing moved:
//
//   - a REF-MOVING operation (merge, pull, rebase, reset, cherry-pick, revert)
//     records the baseline in the commit-entry spelling, so the fail-closed
//     readers -- oplog.LastRefUpdate, doctor's bypass check, undo's per-ref
//     filter -- see the position safegit left the branch at;
//   - a HEAD-MOVING operation (switch, bisect) records the same facts under
//     `observed_*` names those readers do not consume. See navigationExtra.
const (
	oplogOutcomeOK     = "ok"
	oplogOutcomeFailed = "failed"
)

// oplogPosition is where a branch stood before an operation started. It is read
// BEFORE any git runs, because that is the only moment the answer exists.
type oplogPosition struct {
	// ref is the full ref name HEAD pointed at, empty when HEAD is detached.
	ref string
	// oldTip is the commit HEAD resolved to, empty on an unborn branch.
	oldTip string
}

// readOplogPosition resolves the ref and tip an operation is about to move.
//
// Neither answer is guessed at: a detached HEAD has no ref name and an unborn
// branch has no tip, and both are recorded as the empty string they are. An
// entry naming no ref matches no ref, which is the correct behavior for every
// reader.
func readOplogPosition(flags globalFlags) oplogPosition {
	ctx := flags.ctx()
	ref, _ := git.HeadRef(ctx)
	oldTip, _ := git.RevParse(ctx, "HEAD")
	return oplogPosition{ref: ref, oldTip: oldTip}
}

// appendOperationEntry records what a REF-MOVING operation did to the branch.
//
// A failed operation records an EMPTY new tip, and that is the mechanism rather
// than a formality: oplog.LastRefUpdate takes the newest entry for a ref that
// carries a new tip, so an entry with none is passed over and the position
// safegit really last left the branch at is still the one bypass detection
// compares against. The outcome field says the same thing in words, so an
// operator reading the log does not have to infer a refusal from an absence.
//
// `more` is merged LAST, so an operation with more than two outcomes states its
// own: a merge records `fast-forward`, `parked` or `up-to-date` there, each of
// which is a different thing from the bare ok this function would otherwise
// write. Nothing else in an entry may be restated that way.
func appendOperationEntry(flags globalFlags, sgDir, op string, pos oplogPosition, ok bool, more map[string]interface{}) {
	if flags.dryRun {
		// A preview writes nothing, the oplog included. The failure sites reach
		// this before their handler's own dry-run return, because a refusal that
		// happens BEFORE git runs (argv construction, an effects-handle refusal)
		// carries a nonzero code even in a dry run.
		return
	}
	extra := map[string]interface{}{
		"ref":     pos.ref,
		"parent":  pos.oldTip,
		"sha":     "",
		"outcome": oplogOutcomeFailed,
	}
	if ok {
		newTip, _ := git.RevParse(flags.ctx(), "HEAD")
		extra["sha"] = newTip
		extra["outcome"] = oplogOutcomeOK
	}
	for k, v := range more {
		extra[k] = v
	}
	_ = oplog.Append(sgDir, oplog.Entry{Op: op, Extra: extra})
}

// navigationExtra builds the entry for an operation that moves HEAD and no
// branch ref: a branch switch, a bisect step.
//
// The positions ride under `observed_*` rather than the commit-entry names, and
// the reason is the fail-closed readers. oplog.LastRefUpdate reads a new tip
// from `sha`/`to`/`result` and treats the newest such entry for a ref as the
// position safegit last LEFT that ref at. A branch switch left no position: it
// moved HEAD, and the branch is exactly where whatever moved it last put it. An
// entry claiming otherwise would RESET the bypass-detection baseline and mask an
// out-of-band commit made before the switch. A bisect step is worse still -- it
// parks HEAD on some unrelated commit -- and would make the check report a
// divergence that is nothing of the kind.
//
// `observed_` is the honest word for what these are: an observation of where
// HEAD was and where it ended up, not a record of safegit writing a ref.
func appendNavigationEntry(flags globalFlags, sgDir, op, ref, oldTip, newTip string, ok bool, more map[string]interface{}) {
	if flags.dryRun {
		return
	}
	_ = oplog.Append(sgDir, oplog.Entry{Op: op, Extra: navigationExtra(ref, oldTip, newTip, ok, more)})
}

func navigationExtra(ref, oldTip, newTip string, ok bool, more map[string]interface{}) map[string]interface{} {
	extra := map[string]interface{}{
		"ref":             ref,
		"observed_parent": oldTip,
		"observed_tip":    newTip,
		"outcome":         oplogOutcomeFailed,
	}
	if ok {
		extra["outcome"] = oplogOutcomeOK
	}
	for k, v := range more {
		extra[k] = v
	}
	return extra
}

func runRebase(flags globalFlags, args []string) int {
	// FIRST, and before the repository is touched at all: a command line safegit
	// itself refuses is refused without a lock and without a git call.
	parsed := parseGitArgs("rebase", args)
	if code := refuseUnsupportedRebase(parsed); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	release, code := acquireOperationLock(flags, gitDir, "rebase")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "rebase"); code != 0 {
		return code
	}

	// Inside the operation lock and immediately after the coordination guard, so
	// no passthrough in this worktree can start an operation between the read and
	// the work -- the same rule every other in-flight check here follows. It is
	// BEFORE readOplogPosition on purpose: a refusal that happens before git runs
	// writes no oplog entry, like every other pre-git refusal, and a --dry-run
	// rebase refuses here too rather than previewing a run that cannot happen.
	if code := refuseRebaseOverAnotherOperation(gitDir); code != 0 {
		return code
	}

	// A SECOND refusal, beside that one rather than folded into it: the check
	// above is about somebody else's in-flight state, this one is about where
	// this branch stands. An unborn branch has no commits to replay, and git's
	// own answer ("Could not resolve HEAD to a commit") names the ref rather
	// than the reason. Like every other pre-git refusal here it writes no oplog
	// entry, which is why it comes before readOplogPosition.
	if code := refuseUnbornRebase(flags.ctx(), rebaseUpstream(parsed)); code != 0 {
		return code
	}

	pos := readOplogPosition(flags)
	// The upstream as the operator NAMED it, read off the parsed command line
	// rather than off argv[0], which for `--onto <base> <upstream>` and for the
	// state-control forms is an option rather than a revision.
	where := map[string]interface{}{"upstream": rebaseUpstream(parsed)}
	// THE one declared door through the single-authorship boundary: a rebase is
	// git's replay, and every commit it produces is git's. Nothing else in
	// safegit names a door, and the boundary refuses an authoring argv that does
	// not.
	if code := runGitMutation(flags, gitexec.DoorRebasePassthrough, append([]string{"rebase"}, args...)...); code != 0 {
		appendOperationEntry(flags, sgDir, "rebase", pos, false, where)
		return code
	}
	if flags.dryRun {
		return 0
	}

	appendOperationEntry(flags, sgDir, "rebase", pos, true, where)
	return 0
}

func runReset(flags globalFlags, args []string) int {
	if code := refuseUnsupportedReset(flags, parseGitArgs("reset", args)); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// Unconditionally, unlike the dirty-tree guard below: every reset moves
	// HEAD, so every reset has to be serialized against the other operations in
	// this worktree. The guard below is the narrower question -- which forms
	// write to the working tree -- and the classification table answers it.
	release, code := acquireOperationLock(flags, gitDir, "reset")
	if code != 0 {
		return code
	}
	defer release()

	// Which reset forms may be refused over uncommitted work is DERIVED from the
	// classification table rather than re-scanned here. The scan this replaced
	// looked for --hard alone, on a comment claiming it is the only mode that
	// mutates the working tree; --merge and --keep write working-tree files too
	// and ran unguarded.
	if gitexec.WritesWorktree(append([]string{"reset"}, args...)) {
		if code := coordGuard(flags, gitDir, "reset"); code != 0 {
			return code
		}
	}

	pos := readOplogPosition(flags)
	where := map[string]interface{}{"args": strings.Join(args, " ")}
	if code := runGitMutation(flags, gitexec.NoDoor, append([]string{"reset"}, args...)...); code != 0 {
		appendOperationEntry(flags, sgDir, "reset", pos, false, where)
		return code
	}
	if flags.dryRun {
		return 0
	}

	appendOperationEntry(flags, sgDir, "reset", pos, true, where)
	return 0
}

func runBisect(flags globalFlags, args []string) int {
	if code := refuseUnsupportedBisect(parseGitArgs("bisect", args)); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// Unconditionally, for the same reason as reset: a bisect invocation
	// participates in the operation's state whatever it is spelled, so the lock
	// is not conditioned on the subcommand. The dirty-tree guard below is.
	release, code := acquireOperationLock(flags, gitDir, "bisect")
	if code != 0 {
		return code
	}
	defer release()

	// Which bisect subcommands may be refused over uncommitted work is DERIVED
	// from the classification table. The hand-kept list this replaced omitted
	// `skip`, `run` and `replay`, every one of which checks another commit out.
	if gitexec.WritesWorktree(append([]string{"bisect"}, args...)) {
		if code := coordGuard(flags, gitDir, "bisect"); code != 0 {
			return code
		}
	}

	// An unborn branch has no range to search, and `bisect start` is the one
	// subcommand that can be typed there: every other one needs a bisect that is
	// already running, which cannot have been started. Before git, and with no
	// oplog entry, like the sibling pre-git refusals.
	if code := refuseUnbornBisect(flags.ctx(), args); code != 0 {
		return code
	}

	// A bisect step moves HEAD and no branch ref -- `bisect start` detaches it
	// outright -- so its positions are OBSERVED ones. The ref is read before the
	// step, because after `bisect start` there is no branch name to read.
	pos := readOplogPosition(flags)
	where := map[string]interface{}{"args": strings.Join(args, " ")}
	if code := runGitMutation(flags, gitexec.NoDoor, append([]string{"bisect"}, args...)...); code != 0 {
		appendNavigationEntry(flags, sgDir, "bisect", pos.ref, pos.oldTip, "", false, where)
		return code
	}
	if flags.dryRun {
		return 0
	}

	newHead, _ := git.RevParse(flags.ctx(), "HEAD")
	appendNavigationEntry(flags, sgDir, "bisect", pos.ref, pos.oldTip, newHead, true, where)
	return 0
}

// passthroughRole is what a forwarded command line DOES, and the only thing the
// guarded passthrough branches on.
//
// The two roles reach git through the same code and differ in one check. A
// STATE-CONTROL form (`--abort`, `--quit`) acts on an operation git already has
// in flight: it is the way out of that state, and a check that refused it would
// strand the repository in the very state it was protecting. A COMPUTE form
// (`-n`/`--no-commit`) asks git to work out a merge, a pick or an inverse patch
// and stage it, and that is exactly what may not run over somebody else's parked
// operation -- see refuseComputeOverInFlight.
type passthroughRole int

const (
	passthroughStateControl passthroughRole = iota
	passthroughComputes
)

// runGuardedPassthrough forwards a STATE-CONTROL form to git behind the worktree
// guards.
//
// It handles no --help of its own, and neither does any other passthrough
// handler here. strictcli intercepts --help and -h anywhere in a passthrough's
// argv before dispatch, so a handler could only ever see them after a bare --,
// where the forwarded args begin with -- and never with --help. The help text
// these handlers used to print lives in the app.Passthrough registrations in
// main.go, which is what the framework actually renders.
func runGuardedPassthrough(flags globalFlags, gitCmd string, args []string) int {
	return guardedPassthrough(flags, gitCmd, args, passthroughStateControl)
}

// runComputingPassthrough forwards a `--no-commit` COMPUTE form to git behind
// the same guards PLUS the in-flight entry check.
//
// It is a door of its own rather than a flag on the one above because the role
// is a property of the DISPATCH SITE, not of the argv: the cherry-pick and
// revert handlers already decided which form they are looking at, and re-deriving
// it from the arguments down here would be a second parser answering a question
// that was already answered.
func runComputingPassthrough(flags globalFlags, gitCmd string, args []string) int {
	return guardedPassthrough(flags, gitCmd, args, passthroughComputes)
}

// guardedPassthrough runs a coordination check, then passes through to git.
func guardedPassthrough(flags globalFlags, gitCmd string, args []string, role passthroughRole) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	release, code := acquireOperationLock(flags, gitDir, gitCmd)
	if code != 0 {
		return code
	}
	defer release()

	if code := refuseOwnedConclusion(flags, gitDir, gitCmd, args); code != 0 {
		return code
	}

	if code := coordGuard(flags, gitDir, gitCmd); code != 0 {
		return code
	}

	// AFTER the coordination guard, so the ordinary dirty-tree case still carries
	// the listing of what is in the tree, and BEFORE the dry-run branch, because
	// the preview below computes the operation with git's own merge engine over
	// the very state being refused over. Only the compute forms reach it.
	if role == passthroughComputes {
		if code := refuseComputeOverInFlight(gitDir, gitCmd); code != 0 {
			return code
		}
	}

	if flags.dryRun {
		// No git ran: the invocation is recorded, and the outcome it would have
		// is COMPUTED with git's own merge engine rather than left unsaid.
		return previewSequencerOperation(flags, gitCmd, args, append([]string{gitCmd}, args...))
	}

	pos := readOplogPosition(flags)
	code = runPassthrough(flags, gitCmd, args)

	// Behind the exit code, and carrying it: the append used to run
	// unconditionally with the same shape either way, so a cherry-pick git
	// refused was recorded exactly like one git applied.
	appendOperationEntry(flags, sgDir, gitCmd, pos, code == 0,
		map[string]interface{}{"args": strings.Join(args, " ")})

	if code != 0 {
		announceWayOut(flags, gitDir)
	}
	return code
}

// runPassthrough executes a git command directly, forwarding all args.
//
// The context still carries the dispatch's repository-root pin; git.RunPassthrough
// suspends it under the declared operator-cwd exemption, because the argv here is
// the operator's own and any pathspec in it must mean what it meant where it was
// typed. Every other context-carried override still applies.
func runPassthrough(flags globalFlags, gitCmd string, args []string) int {
	ctx := flags.ctx()
	err := git.RunPassthrough(ctx, append([]string{gitCmd}, args...)...)
	// A failure that is NOT git's own exit status is one that happened before
	// git ran -- the execution boundary refusing to construct the invocation, a
	// missing binary -- so no child ever wrote to stderr and the reason has to be
	// printed here. Without this the operator gets a bare nonzero exit and no
	// text at all, which is what happened to a forwarded `--continue` the
	// single-authorship boundary declines to hand to git.
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
	return passthroughExitCode(err)
}

// passthroughStdout names where a passthrough child's stdout goes.
//
// In machine mode safegit's stdout carries exactly one document -- the
// framework's envelope -- so a child writing its own progress there would put a
// second document beside it and break every consumer that parses the stream.
// git's output is not discarded for that: it goes to stderr, which is what
// `push` already does with git's stdout under --json.
func passthroughStdout(flags globalFlags) io.Writer {
	if flags.json {
		return os.Stderr
	}
	return os.Stdout
}

// passthroughExitCode turns the error of a passthrough git invocation into the
// process exit code safegit propagates: git's own code when git ran and said
// no, and exitcode.General when the failure happened before git could speak.
//
// Every guarded passthrough renders its exit code through it, so "what does
// safegit exit when git exits N" has one answer wherever git is forwarded to.
func passthroughExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return exitcode.General
}
