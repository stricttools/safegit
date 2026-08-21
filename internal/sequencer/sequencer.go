// Package sequencer is the single authority on the state git writes into the
// git directory while an operation is in flight -- a conflicted merge, a
// cherry-pick or revert (single or queued), a rebase, or a mailbox
// application.
//
// The package does two things and nothing else: it REPORTS the state, and it
// CLEARS the state of the operations safegit owns. It holds no policy. It does
// not decide whether an in-flight operation should block a commit, which
// command an operator ought to run next, or whether a conclusion is legal --
// those decisions belong to the refusal checks and the conclusion engine that
// call it.
//
// Read is deliberately filesystem-only: it never starts a git subprocess, so
// the refusal checks on safegit's hot paths pay a handful of stat calls rather
// than a fork. The one fact that is not on disk -- the identity recorded on the
// commit a cherry-pick or revert is applying -- is resolved on demand by
// SourceAuthor, which is the only function here that runs git (through
// internal/git, safegit's git boundary).
//
// The git directory always arrives as a parameter. The package never discovers
// it, which is what makes it correct in a linked worktree: pass the worktree's
// own git directory (.git/worktrees/<name>) and every state file resolves
// against it, because git writes all of these per worktree.
package sequencer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The state files and directories git writes, named relative to the git
// directory. These names are the vocabulary of the whole package: Read probes
// them, Paths groups them per operation, and Cleanup removes them.
const (
	FileMergeHead      = "MERGE_HEAD"
	FileMergeMode      = "MERGE_MODE"
	FileMergeMsg       = "MERGE_MSG"
	FileCherryPickHead = "CHERRY_PICK_HEAD"
	FileRevertHead     = "REVERT_HEAD"
	FileAutoMerge      = "AUTO_MERGE"
	DirSequencer       = "sequencer"
	DirRebaseMerge     = "rebase-merge"
	DirRebaseApply     = "rebase-apply"
)

// Kind names the operation in flight.
type Kind int

const (
	// KindNone means no operation is in flight.
	KindNone Kind = iota
	// KindMerge is a merge stopped before its commit: .git/MERGE_HEAD exists.
	// An octopus merge is still one KindMerge; MergeHeads then carries several
	// commits.
	KindMerge
	// KindCherryPick is a cherry-pick stopped before its commit, single or
	// queued. Queued distinguishes the two.
	KindCherryPick
	// KindRevert is a revert stopped before its commit, single or queued.
	KindRevert
	// KindRebase is a rebase in progress, under either backend. Backend says
	// which.
	KindRebase
	// KindAM is a `git am` in progress. It shares the rebase-apply directory
	// with the apply-backend rebase and is told apart by the marker file git
	// writes there, so reporting it as a rebase would be a factual error and a
	// refusal message derived from it would name the wrong way out.
	KindAM
)

// String returns the operation's name in git's own vocabulary.
func (k Kind) String() string {
	switch k {
	case KindNone:
		return "none"
	case KindMerge:
		return "merge"
	case KindCherryPick:
		return "cherry-pick"
	case KindRevert:
		return "revert"
	case KindRebase:
		return "rebase"
	case KindAM:
		return "git am"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Backend names which of git's two rebase implementations is running.
type Backend int

const (
	// BackendNone is the value on every state that is not a rebase.
	BackendNone Backend = iota
	// BackendMerge is the merge backend, which keeps its state in
	// .git/rebase-merge. It is git's default and the backend every
	// interactive rebase uses.
	BackendMerge
	// BackendApply is the apply backend, which keeps its state in
	// .git/rebase-apply. `git rebase --apply` selects it.
	BackendApply
)

// String returns the backend's name as git's own documentation spells it.
func (b Backend) String() string {
	switch b {
	case BackendNone:
		return "none"
	case BackendMerge:
		return "merge"
	case BackendApply:
		return "apply"
	}
	return fmt.Sprintf("Backend(%d)", int(b))
}

// Dir returns the git-directory-relative directory the backend keeps its state
// in, or "" for BackendNone.
func (b Backend) Dir() string {
	switch b {
	case BackendMerge:
		return DirRebaseMerge
	case BackendApply:
		return DirRebaseApply
	}
	return ""
}

// State is one reading of the git directory: what is in flight, and the facts
// about it that live in the state files.
//
// Fields not relevant to Kind hold their zero values. A caller reads Kind
// first and only then the fields that kind populates.
type State struct {
	// Kind is the operation in flight, KindNone when there is none.
	Kind Kind

	// MergeHeads carries every line of MERGE_HEAD, in file order (KindMerge).
	// An ordinary merge has one entry; an octopus merge has one per merged
	// branch, and a conclusion's parents are HEAD followed by all of them.
	MergeHeads []string

	// Source is the commit the operation is applying: CHERRY_PICK_HEAD for
	// KindCherryPick, REVERT_HEAD for KindRevert. It is empty on a queued
	// sequence that is between steps -- the queue exists but git is not
	// stopped on any one commit.
	Source string

	// Queued reports that git's sequencer holds a queue of commands for this
	// operation, i.e. .git/sequencer exists (KindCherryPick, KindRevert). It
	// is the discriminator between `git cherry-pick <one>`, which writes
	// CHERRY_PICK_HEAD and AUTO_MERGE and no sequencer directory, and
	// `git cherry-pick <a> <b>`, which additionally writes the queue.
	//
	// It is a fact about the state, not a verdict: concluding one step of a
	// queue natively would strand the rest, but deciding that is the caller's.
	Queued bool

	// Backend is which rebase implementation is running (KindRebase, KindAM).
	Backend Backend

	// HeadName is the full ref name of the branch being rebased, as recorded
	// in the backend directory's head-name file (KindRebase). It is empty when
	// the rebase started from a detached HEAD, and on KindAM.
	HeadName string

	// MessageFile is the absolute path of the file holding the message a
	// conclusion would start from, or "" when the operation has none on disk.
	// It is MERGE_MSG for a merge, cherry-pick and revert; rebase-merge/message
	// or rebase-apply/final-commit for a rebase. The file's content is git's
	// verbatim draft: it still carries the "# Conflicts:" comment block, which
	// a conclusion strips.
	MessageFile string
}

// InProgress reports whether any operation is in flight.
func (s State) InProgress() bool { return s.Kind != KindNone }

// String describes the state in one factual phrase, suitable for embedding in
// a caller's message. It states what is in flight, never what to do about it.
func (s State) String() string {
	switch s.Kind {
	case KindNone:
		return "no operation in progress"
	case KindMerge:
		if len(s.MergeHeads) > 1 {
			return fmt.Sprintf("an octopus merge of %d commits", len(s.MergeHeads))
		}
		if len(s.MergeHeads) == 1 {
			return fmt.Sprintf("a merge of %s", abbrev(s.MergeHeads[0]))
		}
		return "a merge"
	case KindCherryPick, KindRevert:
		what := "a cherry-pick"
		if s.Kind == KindRevert {
			what = "a revert"
		}
		switch {
		case s.Queued && s.Source != "":
			return fmt.Sprintf("%s sequence stopped at %s", what, abbrev(s.Source))
		case s.Queued:
			return what + " sequence between steps"
		case s.Source != "":
			return fmt.Sprintf("%s of %s", what, abbrev(s.Source))
		}
		return what
	case KindRebase:
		if s.HeadName != "" {
			return fmt.Sprintf("a rebase of %s (%s backend)", s.HeadName, s.Backend)
		}
		return fmt.Sprintf("a rebase (%s backend)", s.Backend)
	case KindAM:
		return "a git am"
	}
	return s.Kind.String()
}

// abbrev shortens a full object name for a message, leaving anything that is
// not one alone.
func abbrev(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// Read reports the operation in flight in the repository whose git directory is
// gitDir. A repository with nothing in flight yields a State with Kind KindNone
// and a nil error; an error means the git directory could not be read or holds
// a state file git could not have written.
//
// It never starts a git subprocess.
//
// When several operations' markers are present at once -- which git does not
// normally produce, but a crashed process or a stray file can leave behind --
// the probe order decides, and it is git's own: merge, then rebase (merge
// backend before apply backend), then cherry-pick, then revert, then a
// sequencer queue with no current step. Reporting the first match rather than
// erroring keeps every caller's behavior deterministic; a caller that considers
// leftovers an error (a conclusion refusing to run against residue) checks for
// them itself.
func Read(gitDir string) (State, error) {
	mergeHeads, err := readSHALines(filepath.Join(gitDir, FileMergeHead))
	if err != nil {
		return State{}, err
	}
	if len(mergeHeads) > 0 {
		return State{
			Kind:        KindMerge,
			MergeHeads:  mergeHeads,
			MessageFile: existingFile(gitDir, FileMergeMsg),
		}, nil
	}

	for _, backend := range []Backend{BackendMerge, BackendApply} {
		dir := filepath.Join(gitDir, backend.Dir())
		ok, err := isDir(dir)
		if err != nil {
			return State{}, err
		}
		if !ok {
			continue
		}
		return readRebase(gitDir, backend)
	}

	queued, err := isDir(filepath.Join(gitDir, DirSequencer))
	if err != nil {
		return State{}, err
	}

	for _, probe := range []struct {
		kind Kind
		file string
	}{
		{KindCherryPick, FileCherryPickHead},
		{KindRevert, FileRevertHead},
	} {
		source, err := readSHA(filepath.Join(gitDir, probe.file))
		if err != nil {
			return State{}, err
		}
		if source == "" {
			continue
		}
		return State{
			Kind:        probe.kind,
			Source:      source,
			Queued:      queued,
			MessageFile: existingFile(gitDir, FileMergeMsg),
		}, nil
	}

	// A queue with no current step: git is mid-sequence but not stopped on a
	// commit, which is what an operator's own `git commit` mid-sequence leaves
	// behind. The todo file's first command says which operation the queue is.
	if queued {
		kind, err := queuedKind(gitDir)
		if err != nil {
			return State{}, err
		}
		if kind != KindNone {
			return State{
				Kind:        kind,
				Queued:      true,
				MessageFile: existingFile(gitDir, FileMergeMsg),
			}, nil
		}
	}

	return State{Kind: KindNone}, nil
}

// readRebase fills in the facts a rebase (or a mailbox application, which
// shares the apply backend's directory) records in its backend directory.
func readRebase(gitDir string, backend Backend) (State, error) {
	dir := filepath.Join(gitDir, backend.Dir())

	kind := KindRebase
	if backend == BackendApply {
		// git tells its two users of this directory apart by a marker file:
		// `rebasing` for a rebase, `applying` for `git am`. A directory with
		// neither is `git am`'s older spelling, so rebasing is the positive
		// test and everything else is an application.
		rebasing, err := exists(filepath.Join(dir, "rebasing"))
		if err != nil {
			return State{}, err
		}
		if !rebasing {
			kind = KindAM
		}
	}

	s := State{Kind: kind, Backend: backend}

	if kind == KindRebase {
		headName, err := readLine(filepath.Join(dir, "head-name"))
		if err != nil {
			return State{}, err
		}
		// A rebase started from a detached HEAD records the literal
		// "detached HEAD" here rather than a ref, which is not a branch name
		// and must not be reported as one.
		if headName != "detached HEAD" {
			s.HeadName = headName
		}
	}

	messageFile := "message"
	if backend == BackendApply {
		messageFile = "final-commit"
	}
	s.MessageFile = existingFile(dir, messageFile)

	return s, nil
}

// queuedKind reads the first command of the sequencer's todo file to tell a
// cherry-pick queue from a revert queue. An empty or absent todo yields
// KindNone: the directory is a leftover, not an operation.
func queuedKind(gitDir string) (Kind, error) {
	path := filepath.Join(gitDir, DirSequencer, "todo")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return KindNone, nil
		}
		return KindNone, fmt.Errorf("reading %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, _, _ := strings.Cut(line, " ")
		switch verb {
		case "pick", "p":
			return KindCherryPick, nil
		case "revert":
			return KindRevert, nil
		default:
			return KindNone, fmt.Errorf("%s: unrecognized sequencer command %q", path, verb)
		}
	}
	return KindNone, nil
}

// readSHALines reads a state file holding one object name per line, returning
// nil when the file does not exist. A line that is not an object name is an
// error: the file is git's, and content git could not have written means
// something else wrote it.
func readSHALines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var shas []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !isObjectName(line) {
			return nil, fmt.Errorf("%s: %q is not an object name", path, line)
		}
		shas = append(shas, line)
	}
	if len(shas) == 0 && len(data) > 0 {
		return nil, fmt.Errorf("%s: holds no object name", path)
	}
	return shas, nil
}

// readSHA reads a state file holding exactly one object name, returning "" when
// the file does not exist.
func readSHA(path string) (string, error) {
	shas, err := readSHALines(path)
	if err != nil {
		return "", err
	}
	switch len(shas) {
	case 0:
		return "", nil
	case 1:
		return shas[0], nil
	}
	return "", fmt.Errorf("%s: holds %d object names, want 1", path, len(shas))
}

// readLine reads a single-line state file, trimmed, returning "" when the file
// does not exist.
func readLine(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// isObjectName reports whether s is a full object name in either hash
// algorithm: 40 lowercase hex digits for SHA-1, 64 for SHA-256.
func isObjectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// existingFile returns the absolute path of dir/name when it exists as a
// regular file, and "" otherwise. A file that cannot be stat'ed is reported as
// absent rather than as an error: MessageFile is an optional convenience, and a
// caller that needs the message opens the path and reports its own failure.
func existingFile(dir, name string) string {
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return path
}

// exists reports whether path exists, distinguishing "not there" from "could
// not tell".
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", path, err)
}

// isDir reports whether path exists and is a directory.
func isDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", path, err)
}
