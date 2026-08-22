package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/strictcli/go/strictcli"
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

// pushPayloadRef is one ref as the machine payload reports it.
type pushPayloadRef struct {
	LocalRef  string `json:"local_ref"`
	LocalSHA  string `json:"local_sha"`
	RemoteRef string `json:"remote_ref"`
	// RemoteSHA is what safegit observed on the remote, and null when the ref
	// is not there yet. The internal all-zero marker never leaves the process:
	// a machine consumer reading 0000... would have to know the convention.
	RemoteSHA *string `json:"remote_sha"`
	// Lease is the expectation pinned onto this ref, and null when the push is
	// not forcing and therefore sends no lease. The empty string is a real
	// value, not an absent one: it is git's spelling for "this ref must not
	// exist yet".
	Lease *string `json:"lease"`
}

// pushPayload is what `push` puts in the envelope's payload.
//
// The hook members are the reason it exists. A dry run does NOT run the
// pre-pre-push hooks -- a hook is an arbitrary script, so running one is a
// mutation a preview may not perform -- and a machine consumer reading a
// preview would otherwise have no way to tell "the hooks passed" from "the
// hooks were never asked".
type pushPayload struct {
	Remote string           `json:"remote"`
	Refs   []pushPayloadRef `json:"refs"`
	// ForceWithLease reports that every ref carried a pinned lease.
	ForceWithLease bool `json:"force_with_lease"`
	// Atomic reports that the push was all-or-nothing, which safegit turns on
	// for every multi-ref push.
	Atomic bool `json:"atomic"`
	// PrePrePushHooksRun is how many pre-pre-push hooks actually ran.
	PrePrePushHooksRun int `json:"pre_pre_push_hooks_run"`
	// PrePrePushHooksSkipped says WHY none ran: "dry-run" when the run is a
	// preview, "disabled" when --no-pre-push-hook was passed, and null when the
	// hooks were asked (whether or not any were installed).
	PrePrePushHooksSkipped *string `json:"pre_pre_push_hooks_skipped"`
	DryRun                 bool    `json:"dry_run"`
}

// pushPayloadSchema declares what `push` puts in the envelope's payload. The
// framework validates the value against it at emission, so the declaration and
// the struct above cannot drift.
var pushPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"remote": strictcli.SchemaType("string"),
		"refs": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"local_ref":  strictcli.SchemaType("string"),
				"local_sha":  strictcli.SchemaType("string"),
				"remote_ref": strictcli.SchemaType("string"),
				"remote_sha": strictcli.SchemaType("string", "null"),
				"lease":      strictcli.SchemaType("string", "null"),
			},
			[]string{"local_ref", "local_sha", "remote_ref", "remote_sha", "lease"},
			false,
		)),
		"force_with_lease":           strictcli.SchemaType("boolean"),
		"atomic":                     strictcli.SchemaType("boolean"),
		"pre_pre_push_hooks_run":     strictcli.SchemaType("integer"),
		"pre_pre_push_hooks_skipped": strictcli.SchemaType("string", "null"),
		"dry_run":                    strictcli.SchemaType("boolean"),
	},
	[]string{"remote", "refs", "force_with_lease", "atomic", "pre_pre_push_hooks_run", "pre_pre_push_hooks_skipped", "dry_run"},
	false,
)

// hookSkipDryRun is the value the payload's pre_pre_push_hooks_skipped member
// carries when a preview did not run the hooks. It is the payload's spelling
// only: the --help text and the preview's own stderr notice say the same thing
// in prose, for readers who are not parsing anything, and there is nothing for
// them to share a constant with.
const hookSkipDryRun = "dry-run"

// buildPushPayload renders what actually happened into the machine payload.
func buildPushPayload(flags globalFlags, remote string, refs []pushRefInfo, force bool, hooksRun int, hooksSkipped *string) pushPayload {
	out := make([]pushPayloadRef, 0, len(refs))
	for _, r := range refs {
		entry := pushPayloadRef{LocalRef: r.LocalRef, LocalSHA: r.LocalSHA, RemoteRef: r.RemoteRef}
		if r.RemoteSHA != nullSHA {
			sha := r.RemoteSHA
			entry.RemoteSHA = &sha
		}
		if force {
			lease := leaseExpectation(r)
			entry.Lease = &lease
		}
		out = append(out, entry)
	}
	return pushPayload{
		Remote:                 remote,
		Refs:                   out,
		ForceWithLease:         force,
		Atomic:                 pushIsAtomic(refs),
		PrePrePushHooksRun:     hooksRun,
		PrePrePushHooksSkipped: hooksSkipped,
		DryRun:                 flags.dryRun,
	}
}

func runPush(flags globalFlags, noPrePrePush bool, forceWithLease bool, remote string, mode pushMode) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
		return exitcode.NotInitialized
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
		return exitcode.General
	}

	forceFlag := forceWithLease

	// Resolve the remote URL
	ctx := flags.ctx()
	remoteURL, err := resolveRemoteURL(ctx, remote)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving remote URL: %v", err))
		return exitcode.General
	}

	// A force-push is consequential; an ordinary push is not. The command is
	// therefore consequential CONDITIONALLY, which strictcli cannot yet declare
	// (a todo is filed upstream), so the condition is asked at safegit's own
	// confirmation seam instead -- and asked here, in front of every network
	// read the push would otherwise do, so a declined force contacts nothing.
	//
	// --approve-consequential is the right consent because the condition IS the
	// flag the caller typed: nothing is discovered at run time, so a caller
	// composing the command line already knows it is forcing.
	//
	// A dry run asks nothing: it overwrites no ref, so there is nothing to
	// consent to.
	if forceFlag && !flags.dryRun {
		c := consent{granted: flags.approved, flag: "--approve-consequential"}
		if !confirmDeliberate(flags, c,
			"Force-push to %s (%s), overwriting whatever each ref's lease expectation does not cover?", remote, remoteURL) {
			infof(flags, "Aborted.\n")
			return exitcode.General
		}
	}

	// Resolve refs to push
	refs, err := resolveRefsForPush(ctx, remote, mode)
	if err != nil {
		// A remote that could not be READ exits PushFailed, the same code the
		// retry path uses when its own re-read fails: the push did not get
		// through, and no amount of fixing the command line changes that. The
		// other failures here -- a detached HEAD, a local ref that will not
		// resolve -- are about this repository and stay General.
		var readErr *remoteReadError
		if errors.As(err, &readErr) {
			die(exitcode.PushFailed, err.Error())
			return exitcode.PushFailed
		}
		die(exitcode.General, fmt.Sprintf("resolving refs: %v", err))
		return exitcode.General
	}

	if len(refs) == 0 {
		die(exitcode.General, "nothing to push (no matching refs)")
		return exitcode.General
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

	// The set the hooks are shown, kept for the retry check below. `refs` is
	// reassigned on every retry; this is what the run committed to pushing.
	validated := refs

	// Run pre-pre-push hooks (unless disabled). A dry run never runs them:
	// a hook is an arbitrary user script, so executing one is a mutation, and
	// hooks.RunAll feeds it stdin -- something the effects handle's closed
	// method set has no way to express (see docs/dry-run notes).
	//
	// The skip is stated in three places, because three different readers have
	// to see it: --help (before the run), the preview's own output (during it),
	// and the payload's pre_pre_push_hooks_skipped member (for a machine
	// consumer, which sees neither of the other two). A preview that silently
	// omitted the hooks would read exactly like a preview whose hooks passed.
	//
	// The switch arms are ordered deliberately: "disabled" beats "dry-run" when
	// both hold. A preview of a run that turned the hooks OFF must report the
	// operator's decision, not the preview's own limitation -- the caller wants
	// to know what the real push will do, and the real push will not run them
	// either.
	var hooksSkipped *string
	switch {
	case noPrePrePush:
		reason := "disabled"
		hooksSkipped = &reason
	case flags.dryRun:
		reason := hookSkipDryRun
		hooksSkipped = &reason
		if !flags.silent() {
			fmt.Fprintln(os.Stderr, "  pre-pre-push hooks are not run under --dry-run; the real push will run them")
		}
	}

	var hookResults []hooks.HookResult
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
			die(exitcode.General, fmt.Sprintf("discovering hooks: %v", err))
			return exitcode.General
		}

		hookResults, err = hooks.RunAll(ctx, hookPaths, hookStdin, timeoutSec, hookEnv)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("running hooks: %v", err))
			return exitcode.General
		}

		// Check hook results
		for _, hr := range hookResults {
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  hook %s: exit=%d (%v)\n", hr.Name, hr.ExitCode, hr.Duration)
			}
			if hr.TimedOut {
				fmt.Fprintf(os.Stderr, "hook %s timed out after %v\n", hr.Name, hr.Duration)
				return exitcode.PushHookTimeout
			}
			if hr.ExitCode != 0 {
				fmt.Fprintf(os.Stderr, "hook %s failed (exit %d)\n", hr.Name, hr.ExitCode)
				return exitcode.PushHookFailed
			}
		}
	}

	// Execute git push with retries
	retryAttempts := cfg.Push.RetryAttempts
	if retryAttempts <= 0 {
		retryAttempts = 3
	}

	pushArgs := buildGitPushArgs(remote, refs, forceFlag)
	var pushErr error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		var gitStderr string
		gitStderr, pushErr = execGitPush(flags, pushArgs)
		if pushErr == nil {
			break
		}
		// A rejected lease is TERMINAL. It is not a flaky connection: somebody
		// moved the ref between safegit observing it and the push reaching it,
		// and the lease is what stopped their commits from being overwritten.
		// Retrying would re-observe THEIR ref, pin the lease to it, and quietly
		// do the overwriting the lease just prevented -- so the retry loop must
		// never see it.
		if leaseRejected(gitStderr, forceFlag) {
			fmt.Fprintf(os.Stderr,
				"push refused: the remote moved after safegit read it, so the --force-with-lease expectation no longer matches\n"+
					"  somebody else pushed to %s between the read and the push, and the lease kept their work\n"+
					"  fetch and look at what arrived (git fetch %s), then decide again\n",
				remote, remote)
			return exitcode.PushLeaseRejected
		}
		// Everything else that is not transport -- non-fast-forward, permission
		// denied -- is a verdict too, and equally not worth repeating.
		if !isTransportError(gitStderr) {
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
			// Re-observe the remote and re-pin every lease before trying again.
			// An expectation describes the remote at a moment; after a failed
			// attempt and a wait, that moment has passed, and a retry carrying
			// the old expectation would refuse a ref that is now fine (or, on a
			// partially-applied push, assert something safegit never saw).
			fresh, err := resolveRefsForPush(ctx, remote, mode)
			if err != nil {
				die(exitcode.PushFailed, fmt.Sprintf("re-reading %s before retrying the push: %v", remote, err))
				return exitcode.PushFailed
			}
			if len(fresh) == 0 {
				die(exitcode.PushFailed, "nothing to push (no matching refs) when re-reading the remote before a retry")
				return exitcode.PushFailed
			}
			// The re-read re-resolves the LOCAL side too, and that is a second
			// window: the push set was decided once, before the pre-pre-push hooks
			// ran, and the hooks were handed those exact SHAs. A local ref that
			// moved since would be published on the strength of a hook run that
			// never saw it. safegit does not re-run the hooks mid-retry -- a hook
			// is an arbitrary script and running it again is a second mutation the
			// operator did not ask for -- so it refuses instead. Fail closed: the
			// operator re-runs the push, which validates and publishes what is
			// actually there now.
			if change, moved := localRefsMoved(validated, fresh); moved {
				die(exitcode.PushFailed, fmt.Sprintf(
					"push refused: %s moved locally while the push was being retried\n"+
						"  safegit resolved %s before pushing; it is now %s\n"+
						"  the push set and the pre-pre-push hooks' input are decided once, and safegit does not re-run the hooks mid-retry\n"+
						"  re-run the push to validate and publish the refs as they stand",
					change.ref, change.was, change.now))
				return exitcode.PushFailed
			}
			refs = fresh
			pushArgs = buildGitPushArgs(remote, refs, forceFlag)
		}
	}

	if pushErr != nil {
		fmt.Fprintf(os.Stderr, "push failed: %v\n", pushErr)
		return exitcode.PushFailed
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

	flags.payload(buildPushPayload(flags, remote, refs, forceFlag, len(hookResults), hooksSkipped))

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

	remoteSHA, err := getRemoteSHA(ctx, remote, headRef)
	if err != nil {
		return nil, err
	}

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
		// Same classification as the single-ref read: one unreadable remote has
		// one answer, whether the push is of one ref or of all of them.
		return nil, &remoteReadError{Remote: remote, Pattern: "refs/heads/*", Err: err}
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
		return nil, &remoteReadError{Remote: remote, Pattern: "refs/tags/*", Err: err}
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

// remoteReadError is a failure to OBSERVE the remote -- git could not list its
// refs at all -- as distinct from an observation that came back saying the ref
// is not there.
//
// Keeping the two apart is the lease's requirement. safegit pins
// --force-with-lease to the SHA it observed, and it pins "absent" to the EMPTY
// expectation, which is git's spelling for "this ref must not exist yet". So an
// unreadable remote answered with the absent marker asserts that a ref which
// plainly does exist does not: git refuses the push as a stale lease, safegit
// reports it as a concurrent pusher who was never there, and the payload says
// the remote ref is null. An unreadable remote is not a state of the remote; it
// is not knowing one, and it is fatal rather than an answer.
type remoteReadError struct {
	// Remote is the remote name that could not be read.
	Remote string
	// Pattern is the ref or ref glob that was being listed.
	Pattern string
	// Err is git's own failure.
	Err error
}

func (e *remoteReadError) Error() string {
	return fmt.Sprintf("reading %s on %s: %v", e.Pattern, e.Remote, e.Err)
}

func (e *remoteReadError) Unwrap() error { return e.Err }

// getRemoteSHA returns the SHA the remote has for one ref, or the null marker
// when the remote answered and does not have it. A remote that could not be
// read is an error, never the null marker -- see remoteReadError.
func getRemoteSHA(ctx context.Context, remote, ref string) (string, error) {
	stdout, stderr, err := git.Run(ctx, "ls-remote", remote, ref)
	if err != nil {
		if msg := strings.TrimSpace(stderr); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", &remoteReadError{Remote: remote, Pattern: ref, Err: err}
	}
	parts := strings.Fields(stdout)
	if len(parts) >= 1 {
		return parts[0], nil
	}
	return nullSHA, nil
}

// localRefMove names one local ref whose SHA is not what an earlier resolution
// saw. An absent side is spelled "(absent)" rather than left blank, so a
// message naming it reads as a sentence either way.
type localRefMove struct {
	ref string
	was string
	now string
}

// localRefsMoved compares the LOCAL side of two resolutions of the same push
// and reports the first difference: a SHA that changed, a ref that appeared, or
// one that went away. Order is the earlier resolution's, so the message a
// caller builds is stable rather than map-ordered.
func localRefsMoved(before, after []pushRefInfo) (localRefMove, bool) {
	const absent = "(absent)"
	now := make(map[string]string, len(after))
	for _, r := range after {
		now[r.LocalRef] = r.LocalSHA
	}
	seen := make(map[string]bool, len(before))
	for _, r := range before {
		seen[r.LocalRef] = true
		sha, ok := now[r.LocalRef]
		if !ok {
			return localRefMove{ref: r.LocalRef, was: r.LocalSHA, now: absent}, true
		}
		if sha != r.LocalSHA {
			return localRefMove{ref: r.LocalRef, was: r.LocalSHA, now: sha}, true
		}
	}
	for _, r := range after {
		if !seen[r.LocalRef] {
			return localRefMove{ref: r.LocalRef, was: absent, now: r.LocalSHA}, true
		}
	}
	return localRefMove{}, false
}

// pushIsAtomic is the one place the all-or-nothing rule is decided: safegit
// makes every multi-ref push atomic, so one refused ref leaves the remote
// exactly as it was rather than half-published.
//
// buildGitPushArgs asks it before adding --atomic, and buildPushPayload asks it
// before reporting the fact, so the argv git ran and the payload a machine
// consumer reads cannot disagree about what the push was.
func pushIsAtomic(refs []pushRefInfo) bool { return len(refs) > 1 }

// leaseExpectation is the value safegit pins a ref's lease to: the SHA it
// observed on the remote, or the EMPTY string when the ref is not there yet.
//
// The empty expectation is git's spelling for "this ref must not exist", and it
// is what the internal all-zero "absent" marker has to become. Sending the
// literal 0000... instead would be an expectation no ref can ever satisfy, so
// every first push of a ref would be refused.
func leaseExpectation(r pushRefInfo) string {
	if r.RemoteSHA == nullSHA {
		return ""
	}
	return r.RemoteSHA
}

// buildGitPushArgs composes the `git push` argv for one attempt.
//
// The lease is PER REF and pinned to the SHA safegit itself observed a moment
// earlier: `--force-with-lease=<remoteRef>:<observedSHA>`. A BARE
// `--force-with-lease` means something different -- compare the remote ref
// against the remote-TRACKING ref for it -- and that is a value safegit never
// resolved. For tags it is a value that does not exist at all: tags have no
// remote-tracking refs, so git zeroes the expectation and refuses to move any
// tag the remote already carries. That refusal is what made safegit's own
// post-scrub instruction ("push the rewritten tags") unsatisfiable.
//
// `--atomic` goes on every multi-ref push, forced or not: one refused ref must
// leave the remote exactly as it was rather than half-published.
func buildGitPushArgs(remote string, refs []pushRefInfo, force bool) []string {
	args := []string{"push"}
	if pushIsAtomic(refs) {
		args = append(args, "--atomic")
	}
	if force {
		for _, r := range refs {
			args = append(args, "--force-with-lease="+r.RemoteRef+":"+leaseExpectation(r))
		}
	}
	args = append(args, remote)
	for _, r := range refs {
		args = append(args, r.LocalRef+":"+r.RemoteRef)
	}
	return args
}

// execGitPush runs git push through the effects handle and returns git's own
// stderr along with an error when the push did not succeed. Routing it here is
// what makes `--dry-run` honest: the push is recorded in the would-do log and
// nothing reaches the remote.
//
// git's output is CAPTURED and written back out rather than streamed straight
// through, because the caller has to read it: a lease rejection, a
// non-fast-forward and a dropped connection are all the same nonzero exit code
// and differ only in what git said. The framework's error string carries the
// argv and the code, never the child's stderr, so classifying on it is
// impossible. The cost is that a long push's progress arrives at the end
// instead of live -- strictcli's Run streams or captures, with no tee.
func execGitPush(flags globalFlags, args []string) (stderrText string, err error) {
	argv, err := gitexec.ArgvAny(gitexec.ExemptGitPush, args...)
	if err != nil {
		return "", err
	}
	grant := "push"
	for _, a := range args {
		if strings.HasPrefix(a, "--force-with-lease") || a == "--force" {
			grant = "force-push"
			break
		}
	}
	done, err := flags.effects().Run(argv,
		strictcli.Check(false),
		strictcli.UseGrant(grant),
		strictcli.Resource("remote-refs:"+remoteOf(args)),
	)
	if err != nil {
		return "", err
	}
	if flags.dryRun {
		// The invocation was recorded instead of performed, so the Completed is
		// unsettled and reading it would panic (the same reasoning, and the same
		// flag-keyed test, as runGitMutation's dry-run branch).
		return "", nil
	}

	if out := done.Stdout(); out != "" {
		// Machine mode owns stdout: the envelope is the only document there, so
		// git's own stdout joins its stderr instead of corrupting it.
		w := os.Stdout
		if flags.json {
			w = os.Stderr
		}
		fmt.Fprint(w, out)
	}
	stderrText = done.Stderr()
	if stderrText != "" {
		fmt.Fprint(os.Stderr, stderrText)
	}
	if code := done.ExitCode(); code != 0 {
		return stderrText, fmt.Errorf("git push exited %d", code)
	}
	return stderrText, nil
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

// leaseRejected reports that git refused the push because a --force-with-lease
// expectation did not match what was on the remote.
//
// The signature, probed against a local bare remote with git 2.54.0 and pinned
// by TestGitBareLeaseCannotForcePushAMovedTag so a git release that respells it
// fails loudly instead of turning every lease rejection into a retried
// transport error:
//
//	To /path/to/remote
//	 ! [rejected]        v1.0 -> v1.0 (stale info)
//	error: failed to push some refs to '/path/to/remote'
//
// Under --atomic the refs that were fine read `(atomic push failed)` on their
// own lines; the rejected one still reads `(stale info)`, so one marker
// classifies the whole push.
//
// Both anchors are deliberate. The PARENTHESES are part of git's per-ref status
// marker rather than decoration, so matching them keeps the words "stale info"
// appearing anywhere else -- a commit subject echoed back, remote-side prose --
// from being read as a rejection. And only a push that SENT a lease can be
// refused for one, so an unforced push is never classified here; without that
// anchor a plain non-fast-forward could exit PushLeaseRejected with a message
// about an expectation safegit never pinned.
//
// A miss is safe in the direction that counts: an unrecognized rejection falls
// through to isTransportError, which does not match it either, so the loop
// breaks out and the push fails terminally. What a miss cannot do is cause a
// retry that overwrites somebody else's work.
func leaseRejected(stderrText string, forced bool) bool {
	return forced && strings.Contains(stderrText, "(stale info)")
}

// isTransportError heuristically determines if a push failure is a
// network/transport issue (worth retrying) vs. a logical rejection
// (non-fast-forward, permission denied, a stale lease).
//
// It reads GIT'S OWN stderr, which is the only place those words ever appear.
// It used to be handed the error the effects handle returns -- a formatted
// string carrying the argv and the exit code and nothing of the child's output
// -- so no pattern here could ever match and the retry loop was dead.
//
// Every pattern is a MULTI-WORD PHRASE one of the three layers below actually
// emits. That is not a style preference: git's stderr echoes REF NAMES (the
// `! [rejected] <ref> -> <ref>` lines), a ref name cannot contain a space, and
// so a phrase cannot be one. The list used to carry bare `EOF`, `SSL`, `TLS`
// and `transport`, which made a branch called `transport-refactor` classify its
// own non-fast-forward rejection as a dropped connection and retry it.
//
// The retry direction is the dangerous one. A transport failure misread as a
// verdict costs one lost retry; a verdict misread as a transport failure
// re-observes the remote, re-pins the lease to whatever is there now, and
// pushes again.
func isTransportError(stderrText string) bool {
	if stderrText == "" {
		return false
	}
	// Captured from git 2.54.0 against unreachable remotes, except where noted;
	// the samples are in push_classify_test.go's gitTransportFailures, which is
	// what re-derives this list if a git or curl release respells anything.
	transportPatterns := []string{
		// curl (https) and OpenSSH resolver failures. The ssh spelling is
		// "Could not resolve hostname", which this covers as a prefix.
		"Could not resolve host",
		// OpenSSH; also the tail of git's native transport line,
		// "127.0.0.1[0: 127.0.0.1]: errno=Connection refused".
		"Connection refused",
		// strerror, reached through curl ("Recv failure: Connection reset by
		// peer") and through ssh.
		"Connection reset",
		"Connection timed out",
		"Network is unreachable",
		"Transport endpoint is not connected",
		// curl's two connect failures, both on one line of a real refusal.
		"Failed to connect to",
		"Could not connect to server",
		// git's own native (git://) transport.
		"unable to connect to",
		// curl's TLS layer, and the OpenSSL error strings behind it. GnuTLS
		// builds of curl report gnutls_handshake instead.
		"TLS connect error",
		"SSL connect error",
		"SSL certificate problem",
		"SSL routines",
		"gnutls_handshake",
		// A transfer that was cut off mid-pack. Not from the local probe --
		// staging a connection that dies mid-transfer is not cheap -- but these
		// are git's long-standing spellings, and "unexpected EOF" is also
		// OpenSSL's.
		"early EOF",
		"unexpected EOF",
		"the remote end hung up unexpectedly",
		"RPC failed",
		// strerror again, via git's own write error and curl's send failure.
		"Broken pipe",
	}
	for _, p := range transportPatterns {
		if strings.Contains(stderrText, p) {
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
