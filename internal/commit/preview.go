// The preview area: where a dry run does its work, so that a preview of a
// commit leaves the repository byte-identical.
package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
)

// BeginPreview opens the throwaway area a dry run works in, and returns the
// context every git call of that run must be made with.
//
// It is the SINGLE preview-area constructor for the whole tool. It lives here
// because the commit pipeline was the first command family to write objects in
// a preview, and it is exported because it is no longer the only one: the
// honest --dry-run of merge, cherry-pick and revert computes its answer with
// `git merge-tree --write-tree`, which writes real tree and blob objects and
// therefore needs exactly this quarantine. Two constructors would be two
// answers to "where does a preview put the objects it makes".
//
// A preview computes real answers: it stages into an index, writes a tree and
// (for a commit or an amend) builds the commit object, because that is the only
// honest way to report which paths the operation would change. Every one of
// those steps writes objects. Pointing GIT_OBJECT_DIRECTORY at a directory
// inside the preview area is what keeps the arithmetic exact while leaving the
// repository's own object store untouched -- the tree SHA the preview reports is
// the tree SHA the real run would produce, and the objects behind it go away
// with the area.
//
// Ordering, which is not incidental: the repository's object store is resolved
// BEFORE the quarantine is installed and the quarantine directory is created
// BEFORE it is named in an environment, because a GIT_OBJECT_DIRECTORY that
// points at a directory that does not exist makes git fail repository discovery
// outright ("not a git repository").
//
// The area's lifetime is the whole operation, not one compare-and-swap attempt:
// the retry loop stages again from scratch each time, and an area per attempt
// would multiply directories for no gain. Not a dry run returns the context
// unchanged and an empty area, which is the signal to stage under the safegit
// directory as an executing run does.
func BeginPreview(ctx context.Context, dryRun bool) (previewCtx context.Context, area string, cleanup func(), err error) {
	if !dryRun {
		return ctx, "", func() {}, nil
	}

	objects, err := git.ObjectsDir(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolving the repository's object store for the preview quarantine: %w", err)
	}

	area, err = os.MkdirTemp("", "safegit-preview-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("creating preview area: %w", err)
	}
	cleanup = func() { os.RemoveAll(area) }

	quarantine := filepath.Join(area, "objects")
	if err := os.MkdirAll(quarantine, 0755); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("creating the preview object quarantine: %w", err)
	}

	return gitexec.WithObjectQuarantine(ctx, quarantine, objects), area, cleanup, nil
}
