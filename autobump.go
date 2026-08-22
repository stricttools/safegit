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
	// Check the current parent pointer via ls-tree, in the PARENT's work tree:
	// the repository is an argument here, which is the declared
	// explicit-directory exemption from the repository-root pin.
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

	// Parse SHA from ls-tree output: "160000 commit <sha>\t<path>"
	parts := strings.Fields(lsOut.String())
	if len(parts) < 3 {
		return "", fmt.Errorf("unexpected ls-tree output: %q", lsOut.String())
	}
	currentSHA := parts[2]
	if currentSHA == newSubSHA {
		return "", nil // already up to date
	}

	// Defense in depth: maybeAutoBumpParent already returns before reaching
	// this function under --dry-run. Never spawn a real parent commit here.
	if flags.dryRun {
		return "", nil
	}

	// Build commit message
	var subject string
	if firstLine != "" {
		subject = fmt.Sprintf("bump %s: %s", subRelPath, firstLine)
	} else {
		subject = fmt.Sprintf("bump %s", subRelPath)
	}
	msg := trailer.AppendCustom(subject, []string{
		"Triggered-by: " + newSubSHA,
		"Operation: " + operation,
	})

	// Get safegit binary path
	safegitBin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving safegit binary: %v", err)
	}

	// Run safegit commit in the parent through the effects handle, so the
	// self-spawn is a recorded PROC_MUTATE rather than a bare subprocess.
	// `commit` is not consequential, so the child needs no approval flag: it
	// dispatches straight through with no terminal to confirm at.
	completed, err := flags.effects().Run(
		[]interface{}{safegitBin, "commit", "-m", msg, "--", subRelPath},
		strictcli.Cwd(parentWorkTree),
		strictcli.UseGrant("parent-bump"),
		strictcli.Resource("parent-pointer:"+subRelPath),
	)
	if err != nil {
		return "", fmt.Errorf("safegit commit in parent: %v", err)
	}

	// Parse commit SHA from stdout: "[branch sha] message"
	output := strings.TrimSpace(completed.Stdout())
	sha := parseCommitSHA(output)
	if sha == "" {
		return "", fmt.Errorf("could not parse commit SHA from parent output: %q", output)
	}
	return sha, nil
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

// requireAutoBumpDecision refuses, BEFORE anything is committed, when this
// repository is a submodule whose parent has not answered the auto-bump
// question. It is the same refusal maybeAutoBumpParent would reach afterwards,
// moved in front of the commit: reaching it afterwards left the submodule with
// a commit whose parent pointer was never updated and a nonzero exit, which is
// the one outcome nobody asked for.
//
// A dry run validates too. An early refusal is an honest preview -- the real
// run would refuse for exactly this reason -- and it is the only part of the
// parent repository a preview touches: the config is READ, and nothing is
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

	// A dry run must leave the parent repository entirely alone: no safegit
	// directory created there, no config read-modify, and above all no commit.
	// The preview says what the real run would attempt.
	if flags.dryRun {
		if !flags.silent() {
			fmt.Fprintf(os.Stderr, "  parent: would bump %s pointer (dry run; parent repo untouched)\n", subRelPath)
		}
		return nil
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

	// Check autoBumpParent setting. commit, amend and reword have already
	// refused before committing anything when it is absent; undo, which can only
	// discover it after the rollback it is undoing, reaches it here.
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
