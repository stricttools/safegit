package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// autoBumpParent performs the actual parent bump: checks the current pointer,
// builds a commit message, and runs safegit commit in the parent repo.
// Returns the new commit SHA (or "" if no bump was needed) and any error.
func autoBumpParent(flags globalFlags, parentWorkTree, subRelPath, newSubSHA, operation, firstLine string) (string, error) {
	// Check current parent pointer via ls-tree
	var lsOut, lsErr bytes.Buffer
	lsCmd := exec.Command("git", "--no-optional-locks", "ls-tree", "HEAD", subRelPath)
	lsCmd.Dir = parentWorkTree
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
	// --yes is mandatory: the child is a mutating command dispatched with no
	// terminal to confirm at, and the operator already consented to this run.
	completed, err := flags.effects().Run(
		[]interface{}{safegitBin, "--yes", "commit", "-m", msg, "--", subRelPath},
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

// maybeAutoBumpParent checks config and conditions, then bumps the parent
// submodule pointer if appropriate.
func maybeAutoBumpParent(ctx context.Context, flags globalFlags, gitDir, newHeadSHA, operation, firstLineMsg string) error {
	parentGitDir, subRelPath, ok := submodule.DetectParent(ctx)
	if !ok {
		return nil // not in a submodule
	}

	// A dry run must leave the parent repository entirely alone: no safegit
	// directory created there, no config read-modify, and above all no commit.
	// The preview says what the real run would attempt.
	if flags.dryRun {
		if !flags.quiet {
			fmt.Fprintf(os.Stderr, "  parent: would bump %s pointer (dry run; parent repo untouched)\n", subRelPath)
		}
		return nil
	}

	// Ensure parent's safegit dir exists
	if err := repo.EnsureInitialized(parentGitDir); err != nil {
		return fmt.Errorf("initializing parent safegit: %v", err)
	}

	// Load parent config
	cfg, err := repo.LoadConfig(parentGitDir)
	if err != nil {
		return fmt.Errorf("loading parent config: %v", err)
	}

	// Check autoBumpParent setting
	if cfg.Commit.AutoBumpParent == nil {
		return fmt.Errorf("commit.autoBumpParent not configured in parent repo — run: safegit config set commit.autoBumpParent true (in the parent)")
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
	sha, err := autoBumpParent(flags, parentWorkTree, subRelPath, newHeadSHA, operation, firstLineMsg)
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

		if !flags.quiet {
			fmt.Fprintf(os.Stderr, "  parent: bumped %s pointer (%s)\n", subRelPath, sha[:8])
		}
	}

	return nil
}
