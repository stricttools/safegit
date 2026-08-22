package git

import (
	"context"
	"fmt"
	"strings"
)

// IndexStage0 is one already-decided resolution applied to an index: every slot
// the path currently occupies is replaced by a single stage-0 entry naming Mode
// and SHA.
//
// An empty Mode removes the path from the index entirely instead, which is what
// resolving a conflict by deleting the path means.
type IndexStage0 struct {
	Path string
	Mode string
	SHA  string
}

// SetIndexStage0 applies resolutions to the index at indexPath, in one
// `git update-index --index-info` batch.
//
// This is how a conflict is resolved in an index without going near the working
// tree: a conflicted path occupies stages 1, 2 and 3, and writing a stage-0
// entry for it is what makes `git write-tree` accept it. Each path is preceded
// by a zero-mode removal line, because git will not hold stage 0 and a higher
// stage for one path at once, and because removing a path the index does not
// hold is a no-op -- so the same batch expresses both "resolve to this blob" and
// "remove this path".
//
// indexPath names the index to write, and an empty indexPath writes the
// repository's shared index -- the same convention UnmergedStages reads by. The
// shared index is written by exactly one caller, the conclusion's own
// reconciliation, and always under the worktree operation lock.
func SetIndexStage0(ctx context.Context, indexPath string, entries []IndexStage0) error {
	if len(entries) == 0 {
		return nil
	}

	var b strings.Builder
	for _, e := range entries {
		b.WriteString(indexRemovalLine(e.Path))
		if e.Mode == "" {
			continue
		}
		b.WriteString(indexInfoLine(indexSlot{Mode: e.Mode, SHA: e.SHA, Stage: 0, Path: e.Path}))
	}

	var env []string
	if indexPath != "" {
		env = []string{"GIT_INDEX_FILE=" + indexPath}
	}
	if _, _, err := RunWithEnvStdin(ctx, env, []byte(b.String()), "update-index", "--index-info"); err != nil {
		return fmt.Errorf("writing %d resolved index entr(ies): %w", len(entries), err)
	}
	return nil
}
