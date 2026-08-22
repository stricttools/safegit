package main

import (
	"fmt"
	"sort"
	"strings"
)

// IntendedChange is what a rewrite operation DECIDED to change in one commit,
// recorded while the decision was made rather than read back off the result:
// the repo-relative paths whose blob (or gitlink) the operation replaced, and
// whether it rewrote the commit message.
//
// Tier A verification diffs the old and new commit and refuses the whole
// rewrite when the two disagree. That is the preservation property: a rewrite
// may change what it said it would change, and nothing else.
type IntendedChange struct {
	Paths          map[string]bool
	MessageChanged bool
}

// Empty reports whether this declaration says the commit's content is
// untouched. Such a commit may still be REWRITTEN -- a new commit object is
// created whenever a parent moved -- but its tree and message must come
// through identical.
func (c IntendedChange) Empty() bool {
	return len(c.Paths) == 0 && !c.MessageChanged
}

// PathList renders the declared paths in a stable order for an error message.
func (c IntendedChange) PathList() string {
	if len(c.Paths) == 0 {
		return "(none)"
	}
	paths := make([]string, 0, len(c.Paths))
	for p := range c.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return strings.Join(paths, ", ")
}

// IntentKind says what KIND of expectation a rewrite carries, because the two
// answers a verifier can act on are different questions -- and because the
// zero value must be neither of them.
type IntentKind int

const (
	// IntentUnset is the zero value and is always an error: a rewrite that
	// reaches Finalize without declaring what it meant to do cannot be
	// verified, and silently skipping the verification is exactly the hole the
	// declaration exists to close.
	IntentUnset IntentKind = iota

	// IntentPerPath means the rewrite declared, per commit, which paths and
	// which messages it changes. Every scrub carries this.
	IntentPerPath

	// IntentIdentityOnly means the rewrite changes commit IDENTITY headers
	// (author, committer, tagger) rather than content, so there is no per-path
	// expectation to check -- but every tree must still come through
	// byte-identical, and a message may change only where the walk declared it
	// rewrote an identity-bearing trailer. `author rewrite` carries this.
	IntentIdentityOnly
)

// RewriteIntent is the declaration a rewrite hands to Finalize.
type RewriteIntent struct {
	Kind IntentKind

	// Changes is keyed by OLD commit SHA. A commit with no entry declares
	// nothing changed in it. Under IntentIdentityOnly only the MessageChanged
	// member is ever set.
	Changes map[string]IntendedChange
}

// PerPathIntent starts an empty per-path declaration for a walk to fill in.
func PerPathIntent() *RewriteIntent {
	return &RewriteIntent{Kind: IntentPerPath, Changes: make(map[string]IntendedChange)}
}

// IdentityIntent declares a rewrite that changes identity headers only. Its
// Changes map carries one thing: the commits whose MESSAGE the rewrite also
// changed, because an identity-bearing trailer (Signed-off-by, Co-authored-by)
// names the same person the headers do.
func IdentityIntent() *RewriteIntent {
	return &RewriteIntent{Kind: IntentIdentityOnly, Changes: make(map[string]IntendedChange)}
}

// Declare records what the operation decided for one commit. Repeated calls for
// the same commit accumulate, so a transform that runs several steps (a blob
// map, then a hash remap) can declare each step as it happens.
func (ri *RewriteIntent) Declare(oldSHA string, paths []string, messageChanged bool) {
	if ri == nil || ri.Kind == IntentUnset {
		return
	}
	if ri.Changes == nil {
		ri.Changes = make(map[string]IntendedChange)
	}
	c, ok := ri.Changes[oldSHA]
	if !ok {
		c = IntendedChange{Paths: make(map[string]bool)}
	}
	if c.Paths == nil {
		c.Paths = make(map[string]bool)
	}
	for _, p := range paths {
		c.Paths[p] = true
	}
	if messageChanged {
		c.MessageChanged = true
	}
	ri.Changes[oldSHA] = c
}

// ChangedCommits returns the OLD SHAs the operation declared a change in.
func (ri *RewriteIntent) ChangedCommits() []string {
	if ri == nil {
		return nil
	}
	var shas []string
	for sha, c := range ri.Changes {
		if !c.Empty() {
			shas = append(shas, sha)
		}
	}
	sort.Strings(shas)
	return shas
}

// For returns the declaration for one old commit; the zero declaration means
// "this commit's content is untouched".
func (ri *RewriteIntent) For(oldSHA string) IntendedChange {
	if ri == nil {
		return IntendedChange{}
	}
	return ri.Changes[oldSHA]
}

// validate refuses a declaration that cannot be verified.
func (ri *RewriteIntent) validate() error {
	if ri == nil || ri.Kind == IntentUnset {
		return fmt.Errorf("the rewrite reached Finalize without declaring what it intended to change; " +
			"this is a safegit bug -- every rewrite must set RewriteResult.Intent")
	}
	return nil
}
