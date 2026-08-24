package gitexec

import (
	"sort"
	"strings"
)

// The single-authorship boundary.
//
// Every commit safegit creates is authored by its own commit pipeline: safegit's
// trailers on it, the repository's commit-msg hook run over it, an oplog entry
// recording it, and `safegit undo` able to reverse it. There is no second class
// of authorship in which git writes a commit behind a safegit command name.
//
// That invariant is enforced here rather than maintained by convention. Every
// git argv safegit builds passes Validate, and Validate refuses one whose
// verb-and-flags shape would let git author a commit unless the call site names
// a DECLARED DOOR. The verb table (classify.go) holds the shape half -- which
// verbs can author, and which tokens take the authoring away -- and this file
// holds the door half: the closed list of sites permitted to open one, in code,
// with the reason.
//
// The one door is `safegit rebase`. A rebase is a replay, git performs it, and
// every commit it produces is git's -- uniformly, on every rebase, never as a
// run-time split. The native pipeline-authored rebase is a declared later
// project (todo/pipeline-authored-rebase.md); until it exists, the door is the
// honest way to say that this ONE command is the exception.

// DoorID identifies one declared door through the single-authorship boundary.
// The value is the code site it covers, so a reader of the table can go straight
// there.
type DoorID string

// NoDoor is the absence of a door: the call site does not let git author a
// commit, and an authoring argv from it is refused. It is the value every site
// but one passes.
const NoDoor DoorID = ""

// DoorRebasePassthrough covers main.runRebase.
const DoorRebasePassthrough DoorID = "main.runRebase"

// AuthoringDoor is one row of the declared table.
type AuthoringDoor struct {
	ID DoorID
	// Verbs are the git subcommands this door opens, and NOTHING else. A door is
	// permission for one operation, not a general licence: naming this door on
	// an argv outside the list is refused too, because a site claiming a
	// permission it was not given is a programming error whether or not that
	// particular argv happened to author anything.
	Verbs  []string
	Reason string
}

var authoringDoors = []AuthoringDoor{
	{
		ID:    DoorRebasePassthrough,
		Verbs: []string{"rebase"},
		Reason: "a rebase is git's replay from end to end: git decides the order, applies each patch and authors each replayed commit, and safegit has no verb that finishes one. " +
			"The behavior is uniform -- every commit a rebase produces is git's, always -- rather than a split decided at run time, and it is recorded as such in docs/divergences.md",
	},
}

// byDoorID indexes the door table.
var byDoorID = func() map[DoorID]AuthoringDoor {
	m := make(map[DoorID]AuthoringDoor, len(authoringDoors))
	for _, d := range authoringDoors {
		m[d.ID] = d
	}
	return m
}()

// AuthoringDoors returns the declared table, sorted by ID. It returns a copy:
// the table is the boundary's, not a caller's, to change.
func AuthoringDoors() []AuthoringDoor {
	out := append([]AuthoringDoor(nil), authoringDoors...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// opens reports whether this door covers the given subcommand.
func (d AuthoringDoor) opens(subcommand string) bool {
	for _, v := range d.Verbs {
		if v == subcommand {
			return true
		}
	}
	return false
}

// lookupDoor returns the declared row, or an error naming the undeclared
// identifier.
func lookupDoor(id DoorID) (AuthoringDoor, error) {
	d, ok := byDoorID[id]
	if !ok {
		return AuthoringDoor{}, &Error{Msg: "gitexec: undeclared single-authorship door " + string(id) +
			"; declare it in internal/gitexec/authoring.go, with the reason git is allowed to author commits there"}
	}
	return d, nil
}

// Authoring reports whether an argv would let GIT author a commit: a verb the
// table marks Authors, with none of that verb's suppressing tokens present.
//
// An argv the table does not declare, and one that names no subcommand at all,
// report TRUE -- the same default-deny WritesObjects and WritesWorktree take. An
// unknown invocation is never assumed harmless. Validate runs its vocabulary
// check first, so neither case ever produces the authoring refusal in practice;
// the default is for any other reader of this view.
func Authoring(args []string) bool {
	name, ok := Subcommand(args)
	if !ok {
		return true
	}
	v, ok := byName[name]
	if !ok {
		return true
	}
	if !v.Authors {
		return false
	}
	for _, tok := range v.SuppressedBy {
		if argvHas(args, name, tok) {
			return false
		}
	}
	return true
}

// checkAuthoring is Validate's single-authorship half: it is what makes "git
// never authors a commit through safegit, except `safegit rebase`" a property of
// the execution boundary rather than a claim about the call sites.
//
// It runs AFTER the vocabulary check, so an undeclared subcommand is reported as
// one instead of as an authoring refusal.
func checkAuthoring(door DoorID, args []string) error {
	name, _ := Subcommand(args)

	if door != NoDoor {
		d, err := lookupDoor(door)
		if err != nil {
			return err
		}
		if !d.opens(name) {
			return &Error{Msg: "gitexec: the declared door " + string(door) + " opens `git " + strings.Join(d.Verbs, "`, `git ") +
				"` and not `git " + name + "`; a call site may not name a door for an argv it was not given"}
		}
		return nil
	}

	if !Authoring(args) {
		return nil
	}
	return &Error{Msg: "gitexec: refusing to run `git " + strings.Join(args, " ") +
		"`: this argv would let git AUTHOR a commit, and every commit safegit makes comes from its own commit pipeline -- " +
		"safegit's trailers on it, the repository's commit-msg hook run over it, and `safegit undo` able to reverse it. " +
		"The one declared exception is `safegit rebase`, where git performs the replay (the door table is in internal/gitexec/authoring.go)"}
}
