package coord

import (
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/sequencer"
)

// SequencerContext is a caller's DECLARATION that it is the conclusion path for
// an in-flight git operation.
//
// Every ordinary caller passes nil, which means "refuse if anything is in
// flight" -- a commit, an amend, a reword or an undo taken while git is
// mid-merge or mid-cherry-pick builds its tree from a parent commit and hands
// commit-tree a single parent, silently discarding the operation's staged
// result and its second parent. A non-nil context means the caller IS the
// command that finishes the named operation and must be allowed to commit
// during exactly the state everyone else is refused for.
//
// The declaration is checked, not trusted: a context naming an operation other
// than the one actually in flight is itself a refusal, as is a context supplied
// when nothing is in flight at all.
type SequencerContext struct {
	// Kind is the operation this caller concludes.
	Kind sequencer.Kind
}

// WayOut names the commands that end an in-flight operation: the one that
// concludes it, keeping the work, and the one that abandons it, throwing the
// work away.
//
// It is the single authority for that advice. Every refusal safegit prints
// while an operation is in flight renders it from here, so no two refusals can
// name different commands for the same state.
type WayOut struct {
	// Conclude finishes the operation and produces its commit.
	Conclude string
	// Abandon throws the operation away and restores the pre-operation tip.
	Abandon string
}

// WayOutOf returns the way out of the state s reports.
//
// Where safegit owns the conclusion it names its own command; where it does not
// it names git's, and it never names git's rebase commands for a `git am` or
// the other way round -- the two share a state directory and an operator sent
// to the wrong one gets a refusal, not a conclusion.
func WayOutOf(s sequencer.State) WayOut {
	switch s.Kind {
	case sequencer.KindMerge:
		return WayOut{Conclude: "safegit merge-continue", Abandon: "git merge --abort"}
	case sequencer.KindCherryPick:
		return WayOut{Conclude: "safegit cherry-pick-continue", Abandon: "git cherry-pick --abort"}
	case sequencer.KindRevert:
		return WayOut{Conclude: "safegit revert-continue", Abandon: "git revert --abort"}
	case sequencer.KindRebase:
		// Rebase conclusion stays git's own: safegit classifies the state and
		// refuses during it, but has no verb that finishes a rebase.
		return WayOut{Conclude: "git rebase --continue", Abandon: "git rebase --abort"}
	case sequencer.KindAM:
		return WayOut{Conclude: "git am --continue", Abandon: "git am --abort"}
	}
	return WayOut{}
}

// InFlightError is the refusal a command owes an operator when git has an
// operation in flight that the command cannot run against. Its message states
// what is in flight, factually, and the way out.
type InFlightError struct {
	// Operation is what safegit refused, in the operator's vocabulary
	// ("commit", "amend", "reword", "undo").
	Operation string
	// State is what the sequencer reader found.
	State sequencer.State
}

func (e *InFlightError) Error() string { return RefuseInFlight(e.Operation, e.State) }

// RefuseInFlight renders the refusal text for one operation against one state.
func RefuseInFlight(operation string, s sequencer.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing %s: %s is in progress", operation, s.String())
	w := WayOutOf(s)
	if w.Conclude != "" {
		fmt.Fprintf(&b, "\n  conclude it:  %s", w.Conclude)
	}
	if w.Abandon != "" {
		fmt.Fprintf(&b, "\n  abandon it:   %s", w.Abandon)
	}
	return b.String()
}

// GuardInFlight is the one check that decides whether operation may run against
// whatever git has in flight in gitDir. It returns nil when it may, an
// *InFlightError when the state forbids it, and a plain error when the state
// could not be read at all -- which is also a refusal, because a state file
// safegit cannot parse is not evidence that nothing is in flight.
//
// declared is the caller's SequencerContext: nil for every ordinary caller.
//
// It is filesystem-only (sequencer.Read starts no subprocess), so putting it on
// the hot path of commit costs a handful of stat calls.
func GuardInFlight(gitDir, operation string, declared *SequencerContext) error {
	state, err := sequencer.Read(gitDir)
	if err != nil {
		return fmt.Errorf("refusing %s: cannot read git's in-flight operation state: %w", operation, err)
	}

	if !state.InProgress() {
		if declared != nil {
			return fmt.Errorf("refusing %s: it declared itself the conclusion of a %s, but no operation is in progress",
				operation, declared.Kind)
		}
		return nil
	}

	if declared == nil {
		return &InFlightError{Operation: operation, State: state}
	}
	if declared.Kind != state.Kind {
		return fmt.Errorf("refusing %s: it declared itself the conclusion of a %s, but %s is in progress",
			operation, declared.Kind, state.String())
	}
	return nil
}
