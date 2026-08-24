package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// autoBumpParent performs the actual parent bump: checks the current pointer,
// builds a commit message, and runs safegit commit in the parent repo.
// Returns the new commit SHA (or "" if no bump was needed) and any error.
func autoBumpParent(ctx context.Context, flags globalFlags, parentWorkTree, subRelPath, newSubSHA, operation, firstLine string) (string, error) {
	currentSHA, err := parentGitlinkSHA(ctx, parentWorkTree, subRelPath)
	if err != nil {
		return "", err
	}
	if currentSHA == newSubSHA {
		return "", nil // already up to date
	}

	// Defense in depth: maybeAutoBumpParent already returns before reaching
	// this function under --dry-run. Never spawn a real parent commit here.
	if flags.dryRun {
		return "", nil
	}

	completed, err := runParentBumpCommit(flags, parentWorkTree, subRelPath,
		parentBumpMessage(subRelPath, firstLine, newSubSHA, operation))
	if err != nil {
		return "", err
	}

	// Parse commit SHA from stdout: "[branch sha] message"
	output := strings.TrimSpace(completed.Stdout())
	sha := parseCommitSHA(output)
	if sha == "" {
		return "", fmt.Errorf("could not parse commit SHA from parent output: %q", output)
	}
	return sha, nil
}

// parentGitlinkSHA reads the gitlink a PARENT repository records for one of its
// submodules -- the object name the bump would replace.
//
// It runs in the PARENT's work tree: the repository is an argument here, which
// is the declared explicit-directory exemption from the repository-root pin. It
// is a READ, so a preview may make it too (see planParentBump).
func parentGitlinkSHA(ctx context.Context, parentWorkTree, subRelPath string) (string, error) {
	var lsOut, lsErr bytes.Buffer
	lsCmd, err := gitexec.Command(ctx, gitexec.Spec{
		Args:   []string{"ls-tree", "--full-tree", "HEAD", subRelPath},
		Exempt: gitexec.ExemptAutoBumpParentPointer,
		Dir:    parentWorkTree,
	})
	if err != nil {
		return "", err
	}
	lsCmd.Stdout = &lsOut
	lsCmd.Stderr = &lsErr
	if err := lsCmd.Run(); err != nil {
		return "", fmt.Errorf("ls-tree in parent: %v (%s)", err, strings.TrimSpace(lsErr.String()))
	}

	// "160000 commit <sha>\t<path>"
	parts := strings.Fields(lsOut.String())
	if len(parts) < 3 {
		return "", fmt.Errorf("unexpected ls-tree output: %q", lsOut.String())
	}
	return parts[2], nil
}

// parentBumpMessage composes the parent commit's message. One builder for both
// modes, so the argv a preview RECORDS is the argv the execute path RUNS, down
// to the trailers -- except for triggeredBy, which a preview cannot know when
// the sub's own commit does not exist yet and which is the placeholder there.
func parentBumpMessage(subRelPath, firstLine, triggeredBy, operation string) string {
	subject := fmt.Sprintf("bump %s", subRelPath)
	if firstLine != "" {
		subject = fmt.Sprintf("bump %s: %s", subRelPath, firstLine)
	}
	return trailer.AppendCustom(subject, []string{
		"Triggered-by: " + triggeredBy,
		"Operation: " + operation,
	})
}

// runParentBumpCommit mints the parent's own commit through the effects handle,
// so the self-spawn is a recorded PROC_MUTATE rather than a bare subprocess --
// performed on an executing run, recorded instead of performed in a preview.
// `commit` is not consequential, so the child needs no approval flag: it
// dispatches straight through with no terminal to confirm at.
func runParentBumpCommit(flags globalFlags, parentWorkTree, subRelPath, msg string) (strictcli.Completed, error) {
	safegitBin, err := os.Executable()
	if err != nil {
		return strictcli.Completed{}, fmt.Errorf("resolving safegit binary: %v", err)
	}
	completed, err := flags.effects().Run(
		[]interface{}{safegitBin, "commit", "-m", msg, "--", subRelPath},
		strictcli.Cwd(parentWorkTree),
		strictcli.UseGrant("parent-bump"),
		strictcli.Resource("parent-pointer:"+subRelPath),
	)
	if err != nil {
		return strictcli.Completed{}, fmt.Errorf("safegit commit in parent: %v", err)
	}
	return completed, nil
}

// parentBumpPlan is a preview's answer to "would this run commit in the parent,
// and with what argv". Every READ the decision needs is made before it exists,
// which is what lets a caller record the ref move first and the bump second
// without a state read in between (the dry-run doctrine).
type parentBumpPlan struct {
	parentWorkTree string
	subRelPath     string
}

// planParentBump makes the parent-bump decision from reads alone: the parent
// config (the same read requireAutoBumpDecision's dry branch makes), the nested
// check, and the gitlink the bump would replace. It returns nil when no bump
// would happen at all -- not a submodule, the key deliberately says no, or the
// gitlink already names newHeadSHA -- and an error for the conditions the real
// run refuses on, so a preview never promises a bump the real run would not
// make.
//
// It creates nothing in the parent: no safegit directory, no config write.
func planParentBump(ctx context.Context, flags globalFlags, newHeadSHA string) (*parentBumpPlan, error) {
	parent, ok := submodule.DetectParent(ctx)
	if !ok {
		return nil, nil
	}
	parentGitDir, subRelPath := parent.GitDir, parent.SubmodulePath

	cfg, err := repo.LoadConfig(parentGitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errAutoBumpUnset
	}
	if err != nil {
		return nil, fmt.Errorf("loading parent config: %v", err)
	}
	if cfg.Commit.AutoBumpParent == nil {
		return nil, errAutoBumpUnset
	}
	if !*cfg.Commit.AutoBumpParent {
		return nil, nil // explicitly disabled
	}

	if err := submodule.CheckNested(ctx, parentGitDir); err != nil {
		return nil, fmt.Errorf("nested submodules detected — set commit.autoBumpParent to false in the parent")
	}

	parentWorkTree := filepath.Dir(parentGitDir)
	currentSHA, err := parentGitlinkSHA(ctx, parentWorkTree, subRelPath)
	if err != nil {
		return nil, err
	}
	if currentSHA == newHeadSHA {
		// Already current: the real run makes no commit here, so the preview
		// records none either.
		return nil, nil
	}
	return &parentBumpPlan{parentWorkTree: parentWorkTree, subRelPath: subRelPath}, nil
}

// recordParentBumpPreview mints the would-do record for the parent's own commit.
// The argv is the one the execute path runs, down to the trailers.
//
// triggeredBy is the object name that goes in the Triggered-by trailer, and it
// is the CALLER's to supply because only the caller knows whether a preview can
// name it. A caller that is about to AUTHOR the sub's commit cannot -- the object
// a real run builds carries the committer timestamp, so no preview can name it --
// and passes previewCommitPlaceholder. undo can: the commit it rolls the branch
// back to is in hand, read off the op log, and it is exactly what the execute
// path writes there. A placeholder in its place would be a preview hiding a value
// it holds.
func recordParentBumpPreview(flags globalFlags, plan *parentBumpPlan, triggeredBy, operation, firstLine string) error {
	_, err := runParentBumpCommit(flags, plan.parentWorkTree, plan.subRelPath,
		parentBumpMessage(plan.subRelPath, firstLine, triggeredBy, operation))
	return err
}

// previewAutoBumpParent is the parent bump's whole PREVIEW half: the decision,
// made from reads alone; the would-do record, minted with the Triggered-by value
// the CALLER supplies; and the notice. It is the single site for all three, so a
// caller that knows the real object name differs from one that does not in that
// value and in nothing else.
//
// Two callers know it. maybeAutoBumpParent's dry branch does not -- every route
// through it is about to author the sub's commit -- and passes the placeholder.
// The crash re-stand path does: the conclusion's commit is already on the branch,
// read off it before this is called, and it is exactly what the execute path
// writes into the parent's message. (undo also knows it, and arranges the two
// halves itself so its records come out in execution order -- see runUndo.)
func previewAutoBumpParent(ctx context.Context, flags globalFlags, newHeadSHA, triggeredBy, operation, firstLineMsg string) error {
	plan, err := planParentBump(ctx, flags, newHeadSHA)
	if err != nil {
		return err
	}
	if plan == nil {
		// No bump would happen: the key says no, or the gitlink already names
		// this SHA. Nothing to record and nothing to announce.
		return nil
	}
	if err := recordParentBumpPreview(flags, plan, triggeredBy, operation, firstLineMsg); err != nil {
		return err
	}
	if !flags.silent() {
		fmt.Fprintf(os.Stderr, "  parent: would bump %s pointer (dry run; parent repo untouched)\n", plan.subRelPath)
	}
	return nil
}

// parseCommitSHA extracts the SHA from safegit commit output.
// Format: "[branch sha] message"
func parseCommitSHA(output string) string {
	// Find first line
	line := output
	if i := strings.IndexByte(output, '\n'); i >= 0 {
		line = output[:i]
	}
	// Expected: "[branchname abcd1234] some message"
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	closeBracket := strings.IndexByte(line, ']')
	if closeBracket < 0 {
		return ""
	}
	inside := line[1:closeBracket] // "branchname abcd1234"
	parts := strings.Fields(inside)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// errAutoBumpUnset is what a parent repository that has not answered the
// auto-bump question produces. The key's PRESENCE is what is mandatory: `true`
// means bump, `false` means deliberately do not, and absent means nobody has
// decided -- which safegit refuses rather than guessing at, because either
// guess is wrong in somebody's repository.
var errAutoBumpUnset = fmt.Errorf("commit.autoBumpParent not configured in parent repo — run: safegit config set commit.autoBumpParent true (in the parent)")

// requireAutoBumpDecision refuses, BEFORE any ref moves, when this repository
// is a submodule whose parent has not answered the auto-bump question. It is the
// same refusal maybeAutoBumpParent would reach afterwards, moved in front of the
// operation: reaching it afterwards left the submodule with a branch that had
// moved and a parent pointer that had not, plus a nonzero exit, which is the one
// outcome nobody asked for.
//
// Every commit-family route calls it -- the authors that make a commit and undo,
// which takes one back. undo is not an exception to the ordering: the gitlink it
// leaves stale is stale in exactly the same way.
//
// A dry run validates too. An early refusal is an honest preview -- the real
// run would refuse for exactly this reason -- and everything a preview does in
// the parent repository is a READ: this config read, and the nested check plus
// the gitlink read the bump PREVIEW makes later (planParentBump). Nothing is
// created there, not even safegit's own directory.
func requireAutoBumpDecision(ctx context.Context, flags globalFlags) error {
	parent, ok := submodule.DetectParent(ctx)
	if !ok {
		return nil // not in a submodule
	}
	parentGitDir := parent.GitDir

	if flags.dryRun {
		// No ensureInitialized: a preview creates nothing in the parent. A
		// parent with no config at all has not answered the question either --
		// it is what the real run finds after initializing the parent with
		// defaults, where the key is absent.
		cfg, err := repo.LoadConfig(parentGitDir)
		if errors.Is(err, fs.ErrNotExist) {
			return errAutoBumpUnset
		}
		if err != nil {
			return fmt.Errorf("loading parent config: %v", err)
		}
		if cfg.Commit.AutoBumpParent == nil {
			return errAutoBumpUnset
		}
		return nil
	}

	if err := ensureInitialized(flags, parentGitDir); err != nil {
		return fmt.Errorf("initializing parent safegit: %v", err)
	}
	cfg, err := repo.LoadConfig(parentGitDir)
	if err != nil {
		return fmt.Errorf("loading parent config: %v", err)
	}
	if cfg.Commit.AutoBumpParent == nil {
		return errAutoBumpUnset
	}
	return nil
}

// maybeAutoBumpParent checks config and conditions, then bumps the parent
// submodule pointer if appropriate.
func maybeAutoBumpParent(ctx context.Context, flags globalFlags, gitDir, newHeadSHA, operation, firstLineMsg string) error {
	parent, ok := submodule.DetectParent(ctx)
	if !ok {
		return nil // not in a submodule
	}
	parentGitDir, subRelPath := parent.GitDir, parent.SubmodulePath

	// A dry run WRITES nothing in the parent repository: no safegit directory
	// created there, no config modified, and above all no commit. It READS it --
	// the config, the nested check, the gitlink -- because that is what deciding
	// whether a bump would happen takes, and a preview that skipped the decision
	// could only describe a bump it had not established would occur.
	//
	// This is the ONE mint site for the parent bump, in both modes, so every
	// caller inherits it: commit, amend, reword, mv, the pipeline-authored
	// merge, pull, cherry-pick and revert, the three conclusions, and undo --
	// which arranges the two halves itself so its records come out in execution
	// order (see runUndo).
	if flags.dryRun {
		// Every route through here is about to author the sub's own commit, which
		// no preview can name: the placeholder is the honest value. The callers
		// that hold the real one -- undo, and the crash re-stand path, where the
		// commit already exists -- record their bump through
		// previewAutoBumpParent themselves.
		return previewAutoBumpParent(ctx, flags, newHeadSHA, previewCommitPlaceholder, operation, firstLineMsg)
	}

	// Ensure parent's safegit dir exists
	if err := ensureInitialized(flags, parentGitDir); err != nil {
		return fmt.Errorf("initializing parent safegit: %v", err)
	}

	// Load parent config
	cfg, err := repo.LoadConfig(parentGitDir)
	if err != nil {
		return fmt.Errorf("loading parent config: %v", err)
	}

	// Check autoBumpParent setting. Every route that reaches this function --
	// commit, amend, reword, mv, the pipeline-authored merge, pull, cherry-pick
	// and revert, the three conclusions, and undo -- has already refused through
	// requireAutoBumpDecision when the key is absent, so this arm is the
	// last-resort one: it fires only when the parent's answer disappeared between
	// that refusal and this bump.
	if cfg.Commit.AutoBumpParent == nil {
		return errAutoBumpUnset
	}
	if !*cfg.Commit.AutoBumpParent {
		return nil // explicitly disabled
	}

	// Check for nested submodules
	if err := submodule.CheckNested(ctx, parentGitDir); err != nil {
		return fmt.Errorf("nested submodules detected — set commit.autoBumpParent to false in the parent")
	}

	// Determine parent work tree from parentGitDir
	parentWorkTree := filepath.Dir(parentGitDir)

	// Perform the bump
	sha, err := autoBumpParent(ctx, flags, parentWorkTree, subRelPath, newHeadSHA, operation, firstLineMsg)
	if err != nil {
		return err
	}

	if sha != "" {
		// Log to the sub's oplog
		sgDir := repo.SafegitDir(gitDir)
		_ = oplog.Append(sgDir, oplog.Entry{
			Op: "auto-bump-parent",
			Extra: map[string]interface{}{
				"parentBumpSHA": sha,
				"parentDir":     parentWorkTree,
			},
		})

		if !flags.silent() {
			fmt.Fprintf(os.Stderr, "  parent: bumped %s pointer (%s)\n", subRelPath, sha[:8])
		}
	}

	return nil
}
