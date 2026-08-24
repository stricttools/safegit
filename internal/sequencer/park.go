package sequencer

import (
	"fmt"
	"os"
	"path/filepath"
)

// The one WRITE this package makes, and why it exists at all.
//
// `git cherry-pick --no-commit` records nothing about the commit it is
// applying: it stages the result and stops, and CHERRY_PICK_HEAD is written
// only when git is going to commit the pick itself. That is a problem for the
// restructured `safegit cherry-pick`, which computes exactly that way and then
// commits the staged result through safegit's own pipeline: the file is what
// Read reports as an operation in flight, what SourceAuthor resolves the
// preserved identity from, what the conflict listing names the incoming side
// after, what `git status` reads to say a pick is under way, and what
// `git cherry-pick --abort` needs in order to undo one.
//
// So safegit writes it. Writing it here rather than at the caller is not
// tidiness: the FORMAT is this package's -- one full object name and a
// newline, which readSHA is the reader of -- and a writer that drifted from
// that reader would produce a file every safegit command in the repository
// then refuses to read.
//
// `git revert --no-commit` needs no such thing: it writes REVERT_HEAD on both
// the clean and the conflicted path (probe-verified), which is why only the
// cherry-pick has a marker function.

// MarkCherryPick writes CHERRY_PICK_HEAD naming the commit being applied, in
// git's own format, so a cherry-pick computed with `--no-commit` is in flight
// exactly as far as git, Read and every conclusion path are concerned.
//
// The write is atomic in content: a temporary sibling is renamed into place, so
// a process killed mid-write leaves either the previous file or the complete
// new one and never a truncated object name -- which Read would refuse, taking
// every other safegit command in the repository down with it.
func MarkCherryPick(gitDir, sha string) error {
	if !isObjectName(sha) {
		return fmt.Errorf("sequencer: %q is not a full object name; %s must name the commit being applied", sha, FileCherryPickHead)
	}
	final := filepath.Join(gitDir, FileCherryPickHead)

	tmp, err := os.CreateTemp(gitDir, FileCherryPickHead+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating the temporary file for %s: %w", final, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(sha + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	// git's own state files are world-readable; CreateTemp makes 0600.
	if err := os.Chmod(tmpName, 0644); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("publishing %s: %w", final, err)
	}
	return nil
}
