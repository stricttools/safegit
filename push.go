package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/strictcli/go/strictcli"
)

// Exit codes specific to push
const (
	exitPushHookFailed  = 20
	exitPushHookTimeout = 21
	exitPushGitFailed   = 40
)

// pushMode selects which refs to push.
type pushMode int

const (
	pushModeHead     pushMode = iota // push only the current branch
	pushModeBranches                 // push all branches
	pushModeTags                     // push all tags
	pushModeBoth                     // push all branches and all tags
)

// nullSHA is the zero SHA used when a ref does not exist on the remote. It is
// the same all-zero object name git.ZeroSHA carries into update-ref; there is
// one convention and one spelling of it.
const nullSHA = git.ZeroSHA

// pushRefInfo describes a single ref being pushed.
type pushRefInfo struct {
	LocalRef  string
	LocalSHA  string
	RemoteRef string
	RemoteSHA string
}

func runPush(flags globalFlags, noPrePrePush bool, forceWithLease bool, remote string, mode pushMode) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(1, err.Error())
		return 1
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(1, fmt.Sprintf("loading config: %v", err))
		return 1
	}

	forceFlag := forceWithLease

	// Resolve the remote URL
	ctx := flags.ctx()
	remoteURL, err := resolveRemoteURL(ctx, remote)
	if err != nil {
		die(1, fmt.Sprintf("resolving remote URL: %v", err))
		return 1
	}

	// Resolve refs to push
	refs, err := resolveRefsForPush(ctx, remote, mode)
	if err != nil {
		die(1, fmt.Sprintf("resolving refs: %v", err))
		return 1
	}

	if len(refs) == 0 {
		die(1, "nothing to push (no matching refs)")
		return 1
	}

	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  remote: %s (%s)\n", remote, remoteURL)
		for _, r := range refs {
			fmt.Fprintf(os.Stderr, "  ref: %s -> %s\n", shortRef(r.LocalRef), shortRef(r.RemoteRef))
		}
	}

	// Build hook stdin (same format as git pre-push)
	var stdinLines []string
	for _, r := range refs {
		stdinLines = append(stdinLines, fmt.Sprintf("%s %s %s %s", r.LocalRef, r.LocalSHA, r.RemoteRef, r.RemoteSHA))
	}
	hookStdin := []byte(strings.Join(stdinLines, "\n") + "\n")

	// Run pre-pre-push hooks (unless disabled). A dry run never runs them:
	// a hook is an arbitrary user script, so executing one is a mutation, and
	// hooks.RunAll feeds it stdin -- something the effects handle's closed
	// method set has no way to express (see docs/dry-run notes).
	var hookResults []hooks.HookResult
	if !noPrePrePush && flags.dryRun && !flags.silent() {
		fmt.Fprintln(os.Stderr, "  pre-pre-push hooks are not run under --dry-run")
	}
	if !noPrePrePush && !flags.dryRun {
		timeoutSec := cfg.Hooks.PrePrePush.TimeoutSeconds
		if timeoutSec <= 0 {
			timeoutSec = 1800
		}

		hookEnv := []string{
			"SAFEGIT_REMOTE_NAME=" + remote,
			"SAFEGIT_REMOTE_URL=" + remoteURL,
			"SAFEGIT_PHASE=pre-pre-push",
			fmt.Sprintf("SAFEGIT_HOOK_TIMEOUT_S=%d", timeoutSec),
		}

		// Discover hooks: if inside a submodule, cascade from parent first
		var hookPaths []string
		parentGitDir, _, isSubmodule := submodule.DetectParent(ctx)
		if isSubmodule {
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  submodule detected, cascading hooks from parent %s\n", parentGitDir)
			}
			hookPaths, err = hooks.DiscoverMulti([]string{parentGitDir, gitDir})
		} else {
			hookPaths, err = hooks.Discover(gitDir)
		}
		if err != nil {
			die(1, fmt.Sprintf("discovering hooks: %v", err))
			return 1
		}

		hookResults, err = hooks.RunAll(ctx, hookPaths, hookStdin, timeoutSec, hookEnv)
		if err != nil {
			die(1, fmt.Sprintf("running hooks: %v", err))
			return 1
		}

		// Check hook results
		for _, hr := range hookResults {
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  hook %s: exit=%d (%v)\n", hr.Name, hr.ExitCode, hr.Duration)
			}
			if hr.TimedOut {
				fmt.Fprintf(os.Stderr, "hook %s timed out after %v\n", hr.Name, hr.Duration)
				return exitPushHookTimeout
			}
			if hr.ExitCode != 0 {
				fmt.Fprintf(os.Stderr, "hook %s failed (exit %d)\n", hr.Name, hr.ExitCode)
				return exitPushHookFailed
			}
		}
	}

	// Execute git push with retries
	retryAttempts := cfg.Push.RetryAttempts
	if retryAttempts <= 0 {
		retryAttempts = 3
	}

	// Build explicit refspecs from resolved refs
	refspecs := make([]string, len(refs))
	for i, r := range refs {
		refspecs[i] = r.LocalRef + ":" + r.RemoteRef
	}
	pushArgs := buildGitPushArgs(remote, refspecs, forceFlag)
	var pushErr error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		pushErr = execGitPush(flags, pushArgs)
		if pushErr == nil {
			break
		}
		// Only retry on transport errors (not on rejected pushes like non-fast-forward)
		if !isTransportError(pushErr) {
			break
		}
		if attempt < retryAttempts {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  retry %d/%d after %v\n", attempt+1, retryAttempts, backoff)
			} else if !flags.silent() {
				fmt.Fprintf(os.Stderr, "transport error, retrying in %v (attempt %d/%d)...\n", backoff, attempt+1, retryAttempts)
			}
			time.Sleep(backoff)
		}
	}

	if pushErr != nil {
		fmt.Fprintf(os.Stderr, "push failed: %v\n", pushErr)
		return exitPushGitFailed
	}

	// Log to oplog. The oplog is an atomically-appended JSONL audit trail; the
	// effects handle's `write` is whole-content and would destroy the O_APPEND
	// concurrency guarantee, so this mutation stays outside the handle and is
	// simply skipped in dry mode -- a preview leaves no audit trail behind.
	if !flags.dryRun {
		sgDir := repo.SafegitDir(gitDir)
		refDetails := make([]map[string]string, len(refs))
		for i, r := range refs {
			refDetails[i] = map[string]string{
				"localRef": r.LocalRef, "localSha": r.LocalSHA,
				"remoteRef": r.RemoteRef, "remoteSha": r.RemoteSHA,
			}
		}
		_ = oplog.Append(sgDir, oplog.Entry{
			Op: "push",
			Extra: map[string]interface{}{
				"remote":   remote,
				"refs":     refDetails,
				"hooksRun": len(hookResults),
			},
		})
	}

	// Output result
	if !flags.silent() && !flags.dryRun {
		for _, r := range refs {
			fmt.Printf("  %s -> %s\n", shortRef(r.LocalRef), shortRef(r.RemoteRef))
		}
		if len(hookResults) > 0 {
			fmt.Printf("(%d pre-pre-push hook(s) passed)\n", len(hookResults))
		}
	}
	return 0
}

// resolveRemoteURL gets the URL for a named remote.
func resolveRemoteURL(ctx context.Context, remote string) (string, error) {
	stdout, _, err := git.Run(ctx, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("remote %q not found", remote)
	}
	return strings.TrimSpace(stdout), nil
}

// resolveRefsForPush determines what refs will be pushed based on the selected mode.
func resolveRefsForPush(ctx context.Context, remote string, mode pushMode) ([]pushRefInfo, error) {
	switch mode {
	case pushModeHead:
		return resolveHeadRef(ctx, remote)
	case pushModeBranches:
		return resolveBranchRefs(ctx, remote)
	case pushModeTags:
		return resolveTagRefs(ctx, remote)
	case pushModeBoth:
		branches, err := resolveBranchRefs(ctx, remote)
		if err != nil {
			return nil, err
		}
		tags, err := resolveTagRefs(ctx, remote)
		if err != nil {
			return nil, err
		}
		return append(branches, tags...), nil
	default:
		return nil, fmt.Errorf("unknown push mode")
	}
}

// resolveHeadRef resolves the current branch for --only-head mode.
func resolveHeadRef(ctx context.Context, remote string) ([]pushRefInfo, error) {
	headRef, err := git.HeadRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot push: HEAD is detached; check out a branch first")
	}

	localSHA, err := git.RevParse(ctx, headRef)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", headRef, err)
	}

	remoteSHA := getRemoteSHA(ctx, remote, headRef)

	return []pushRefInfo{{
		LocalRef:  headRef,
		LocalSHA:  localSHA,
		RemoteRef: headRef,
		RemoteSHA: remoteSHA,
	}}, nil
}

// resolveBranchRefs enumerates all local branches for --only-branches mode.
func resolveBranchRefs(ctx context.Context, remote string) ([]pushRefInfo, error) {
	lines, err := git.ForEachRef(ctx, "%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("listing local branches: %w", err)
	}

	remoteMap, err := git.LsRemoteBulk(ctx, remote, "refs/heads/*")
	if err != nil {
		return nil, fmt.Errorf("listing remote branches: %w", err)
	}

	var refs []pushRefInfo
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refName := parts[0]
		localSHA := parts[1]
		remoteSHA := nullSHA
		if sha, ok := remoteMap[refName]; ok {
			remoteSHA = sha
		}
		refs = append(refs, pushRefInfo{
			LocalRef:  refName,
			LocalSHA:  localSHA,
			RemoteRef: refName,
			RemoteSHA: remoteSHA,
		})
	}
	return refs, nil
}

// resolveTagRefs enumerates all local tags for --only-tags mode.
func resolveTagRefs(ctx context.Context, remote string) ([]pushRefInfo, error) {
	lines, err := git.ForEachRef(ctx, "%(refname) %(objectname)", "refs/tags/")
	if err != nil {
		return nil, fmt.Errorf("listing local tags: %w", err)
	}

	remoteMap, err := git.LsRemoteBulk(ctx, remote, "refs/tags/*")
	if err != nil {
		return nil, fmt.Errorf("listing remote tags: %w", err)
	}

	var refs []pushRefInfo
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refName := parts[0]
		localSHA := parts[1]
		remoteSHA := nullSHA
		if sha, ok := remoteMap[refName]; ok {
			remoteSHA = sha
		}
		refs = append(refs, pushRefInfo{
			LocalRef:  refName,
			LocalSHA:  localSHA,
			RemoteRef: refName,
			RemoteSHA: remoteSHA,
		})
	}
	return refs, nil
}

func getRemoteSHA(ctx context.Context, remote, ref string) string {
	stdout, _, err := git.Run(ctx, "ls-remote", remote, ref)
	if err != nil || stdout == "" {
		return nullSHA
	}
	parts := strings.Fields(stdout)
	if len(parts) >= 1 {
		return parts[0]
	}
	return nullSHA
}

func buildGitPushArgs(remote string, refspecs []string, force bool) []string {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	args = append(args, refspecs...)
	return args
}

// execGitPush runs git push through the effects handle, streaming
// stdout/stderr to the user. Routing it here is what makes `--dry-run` honest:
// the push is recorded in the would-do log and nothing reaches the remote.
func execGitPush(flags globalFlags, args []string) error {
	argv, err := gitexec.ArgvAny(gitexec.ExemptGitPush, args...)
	if err != nil {
		return err
	}
	grant := "push"
	for _, a := range args {
		if strings.HasPrefix(a, "--force-with-lease") || a == "--force" {
			grant = "force-push"
			break
		}
	}
	_, err = flags.effects().Run(argv,
		strictcli.Stream(true),
		strictcli.UseGrant(grant),
		strictcli.Resource("remote-refs:"+remoteOf(args)),
	)
	return err
}

// remoteOf extracts the remote name from a built `git push` argv.
func remoteOf(args []string) string {
	for _, a := range args {
		if a == "push" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return "origin"
}

// isTransportError heuristically determines if a push error is a network/transport issue
// (worth retrying) vs. a logical rejection (non-fast-forward, permission denied).
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transportPatterns := []string{
		"Could not resolve host",
		"Connection refused",
		"Connection reset",
		"Connection timed out",
		"Network is unreachable",
		"failed to connect",
		"SSL",
		"TLS",
		"EOF",
		"broken pipe",
		"transport",
	}
	for _, p := range transportPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func shortRef(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	ref = strings.TrimPrefix(ref, "refs/tags/")
	return ref
}
