package commit

import "context"

// RefUpdate performs the compare-and-swap that makes a commit the branch's tip
// -- the ONE mutation the commit pipeline makes on the world.
//
// It is an input rather than a call into internal/git because that update is
// the seam a preview stops at: the caller's implementation mints it through the
// framework's effects handle, which performs it in an executing run and RECORDS
// it in a dry run, so a preview's would-do log states the move it would make
// instead of rendering an empty body. There is exactly one mint site, inside
// the compare-and-swap retry loop; a second one alongside it would fire twice
// per commit and record a move the loop had already made.
//
// The interface is the pipeline's own rather than the framework's handle type
// because the handle's result carrier cannot be constructed outside the
// framework (its settled-ness is unexported and every accessor panics when
// unsettled), which would leave this package's own tests unable to supply one.
// Production has a single implementation and it IS the handle.
type RefUpdate interface {
	// Update moves ref to newSHA, but only while it still points at expected --
	// git's ZeroSHA where the caller means "this ref must not exist yet".
	//
	// The returned error carries git's own message, which is what makes a
	// transient ref-lock failure ("cannot lock ref") tellable from a real
	// refusal: the pipeline classifies it and retries the whole attempt rather
	// than failing the commit.
	//
	// In a preview the invocation is recorded and nothing is performed, so the
	// answer is nil and the retry loop ends after one pass -- there is no ref
	// movement for a second attempt to reconcile with.
	Update(ctx context.Context, ref, newSHA, expected string) error
}
