// Native git hook execution for the commit family: pre-commit, commit-msg and
// post-commit, run from .git/hooks exactly as git runs them.
package commit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// The three hooks git runs around a commit that safegit runs too.
// prepare-commit-msg is deliberately absent: safegit never opens an editor, so
// there is no message-preparation step for a hook to prepare.
const (
	hookPreCommit  = "pre-commit"
	hookCommitMsg  = "commit-msg"
	hookPostCommit = "post-commit"
)

// nativeHooks runs a repository's own git hooks around ONE commit-family
// operation -- a commit, an amend or a reword.
//
// One instance per operation, not per attempt. The compare-and-swap loop can
// run the staging and object-building phase several times when another session
// moves the ref underneath it, and a hook that ran once per attempt would lint
// the same message twice, mail the same notification twice, or (for a
// commit-msg hook that rewrites) rewrite its own rewrite. So pre-commit and
// commit-msg each run at most once here and every later attempt reuses the
// answer, which is also what git does: one commit, one run.
//
// Hook output goes to stderr, whatever the hook writes it to. safegit's stdout
// is a structured channel -- one JSON envelope in machine mode, a parseable
// commit line otherwise -- and an operator-supplied script must not be able to
// write into it.
type nativeHooks struct {
	gitDir     string
	repoRoot   string
	safegitDir string

	// skip is a dry run: no hook runs at all, because a hook is an arbitrary
	// script whose effects safegit cannot record, undo, or preview.
	skip bool

	preCommitDone bool

	msgDone bool
	message string

	// scratch is the directory the commit-msg message file is written in,
	// created on first use and removed by cleanup.
	scratch string
}

// newNativeHooks prepares hook execution for one operation. Under a dry run it
// prepares nothing and says so, naming the hooks the real run would have run.
func newNativeHooks(ctx context.Context, repoRoot, safegitDir string, dryRun bool) (*nativeHooks, error) {
	gitDir, err := git.GitDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving git dir: %w", err)
	}
	h := &nativeHooks{gitDir: gitDir, repoRoot: repoRoot, safegitDir: safegitDir, skip: dryRun}
	if dryRun {
		h.noteSkipped()
	}
	return h, nil
}

// noteSkipped tells the operator which of their hooks a preview did not run.
// Silent when the repository has none, which is the common case.
func (h *nativeHooks) noteSkipped() {
	var present []string
	for _, name := range []string{hookPreCommit, hookCommitMsg, hookPostCommit} {
		if h.path(name) != "" {
			present = append(present, name)
		}
	}
	if len(present) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "  hooks: %s not run (dry run)\n", strings.Join(present, ", "))
}

// path returns the executable hook of that name, or "" when the repository has
// no such hook or the file is not executable (which is git's own rule for
// ignoring it).
func (h *nativeHooks) path(name string) string {
	p := filepath.Join(h.gitDir, "hooks", name)
	info, err := os.Stat(p)
	if err != nil || info.Mode()&0111 == 0 {
		return ""
	}
	return p
}

// wantsIndex reports whether a hook that inspects staged content -- pre-commit
// or commit-msg -- still has to run. It is how reword, which stages nothing of
// its own, decides whether materializing an index for the hooks to read is
// worth doing at all.
func (h *nativeHooks) wantsIndex() bool {
	if h.skip {
		return false
	}
	if !h.preCommitDone && h.path(hookPreCommit) != "" {
		return true
	}
	if !h.msgDone && h.path(hookCommitMsg) != "" {
		return true
	}
	return false
}

// run executes one hook to completion with its output on stderr, returning the
// hook's own error when it exits nonzero.
//
// The environment is the process's own plus GIT_INDEX_FILE, and deliberately
// carries no object quarantine: no hook ever runs in a preview (skip is set
// from dryRun, and every entry point returns early on it). Were that to change,
// a hook's own git calls would write objects into the repository for real --
// gitexec's boundary sees only the subprocesses safegit itself constructs, and
// a hook is an operator-supplied script whose children it can neither classify
// nor reach.
func (h *nativeHooks) run(ctx context.Context, hookPath, indexPath string, args ...string) error {
	cmd := exec.CommandContext(ctx, hookPath, args...)
	cmd.Dir = h.repoRoot
	cmd.Env = os.Environ()
	if indexPath != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+indexPath)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// preCommit runs the pre-commit hook against the staged content in indexPath.
// A nonzero exit aborts the operation. Runs at most once per operation.
func (h *nativeHooks) preCommit(ctx context.Context, indexPath string) error {
	if h.skip || h.preCommitDone {
		return nil
	}
	h.preCommitDone = true

	hookPath := h.path(hookPreCommit)
	if hookPath == "" {
		return nil
	}
	if err := h.run(ctx, hookPath, indexPath); err != nil {
		return &CommitError{
			Code:    exitcode.CommitHookRejected,
			Message: fmt.Sprintf("pre-commit hook failed: %v", err),
			Err:     err,
		}
	}
	return nil
}

// commitMsg runs the commit-msg hook on the caller's composed message and
// returns the message the commit is actually built from -- which is the file's
// content afterwards, because a commit-msg hook is allowed to rewrite it in
// place (that is how "Signed-off-by" and issue-key hooks work). A nonzero exit
// aborts the operation with the message uncommitted.
//
// The message it sees is the user's: their -m text and any trailers they asked
// for. safegit's own session trailer is injected afterwards by the caller, so
// a hook can neither be confused by it nor strip it.
//
// Runs at most once per operation; later compare-and-swap attempts get the same
// message back.
func (h *nativeHooks) commitMsg(ctx context.Context, indexPath, composed string) (string, error) {
	if h.skip {
		return composed, nil
	}
	if h.msgDone {
		return h.message, nil
	}

	hookPath := h.path(hookCommitMsg)
	if hookPath == "" {
		h.msgDone, h.message = true, composed
		return composed, nil
	}

	msgPath, err := h.messageFile(composed)
	if err != nil {
		return "", err
	}

	if err := h.run(ctx, hookPath, indexPath, msgPath); err != nil {
		return "", &CommitError{
			Code:    exitcode.CommitHookRejected,
			Message: fmt.Sprintf("commit-msg hook rejected the message: %v", err),
			Err:     err,
		}
	}

	data, err := os.ReadFile(msgPath)
	if err != nil {
		return "", fmt.Errorf("re-reading the message the commit-msg hook left behind: %w", err)
	}

	h.msgDone = true
	h.message = strings.TrimRight(string(data), "\n")
	return h.message, nil
}

// messageFile writes the composed message where the commit-msg hook can edit
// it and returns that path. The file is per-invocation rather than git's shared
// .git/COMMIT_EDITMSG, for the same reason the index is: two safegit runs in
// one repository must not be able to hand each other's message to a hook.
func (h *nativeHooks) messageFile(composed string) (string, error) {
	if h.scratch == "" {
		base := filepath.Join(h.safegitDir, "tmp")
		if err := os.MkdirAll(base, 0755); err != nil {
			return "", fmt.Errorf("creating hook scratch dir: %w", err)
		}
		// <pid>-<random>, the naming the tmp-directory garbage collector reads,
		// so a crash between here and cleanup leaves nothing permanent.
		dir, err := os.MkdirTemp(base, fmt.Sprintf("%d-", os.Getpid()))
		if err != nil {
			return "", fmt.Errorf("creating hook scratch dir: %w", err)
		}
		h.scratch = dir
	}

	path := filepath.Join(h.scratch, "COMMIT_EDITMSG")
	if err := os.WriteFile(path, []byte(strings.TrimRight(composed, "\n")+"\n"), 0644); err != nil {
		return "", fmt.Errorf("writing the commit message for the commit-msg hook: %w", err)
	}
	return path, nil
}

// postCommit runs the post-commit hook after the ref has moved. Its exit status
// is deliberately ignored: the commit already exists, and nothing a notifier
// script decides can un-create it.
func (h *nativeHooks) postCommit(ctx context.Context) {
	if h.skip {
		return
	}
	hookPath := h.path(hookPostCommit)
	if hookPath == "" {
		return
	}
	_ = h.run(ctx, hookPath, "")
}

// cleanup removes whatever the hooks needed on disk.
func (h *nativeHooks) cleanup() {
	if h.scratch != "" {
		os.RemoveAll(h.scratch)
		h.scratch = ""
	}
}
