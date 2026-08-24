package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/exitcode"
)

// Aftercare: the steps that can only run once a ref has moved, and the one
// vocabulary every command that runs them reports them in.
//
// What makes them a family is not what they do but what a failure in any of
// them means. The commit is REAL -- the object exists, the ref points at it,
// `safegit undo` can reverse it -- and something safegit owed afterwards did
// not finish. That is neither a success nor a refusal, and reporting it as
// either is what this file exists to stop:
//
//   - as a success (exit 0), because a caller would never look at the message
//     saying their work is parked in a stash or their index is out of step;
//   - as the undifferentiated General (exit 1), because a caller cannot tell
//     "it did not happen" from "it happened and its aftercare did not", and the
//     guess a script makes by default is to retry -- which makes a second
//     commit.
//
// So every member exits exitcode.CommitStands, and every member REPORTS: the
// envelope is emitted with the payload the run would have carried, so machine
// mode never answers an operator with an empty stdout beside a ref that moved.
// That is why these sites return rather than call die(): os.Exit runs below the
// seam that emits the envelope. (Lock release is NOT the reason -- die()
// releases pending locks already.)

// The aftercare steps, named once. Each string is what the payload's residue
// member carries and what the stderr line reads, so an operator and a consumer
// are told the same thing in the same words.
//
// internal/commit declares the one step the pipeline itself performs after the
// ref update (commit.StepIndexReconcile); the rest belong to the commands.
const (
	// stepStateCleanup is removing a concluded operation's whole state-file set.
	stepStateCleanup = "removing the operation's state files"
	// stepIndexResolve is applying the declared resolutions to the shared index.
	stepIndexResolve = "resolving the shared index"
	// stepWorktreeResolution is writing the resolved content into the working
	// tree, which is what git's own `checkout --ours` and `rm` do.
	stepWorktreeResolution = "writing the resolved content into the working tree"
	// stepParentBump is committing a parent repository's moved gitlink. The
	// wording keeps the config key's own vocabulary (commit.autoBumpParent), so
	// an operator reading the message and an operator reading the config are
	// looking at the same word.
	stepParentBump = "the auto-bump of the parent's gitlink"
	// stepAutostashApply is putting back the uncommitted work git set aside
	// before a merge began.
	stepAutostashApply = "applying the autostash"
	// stepAutostashStore is parking that work as a stash entry after the apply
	// conflicted, which is the last place it can be reached from.
	stepAutostashStore = "storing the autostash as a stash entry"
	// stepAutostashFile is removing MERGE_AUTOSTASH once the work it points at
	// has been put somewhere else.
	stepAutostashFile = "removing MERGE_AUTOSTASH"
	// stepAutostashState is removing the state the autostash's own apply left
	// behind, which is a merge's residue because the apply is a merge.
	stepAutostashState = "removing the autostash apply's leftover state"
	// stepAutostashForeign is the autostash this merge did not create: the file
	// names a commit that is not the stash git made here, so the conclusion put
	// nothing back and removed nothing. It is residue because the file is still
	// there and something has to be done about it -- by an operator, not by a
	// conclusion guessing whose work it holds.
	stepAutostashForeign = "applying an autostash this merge did not create"
)

// residueEntry is one aftercare step that did not finish, as the payload reports
// it. Step is from the vocabulary above; Detail is the same sentence the stderr
// line carried, so a consumer needs no second channel to learn what happened.
type residueEntry struct {
	Step   string `json:"step"`
	Detail string `json:"detail"`
}

// The states an autostash can end a conclusion in. They are an enum rather than
// a pair of booleans because the four are mutually exclusive answers to one
// question -- where is the operator's uncommitted work now -- and a consumer
// that has to reconstruct that from flags will get it wrong.
const (
	// autostashNone: git set nothing aside, so there was nothing to put back.
	autostashNone = "none"
	// autostashApplied: the work is back in the working tree.
	autostashApplied = "applied"
	// autostashStored: the apply conflicted, so the work is a stash entry
	// (stash@{0}) and MERGE_AUTOSTASH is gone.
	autostashStored = "stored"
	// autostashUnstored: the apply conflicted AND storing it failed, so the
	// stash commit named in MERGE_AUTOSTASH is the only name the work has left.
	autostashUnstored = "unstored"
	// autostashPending: the aftercare stopped before the autostash was reached,
	// so MERGE_AUTOSTASH still holds the work untouched.
	autostashPending = "pending"
	// autostashForeign: MERGE_AUTOSTASH names a commit that is not the stash git
	// made for THIS merge -- residue of an operation that is over, or a file
	// somebody wrote by hand. It was neither applied nor removed, so the work it
	// names (whosever it is) is exactly where it was.
	autostashForeign = "foreign"
)

// autostashOutcome is what became of the work git set aside, as the payload
// reports it.
type autostashOutcome struct {
	State string `json:"state"`
	// Stash is the stash COMMIT -- the object MERGE_AUTOSTASH names, and the
	// object `git stash apply` takes -- and null where there was no autostash at
	// all. It is the same value in every non-none state, because the states
	// differ in where that commit can be REACHED from, not in which commit it is.
	Stash *string `json:"stash"`
}

// noAutostash is the outcome of a conclusion git set nothing aside for, and of
// every conclusion that is not a merge's.
func noAutostash() autostashOutcome { return autostashOutcome{State: autostashNone} }

// aftercareExit turns a residue list into the process exit code. An empty list
// is a run that finished everything it owed.
func aftercareExit(residue []residueEntry) int {
	if len(residue) == 0 {
		return exitcode.OK
	}
	return exitcode.CommitStands
}

// orEmptyResidue renders the residue list for a payload, never nil: a declared
// member arriving as null invites a consumer to read "nothing was left behind"
// as "this run did not answer".
func orEmptyResidue(residue []residueEntry) []residueEntry {
	if residue == nil {
		return []residueEntry{}
	}
	return residue
}

// commitStands recognizes the pipeline's commit-stands verdict.
//
// It is the ONE place that recognition happens, so no caller can accidentally
// treat a ref that moved as a refusal. A caller must also check that the result
// value came back non-nil, because the report it builds is made of it.
func commitStands(err error) *commit.PartialError {
	var pe *commit.PartialError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// reportAftercareFailure prints one aftercare failure and records it.
//
// stderr unconditionally, --quiet included: stdout belongs to the envelope in
// machine mode, and a request for less chatter is not a request to be left
// guessing where one's own work went.
func reportAftercareFailure(residue []residueEntry, step string, err error) []residueEntry {
	detail := fmt.Sprintf("%s failed: %v", step, err)
	fmt.Fprintf(os.Stderr, "error: %s\n", detail)
	return append(residue, residueEntry{Step: step, Detail: detail})
}

// recordAftercareFailure records an aftercare failure whose message the caller
// has already written itself, for the sites whose wording is several lines of
// recovery instructions rather than one sentence.
func recordAftercareFailure(residue []residueEntry, step, detail string) []residueEntry {
	return append(residue, residueEntry{Step: step, Detail: detail})
}
