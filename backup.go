package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// backupRefPrefix is the safegit-owned remote namespace. Each branch gets one
// slot: refs/backups/<branch>. Nothing else writes into this namespace, which
// is why backup pushes bypass local pre-push hooks (--no-verify): the hooks
// exist to police refs/heads, not this tool-internal namespace.
const backupRefPrefix = "refs/backups/"

// backupRef returns the backup slot ref for a branch.
func backupRef(branch string) string { return backupRefPrefix + branch }

// previewLeasePlaceholder stands where the lease's expected SHA goes in a
// recorded push. A backup's lease is pinned to the slot's value READ FROM THE
// REMOTE a moment before the push, and a preview contacts no remote at all, so
// there is no such value for it to carry -- the same reasoning, and the same
// spelling convention, as previewCommitPlaceholder.
const previewLeasePlaceholder = "<slot-sha>"

// currentBranch returns the checked-out branch name (not the full ref).
func currentBranch(ctx context.Context, flags globalFlags, cmd string) string {
	headRef, err := git.HeadRef(ctx)
	if err != nil || headRef == "" {
		die(exitcode.General, "HEAD is detached; backup slots are per branch, so check out a branch first")
	}
	return strings.TrimPrefix(headRef, "refs/heads/")
}

// remoteSlotSHA returns the SHA the remote currently has in the given ref, and
// whether the ref exists at all. Network errors are fatal: a backup must never
// be taken against a guess about remote state.
func remoteSlotSHA(ctx context.Context, flags globalFlags, cmd, remote, ref string) (string, bool) {
	stdout, _, err := git.Run(ctx, "ls-remote", remote, ref)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing %s on %s: %v", ref, remote, err))
	}
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// fetchSlotObjects downloads the slot's objects so its commits can be inspected
// locally, and returns the fetched SHA.
//
// It is `backup backup`'s own fetch, and it is deliberately NOT minted through
// the effects handle: what a network READ is under the effects regime is a
// framework question that has not been answered yet, and this fetch happens only
// on an executing backup, where nothing is being previewed. `backup restore`'s
// fetch IS minted, because a restore preview has to describe it -- see
// runBackupGit.
func fetchSlotObjects(ctx context.Context, flags globalFlags, cmd, remote, ref string) string {
	if _, stderr, err := git.Run(ctx, "fetch", remote, ref); err != nil {
		die(exitcode.General, fmt.Sprintf("fetching %s from %s: %v\n%s", ref, remote, err, strings.TrimSpace(stderr)))
	}
	return resolveFetchHead(ctx)
}

// resolveFetchHead reads what the last fetch brought. It runs on the EXECUTE
// path only: a preview performed no fetch, so FETCH_HEAD either does not exist
// or still names whatever an earlier command left there, and a preview that read
// it would report a stale answer as this run's.
func resolveFetchHead(ctx context.Context) string {
	sha, err := git.RevParse(ctx, "FETCH_HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving fetched backup: %v", err))
	}
	return sha
}

// mintSlotFetch is `backup restore`'s half of the fetch: the invocation alone,
// minted, with no FETCH_HEAD read after it. The read is the caller's, on the
// execute path only.
func mintSlotFetch(flags globalFlags, remote, ref string) error {
	_, stderr, err := runBackupGit(flags, "backup-slot:"+ref, "fetch", remote, ref)
	if err != nil {
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			return fmt.Errorf("%v\n%s", err, trimmed)
		}
		return err
	}
	return nil
}

// runBackupGit mints one of `backup restore`'s two git invocations -- the fetch
// of the slot, and the fast-forward onto it -- through the effects handle, so a
// preview RECORDS each and performs neither, and a real restore leaves both in
// the envelope's effect log.
//
// It returns git's own stdout and stderr, which the fast-forward's caller reads:
// a refusal there is the one the operator has to see verbatim. In a preview
// nothing ran, so both are empty.
func runBackupGit(flags globalFlags, resource string, args ...string) (stdout, stderr string, err error) {
	argv, err := gitexec.ArgvAny(gitexec.ExemptBackupRestoreGit, gitexec.NoDoor, args...)
	if err != nil {
		return "", "", err
	}
	done, err := flags.effects().Run(argv, strictcli.Resource(resource), strictcli.Check(false))
	if err != nil {
		return "", "", err
	}
	if flags.dryRun {
		// Recorded instead of performed: the carrier is unsettled and asking it
		// anything would panic.
		return "", "", nil
	}
	stdout, stderr = done.Stdout(), done.Stderr()
	if code := done.ExitCode(); code != 0 {
		return stdout, stderr, fmt.Errorf("git %s exited %d", args[0], code)
	}
	return stdout, stderr, nil
}

// --- remote exposure ---

// remoteExposure classifies how visible a remote is, which decides whether
// pushing a full branch snapshot there needs a deliberate confirmation.
type remoteExposure int

const (
	exposureLocal   remoteExposure = iota // filesystem path or file:// URL
	exposurePrivate                       // forge says the repository is private
	exposurePublic                        // forge says the repository is public
	exposureUnknown                       // networked remote whose visibility we cannot determine
)

// githubSlug extracts "owner/repo" from a github.com remote URL, in either the
// scp-like (git@github.com:owner/repo.git) or URL (https://github.com/owner/repo)
// form. The second return value reports whether the URL is a github.com remote.
func githubSlug(remoteURL string) (string, bool) {
	u := strings.TrimSpace(remoteURL)
	switch {
	case strings.HasPrefix(u, "git@github.com:"):
		u = strings.TrimPrefix(u, "git@github.com:")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		u = strings.TrimPrefix(u, "ssh://git@github.com/")
	case strings.HasPrefix(u, "https://github.com/"):
		u = strings.TrimPrefix(u, "https://github.com/")
	case strings.HasPrefix(u, "http://github.com/"):
		u = strings.TrimPrefix(u, "http://github.com/")
	default:
		return "", false
	}
	u = strings.TrimSuffix(strings.Trim(u, "/"), ".git")
	parts := strings.Split(u, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// isNetworkRemote reports whether the URL addresses a remote host rather than a
// path on this machine.
func isNetworkRemote(remoteURL string) bool {
	u := strings.TrimSpace(remoteURL)
	if strings.HasPrefix(u, "file://") {
		return false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "ftp://", "ftps://"} {
		if strings.HasPrefix(u, scheme) {
			return true
		}
	}
	// scp-like syntax: user@host:path (a Windows drive letter is not a host).
	if i := strings.Index(u, ":"); i > 1 && !strings.HasPrefix(u, "/") && !strings.Contains(u[:i], "/") {
		return true
	}
	return false
}

// ghRepoVisibility reports what the forge says about a github.com repository's
// visibility ("PUBLIC", "PRIVATE", "INTERNAL"), or an error when it cannot be
// asked. It is a variable so tests can drive every classification branch
// without gh installed and without touching the network.
var ghRepoVisibility = ghRepoVisibilityViaCLI

// ghRepoVisibilityViaCLI is the real probe: the gh CLI, if it is installed.
func ghRepoVisibilityViaCLI(ctx context.Context, slug string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "gh", "repo", "view", slug, "--json", "visibility", "-q", ".visibility").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// classifyRemote decides whether pushing a branch snapshot to this remote is a
// publication. github.com remotes are probed with gh; any other networked
// remote is unknown, and unknown is treated as "ask", never as "safe".
func classifyRemote(ctx context.Context, remoteURL string) remoteExposure {
	if !isNetworkRemote(remoteURL) {
		return exposureLocal
	}
	slug, ok := githubSlug(remoteURL)
	if !ok {
		return exposureUnknown
	}
	out, err := ghRepoVisibility(ctx, slug)
	if err != nil {
		return exposureUnknown
	}
	switch strings.ToUpper(strings.TrimSpace(out)) {
	case "PUBLIC":
		return exposurePublic
	case "PRIVATE", "INTERNAL":
		return exposurePrivate
	default:
		return exposureUnknown
	}
}

// confirmExposure asks for confirmation when a backup would leave this machine
// for a remote that is public (or that we cannot prove is private). Returns
// false when the user declines.
//
// The question is about the TARGET, and its answer is discovered by probing the
// remote at run time -- a caller composing the command line may not know it. So
// the blanket --approve-consequential does not answer it; only the
// per-condition --allow-public-remote does, which is what keeps a
// non-interactive run from publishing a branch to a public repository without
// having said so.
func confirmExposure(ctx context.Context, flags globalFlags, remote, remoteURL string, allowPublicRemote bool) bool {
	c := consent{granted: allowPublicRemote, flag: "--allow-public-remote"}
	switch classifyRemote(ctx, remoteURL) {
	case exposurePublic:
		fmt.Fprintf(os.Stderr, "warning: %s (%s) is a PUBLIC repository\n", remote, remoteURL)
		fmt.Fprintf(os.Stderr, "         a backup pushes your entire current branch there, including work you have not published\n")
		return confirmDeliberate(flags, c, "Push a backup of this branch to a PUBLIC repository?")
	case exposureUnknown:
		fmt.Fprintf(os.Stderr, "warning: cannot determine whether %s (%s) is public\n", remote, remoteURL)
		fmt.Fprintf(os.Stderr, "         a backup pushes your entire current branch there\n")
		return confirmDeliberate(flags, c, "Push a backup of this branch to a remote of unknown visibility?")
	default:
		return true
	}
}

// --- commands ---

func runBackupCreate(flags globalFlags, remote string, overwriteRemoteBackup, allowPublicRemote bool) int {
	const cmd = "backup backup"

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)
	ctx := flags.ctx()

	branch := currentBranch(ctx, flags, cmd)
	slot := backupRef(branch)

	headSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	remoteURL, err := resolveRemoteURL(ctx, remote)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving remote URL: %v", err))
	}

	// A dry run previews from local state alone. Classifying the remote, reading
	// the slot, and the ancestry check all need the network, so a preview would
	// otherwise prompt about exposure and fail outright on an unreachable
	// remote -- neither of which a preview may do.
	//
	// It still MINTS the push, with the lease pinned to a placeholder: the
	// machine-readable effect record is the whole point of a preview, and an
	// envelope that promised a backup while recording nothing described a run
	// that changes nothing. The placeholder keeps the `--force-with-lease=`
	// spelling, because that prefix is what selects the force-push grant.
	if flags.dryRun {
		lease := "--force-with-lease=" + slot + ":" + previewLeasePlaceholder
		if _, err := execGitPush(flags, []string{"push", "--no-verify", lease, remote, "HEAD:" + slot}); err != nil {
			die(exitcode.General, fmt.Sprintf("recording the push: %v", err))
		}
		infof(flags, "Would back up %s (%s) to backup slot %s on %s\n", branch, headSHA[:12], slot, remote)
		infof(flags, "  equivalent git command: git push --no-verify --force-with-lease=%s:<slot sha observed at run time> %s HEAD:%s\n", slot, remote, slot)
		infof(flags, "  the slot's current SHA, the ancestry check against it, and the lease pinned to it are resolved when the backup runs; no remote was contacted\n")
		infof(flags, "Dry run: no changes made.\n")
		return 0
	}

	// Ask before touching a remote we cannot prove is private: a backup pushes
	// the whole branch, so the decision belongs before any network contact.
	if !confirmExposure(ctx, flags, remote, remoteURL, allowPublicRemote) {
		infof(flags, "Aborted.\n")
		return exitcode.General
	}

	// Read the remote slot BEFORE deciding anything: the fetched SHA is both
	// the ancestry check's subject and the lease the push is pinned to, so the
	// check and the push can never disagree about what was there.
	slotSHA, slotExists := remoteSlotSHA(ctx, flags, cmd, remote, slot)
	var diverged bool
	if slotExists {
		slotSHA = fetchSlotObjects(ctx, flags, cmd, remote, slot)
		isAncestor, err := git.IsAncestorOf(ctx, slotSHA, headSHA)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("comparing backup slot with HEAD: %v", err))
		}
		diverged = !isAncestor
		if diverged && !overwriteRemoteBackup {
			die(exitcode.BackupDiverged, fmt.Sprintf(
				"remote backup contains work not in your current history\n"+
					"  slot: %s on %s = %s\n"+
					"  HEAD: %s\n"+
					"inspect it with: git log --oneline %s\n"+
					"restore it with: safegit backup restore %s\n"+
					"overwrite it deliberately with: safegit backup backup --overwrite-remote-backup %s",
				slot, remote, slotSHA[:12], headSHA[:12], slotSHA[:12], remote, remote))
		}
	}

	// The lease expectation: the SHA observed a moment ago, or the empty string
	// meaning "this ref must not exist yet".
	lease := "--force-with-lease=" + slot + ":" + slotSHA
	pushArgs := []string{"push", "--no-verify", lease, remote, "HEAD:" + slot}

	if gitStderr, err := execGitPush(flags, pushArgs); err != nil {
		// The slot moved between the read above and this push: another machine
		// is backing up the same branch. The lease refused it, which is the same
		// mechanism and the same verdict `push` reports, so it gets the same exit
		// code. It is NOT BackupDiverged -- that is the ancestry refusal, decided
		// from a slot safegit read and found to hold unfamiliar commits, and it
		// happens before anything is pushed.
		if leaseRejected(gitStderr, true) {
			fmt.Fprintf(os.Stderr,
				"backup refused: %s on %s moved after safegit read it, so the --force-with-lease expectation no longer matches\n"+
					"  another machine backed up this branch between the read and the push, and the lease kept its work\n"+
					"  look at what arrived (safegit backup list %s), then run the backup again\n",
				slot, remote, remote)
			return exitcode.PushLeaseRejected
		}
		fmt.Fprintf(os.Stderr, "backup push failed: %v\n", err)
		return exitcode.PushFailed
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "backup",
		Extra: map[string]interface{}{
			"remote":      remote,
			"branch":      branch,
			"ref":         slot,
			"sha":         headSHA,
			"previousSha": slotSHA,
			"overwritten": diverged,
		},
	})

	infof(flags, "  %s (%s) -> %s %s\n", branch, headSHA[:12], remote, slot)
	return 0
}

func runBackupList(flags globalFlags, remote string) int {
	const cmd = "backup list"

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}
	ctx := flags.ctx()

	if _, err := resolveRemoteURL(ctx, remote); err != nil {
		die(exitcode.General, fmt.Sprintf("resolving remote URL: %v", err))
	}

	refs, err := git.LsRemoteBulk(ctx, remote, backupRefPrefix+"*")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing backups on %s: %v", remote, err))
	}
	if len(refs) == 0 {
		infof(flags, "no backups on %s\n", remote)
		return 0
	}

	names := make([]string, 0, len(refs))
	for ref := range refs {
		names = append(names, ref)
	}
	sort.Strings(names)

	width := 0
	for _, ref := range names {
		if n := len(strings.TrimPrefix(ref, backupRefPrefix)); n > width {
			width = n
		}
	}
	for _, ref := range names {
		branch := strings.TrimPrefix(ref, backupRefPrefix)
		sha := refs[ref]
		if len(sha) > 12 {
			sha = sha[:12]
		}
		outf(flags, "  %-*s  %s  %s\n", width, branch, sha, ref)
	}
	return 0
}

func runBackupRestore(flags globalFlags, remote string) int {
	const cmd = "backup restore"

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)

	// gitDir, not sgDir: coordGuard reads git's OWN in-flight operation state
	// (MERGE_HEAD, .git/sequencer, rebase-merge/) out of the git directory, so
	// handing it the safegit directory made every refusal report a bare dirty
	// tree and print the "commit your work" advice even mid-merge, where that
	// advice cannot be followed.
	if code := coordGuard(flags, gitDir, "backup restore"); code != 0 {
		return code
	}

	ctx := flags.ctx()
	branch := currentBranch(ctx, flags, cmd)
	slot := backupRef(branch)

	oldHead, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	if _, err := resolveRemoteURL(ctx, remote); err != nil {
		die(exitcode.General, fmt.Sprintf("resolving remote URL: %v", err))
	}

	slotSHA, slotExists := remoteSlotSHA(ctx, flags, cmd, remote, slot)
	if !slotExists {
		die(exitcode.BackupNoSlot, fmt.Sprintf(
			"no backup slot %s on %s\n"+
				"list what is there with: safegit backup list %s", slot, remote, remote))
	}

	// The preview's two records, in the order the execute path performs them:
	// the fetch, then the fast-forward. Neither is performed. The fast-forward
	// names the SLOT's own SHA rather than FETCH_HEAD, because the preview
	// fetched nothing and FETCH_HEAD would name whatever an earlier command
	// left there -- and the slot's SHA is exactly what the fetch would put in
	// it, read a moment ago and real.
	//
	// That read is itself an `ls-remote`, so `backup restore --dry-run` does
	// contact the remote. It is the one network read a preview here makes, it
	// is stated rather than hidden, and whether the effects regime should carry
	// network READS at all is the framework question this and the backup fetch
	// both wait on.
	if flags.dryRun {
		if err := mintSlotFetch(flags, remote, slot); err != nil {
			die(exitcode.General, fmt.Sprintf("recording the fetch: %v", err))
		}
		if _, _, err := runBackupGit(flags, "ref:HEAD", "merge", "--ff-only", slotSHA); err != nil {
			die(exitcode.General, fmt.Sprintf("recording the fast-forward: %v", err))
		}
		infof(flags, "Would fast-forward %s from backup slot %s on %s (%s -> %s)\n",
			branch, slot, remote, oldHead[:12], slotSHA[:12])
		infof(flags, "  equivalent git commands: git fetch %s %s && git merge --ff-only FETCH_HEAD\n", remote, slot)
		infof(flags, "Dry run: no changes made.\n")
		return 0
	}

	if err := mintSlotFetch(flags, remote, slot); err != nil {
		die(exitcode.General, fmt.Sprintf("fetching %s from %s: %v", slot, remote, err))
	}
	fetched := resolveFetchHead(ctx)

	stdout, stderr, err := runBackupGit(flags, "ref:HEAD", "merge", "--ff-only", "FETCH_HEAD")
	if stdout != "" {
		outf(flags, "%s", stdout)
	}
	if err != nil {
		fmt.Fprint(os.Stderr, stderr)
		die(exitcode.General, fmt.Sprintf(
			"backup %s cannot be fast-forwarded onto %s: the branch has commits the backup does not contain\n"+
				"  slot: %s = %s\n"+
				"  HEAD: %s\n"+
				"inspect the difference with: git log --oneline %s..%s",
			slot, branch, slot, fetched[:12], oldHead[:12], oldHead[:12], fetched[:12]))
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "backup-restore",
		Extra: map[string]interface{}{
			"remote": remote,
			"branch": branch,
			"ref":    slot,
			"from":   oldHead,
			"to":     fetched,
		},
	})

	infof(flags, "  %s restored from %s %s (%s -> %s)\n", branch, remote, slot, oldHead[:12], fetched[:12])
	return 0
}
