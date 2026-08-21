package sequencer

import (
	"context"
	"fmt"

	"github.com/smm-h/safegit/internal/git"
)

// SourceAuthor returns the author identity recorded on the commit a cherry-pick
// or revert is applying -- the commit State.Source names. It is the one fact a
// conclusion needs that is not written into any state file, so it is the one
// function in this package that runs git.
//
// It is a separate call rather than a field on State because Read is on every
// refusal check's path and must not fork a subprocess, and because the identity
// is a property of a commit object rather than of the sequencer state. A caller
// that needs it asks for it.
//
// What the identity means is the caller's business: git's cherry-pick preserves
// it on the new commit while git's revert does not, and this function reports
// the fact for both without taking a position.
//
// It is an error to call it on any other kind, or on a queued state that is
// between steps and therefore names no source commit.
func SourceAuthor(ctx context.Context, s State) (git.AuthorInfo, error) {
	switch s.Kind {
	case KindCherryPick, KindRevert:
	default:
		return git.AuthorInfo{}, fmt.Errorf("sequencer: %s has no source commit to read an author from", s.Kind)
	}
	if s.Source == "" {
		return git.AuthorInfo{}, fmt.Errorf("sequencer: the %s names no source commit", s.Kind)
	}
	info, err := git.ParseCommit(ctx, s.Source)
	if err != nil {
		return git.AuthorInfo{}, fmt.Errorf("reading the %s source commit %s: %w", s.Kind, s.Source, err)
	}
	return info.Author, nil
}
