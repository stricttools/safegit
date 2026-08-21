package sequencer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Paths returns the state files and directories that constitute an operation,
// named relative to the git directory, in a stable order. It is the declared
// set Cleanup removes, exposed so a caller (or a test) can see the contract
// rather than infer it.
//
// The sets:
//
//	merge        MERGE_HEAD, MERGE_MODE, MERGE_MSG, AUTO_MERGE
//	cherry-pick  CHERRY_PICK_HEAD, MERGE_MSG, AUTO_MERGE, sequencer/
//	revert       REVERT_HEAD, MERGE_MSG, AUTO_MERGE, sequencer/
//
// MERGE_MSG and AUTO_MERGE belong to all three: git writes them for every one
// of these operations and removes them when the operation concludes, so they
// are not "another operation's files" that a cleanup could leave behind.
//
// Paths returns nil for KindNone, KindRebase and KindAM. safegit owns no part
// of a rebase's or a mailbox application's state: those are concluded and
// abandoned by git's own commands, which restore a great deal more than a set
// of files (the original branch tip, the remaining todo, the reflog trail).
// Cleanup refuses them for the same reason.
func Paths(k Kind) []string {
	switch k {
	case KindMerge:
		return []string{FileMergeHead, FileMergeMode, FileMergeMsg, FileAutoMerge}
	case KindCherryPick:
		return []string{FileCherryPickHead, FileMergeMsg, FileAutoMerge, DirSequencer}
	case KindRevert:
		return []string{FileRevertHead, FileMergeMsg, FileAutoMerge, DirSequencer}
	}
	return nil
}

// Cleanup removes exactly the state files and directories of operation k from
// gitDir -- Paths(k), nothing more and nothing less -- and is the single
// implementation of that removal: the conclusion commands and the restructured
// revert all call it rather than each deleting their own idea of the set.
//
// A path that is already absent is not an error; git writes several of these
// only under some conditions (an octopus merge leaves no AUTO_MERGE, a single
// pick leaves no sequencer directory) and a concluded operation is a concluded
// operation either way. Every path is attempted even when an earlier one
// fails, and the failures are returned joined, so a partial removal is
// reported in full rather than one path at a time.
//
// Removing the set is the last step of concluding an operation, never a way to
// dispose of one: it drops the state without touching HEAD, the index or the
// working tree, so calling it on an operation that was not actually concluded
// abandons that operation's work silently. For a cherry-pick or revert the set
// includes the sequencer directory, i.e. the whole queue -- concluding one step
// of a sequence natively and then calling Cleanup would strand the remaining
// commits. The caller decides; this function only removes.
//
// KindNone, KindRebase and KindAM are refused: there is no set to remove for
// the first, and safegit does not own the state of the other two.
func Cleanup(gitDir string, k Kind) error {
	paths := Paths(k)
	if len(paths) == 0 {
		switch k {
		case KindNone:
			return errors.New("sequencer: nothing to clean up: no operation in progress")
		case KindRebase, KindAM:
			return fmt.Errorf("sequencer: safegit does not own %s state; it is concluded or abandoned with git's own commands", k)
		}
		return fmt.Errorf("sequencer: no state-file set is declared for %s", k)
	}

	var errs []error
	for _, name := range paths {
		path := filepath.Join(gitDir, name)
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}
