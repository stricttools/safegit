// Already-decided index edits: the mechanical half of concluding an operation
// git stopped mid-flight.
package commit

import (
	"context"
	"fmt"

	"github.com/smm-h/safegit/internal/git"
)

// IndexEditKind names what one edit does to the temporary index.
type IndexEditKind int

const (
	// IndexEditBlob places Mode and SHA at stage 0, replacing every slot the
	// path holds.
	IndexEditBlob IndexEditKind = iota
	// IndexEditWorktree stages the working-tree file at Path, whatever it now
	// holds, replacing every slot the path holds.
	IndexEditWorktree
	// IndexEditRemove removes every slot the path holds, so the commit does not
	// contain it. The working-tree file is not touched.
	IndexEditRemove
)

// IndexEdit is one caller-decided change to the temporary index, applied after
// the index is seeded and before anything else is staged.
//
// It carries no conflict vocabulary on purpose. Deciding that `--resolve
// path=theirs` means "the stage-3 blob" is the conclusion engine's job, and it
// is done once, against the shared index, before the pipeline runs; what arrives
// here is a mode, an object name and a path, which the pipeline applies without
// interpreting. That split is what keeps the pipeline free of any opinion about
// merges while still writing the objects inside a dry run's quarantine and
// re-applying every edit on a compare-and-swap retry.
type IndexEdit struct {
	Kind IndexEditKind
	Path string
	// Mode and SHA are read for IndexEditBlob and ignored otherwise.
	Mode string
	SHA  string
}

// ApplyIndexEditsTo applies the same edits to an index OUTSIDE the pipeline,
// resolving the repository root itself.
//
// It exists for one caller: a conclusion, once its commit is real, has to put
// the shared index in the same state before reconciling it, because the
// reconciliation deliberately preserves unmerged stages and would otherwise
// preserve the very conflict the conclusion just resolved. An empty indexPath
// means the shared index.
func ApplyIndexEditsTo(ctx context.Context, indexPath string, edits []IndexEdit) error {
	if len(edits) == 0 {
		return nil
	}
	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		return fmt.Errorf("resolving repo root: %w", err)
	}
	return applyIndexEdits(ctx, indexPath, repoRoot, edits)
}

// applyIndexEdits performs the caller's edits against the temporary index.
//
// The blob and removal edits go out in one `--index-info` batch, because they
// are pure index writes with no working-tree involvement. The working-tree edits
// follow, one `git add` each, which is what hashes the file's current content
// and records its mode. The two groups never name the same path -- duplicate
// paths are refused before a request is built -- so their order is immaterial.
func applyIndexEdits(ctx context.Context, indexPath, repoRoot string, edits []IndexEdit) error {
	if len(edits) == 0 {
		return nil
	}

	var batch []git.IndexStage0
	for _, e := range edits {
		switch e.Kind {
		case IndexEditBlob:
			batch = append(batch, git.IndexStage0{Path: e.Path, Mode: e.Mode, SHA: e.SHA})
		case IndexEditRemove:
			batch = append(batch, git.IndexStage0{Path: e.Path})
		case IndexEditWorktree:
			// Applied below, after the batch.
		default:
			return fmt.Errorf("index edit for %s carries kind %d, which is not one of blob, worktree or remove", e.Path, int(e.Kind))
		}
	}
	if err := git.SetIndexStage0(ctx, indexPath, batch); err != nil {
		return err
	}

	for _, e := range edits {
		if e.Kind != IndexEditWorktree {
			continue
		}
		if err := git.AddFile(ctx, indexPath, git.Anchor(repoRoot, e.Path)); err != nil {
			return fmt.Errorf("staging the working-tree content of %s: %w", e.Path, err)
		}
	}
	return nil
}
