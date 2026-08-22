package main

import (
	"context"
	"errors"
	"testing"

	"github.com/smm-h/safegit/internal/git"
)

// An empty expected old value is git's spelling for an UNCONDITIONAL ref write:
// the ref moves to whatever the caller computed, no matter what another process
// did to it in the meantime. That is exactly the compare-and-swap safegit
// exists to provide, so internal/git refuses it outright and a caller that
// means "this ref must not exist yet" says so with git.ZeroSHA.
//
// The commit pipeline's ref-update port is the one ref-move path that did not
// hold to that: it quietly substituted ZeroSHA for an empty expectation, which
// turns "I do not know what is there" into "I assert nothing is there" -- a
// refusal from git at best, and a rule that exists in one place and not the
// other at worst. The pipeline always decides explicitly today, which is
// precisely why the substitution had to go: nothing exercised it, so nothing
// would have noticed it starting to matter.
func TestEffectsRefUpdateRefusesAnEmptyExpectedValue(t *testing.T) {
	// The zero globalFlags carries no effects handle, so this call reaching one
	// would panic. It returning an error is itself the statement that the
	// refusal comes first -- before any effect is minted, let alone performed.
	err := effectsRefUpdate{}.Update(context.Background(), "refs/heads/main",
		"1111111111111111111111111111111111111111", "")
	if err == nil {
		t.Fatal("an empty expected old value was accepted; it is git's spelling for an unconditional write")
	}
	if !errors.Is(err, git.ErrNoExpectedValue) {
		t.Errorf("the port must refuse on the same terms git.UpdateRef does (%v), got: %v",
			git.ErrNoExpectedValue, err)
	}
}
