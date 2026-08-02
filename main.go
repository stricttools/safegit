package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
	"github.com/smm-h/strictcli/go/strictcli"
)

// Set via -ldflags "-X main.version=..." at build time.
// Falls back to the module version embedded by go install.
var version = ""

// jsonEmitted tracks whether emitJSON has been called, so die() knows
// whether a JSON error envelope is still needed.
var jsonEmitted bool

func init() {
	if version != "" {
		version = strings.TrimPrefix(version, "v")
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	} else {
		version = "dev"
	}
}

// globalFlags holds flags parsed before command dispatch.
type globalFlags struct {
	quiet   bool
	verbose bool
	dryRun  bool
	// yes pre-approves ordinary prompts. --json implies it, because a
	// machine-readable run has nobody to answer them.
	yes bool
	// yesExplicit records whether --yes was actually passed. Decisions that
	// only a human (or an agent that deliberately said so) may make -- such as
	// publishing a whole branch to a remote we cannot prove is private -- are
	// gated on this, so adding --json can never disarm them.
	yesExplicit bool
	configPath  string
	json        bool
}

func main() {
	app := strictcli.NewApp("safegit", version, "concurrency-safe git wrapper providing 20 commands for multi-agent use with atomic commits, oplog-based undo, and history rewriting",
		strictcli.WithHandshakeEnv(sessionIDEnvVar, "Claude Code session identifier set by the invoking agent session; scopes 'safegit undo' to operations this session performed and is recorded as a commit trailer"),
	)

	app.GlobalFlag(strictcli.BoolFlag("quiet", "suppress all informational output, only showing errors and results", strictcli.Short("q"), strictcli.Default(false)))
	app.GlobalFlag(strictcli.BoolFlag("verbose", "enable verbose output with detailed progress and diagnostic info", strictcli.Default(false)))
	app.GlobalFlag(strictcli.BoolFlag("dry-run", "preview what would happen without writing any changes to disk", strictcli.Short("n"), strictcli.Default(false)))
	app.GlobalFlag(strictcli.BoolFlag("yes", "automatically confirm all interactive prompts without asking", strictcli.Short("y"), strictcli.Default(false)))
	app.GlobalFlag(strictcli.StringFlag("config-file", "path to a custom safegit config file instead of the default location", strictcli.Default("")))
	app.GlobalFlag(strictcli.BoolFlag("json", "emit machine-readable JSON output to stdout instead of human text", strictcli.Default(false)))

	pt := func(ctx *strictcli.Context, name string, args []string, globals map[string]interface{}) int {
		gf := globalsToFlags(globals)
		switch name {
		case "checkout":
			return runCheckout(gf, args)
		case "merge":
			return runMerge(gf, args)
		case "rebase":
			return runRebase(gf, args)
		case "reset":
			return runReset(gf, args)
		case "bisect":
			return runBisect(gf, args)
		case "cherry-pick":
			return runGuardedPassthrough(gf, "cherry-pick", args)
		case "revert":
			return runGuardedPassthrough(gf, "revert", args)
		}
		return 1
	}

	app.Command("commit", "stage and commit specified files in a single atomic operation", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(kwargs)
		messages := kwargsStrSlice(kwargs["m"])
		var messageFile string
		if v := kwargs["F"]; v != nil {
			messageFile = v.(string)
		}
		var branch string
		if v := kwargs["branch"]; v != nil {
			branch = v.(string)
		}
		amend := kwargs["amend"].(bool)
		allowEmpty := kwargs["allow_empty"].(bool)
		trailers := kwargsStrSlice(kwargs["trailer"])
		files := kwargsStrSlice(kwargs["files"])
		runCommit(gf, messages, messageFile, branch, amend, allowEmpty, trailers, files)
		return strictcli.Exit(0)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("m", "commit message line; can be repeated to build multi-line messages", strictcli.Short("m"), strictcli.Repeatable(), strictcli.Unique(false)),
			strictcli.StringFlag("F", "read the full commit message body from a file instead of --m flags", strictcli.Short("F"), strictcli.Default(nil)),
			strictcli.StringFlag("branch", "commit the staged files onto a different branch without switching to it", strictcli.Default(nil)),
			strictcli.BoolFlag("amend", "amend the current HEAD commit by replacing it with updated content", strictcli.Default(false)),
			strictcli.BoolFlag("allow-empty", "allow creating a commit even when no files have been changed", strictcli.Default(false)),
			strictcli.StringFlag("trailer", "add a key-value trailer line to the commit message (repeatable)", strictcli.Repeatable(), strictcli.Unique(false)),
		),
		strictcli.WithArgs(
			strictcli.NewArg("files", "files to commit (supports hunk specs: file.go:1,3)", strictcli.ArgRequired(false), strictcli.Variadic()),
		),
	)
	app.Passthrough("checkout", "checkout a branch or ref with working-tree safety guards", pt)
	app.Passthrough("merge", "merge a branch into HEAD with working-tree safety guards", pt)
	app.Passthrough("rebase", "rebase current branch onto upstream with safety guards", pt)
	app.Passthrough("reset", "reset HEAD with guards that prevent accidental --hard data loss", pt)
	app.Passthrough("bisect", "binary search through commits to find a bug, with safety guards", pt)
	app.Command("push", "push refs to remote with pre-pre-push hooks and automatic retry", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(kwargs)
		prePushHook := kwargs["pre_push_hook"].(bool)
		forceWithLease := kwargs["force_with_lease"].(bool)
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		onlyHead := kwargs["only_head"].(bool)
		onlyBranches := kwargs["only_branches"].(bool)
		onlyTags := kwargs["only_tags"].(bool)
		bothBranchesAndTags := kwargs["both_branches_and_tags"].(bool)
		var mode pushMode
		switch {
		case onlyHead:
			mode = pushModeHead
		case onlyBranches:
			mode = pushModeBranches
		case onlyTags:
			mode = pushModeTags
		case bothBranchesAndTags:
			mode = pushModeBoth
		}
		return strictcli.Exit(runPush(gf, !prePushHook, forceWithLease, remote, mode))
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("pre-push-hook", "run pre-pre-push hook scripts before pushing to remote", strictcli.Default(true)),
			strictcli.BoolFlag("force-with-lease", "force push using --force-with-lease to prevent overwriting others' work", strictcli.Default(false)),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("only-head", "push only the current HEAD branch to the remote, ignoring other refs", strictcli.Default(false)),
				strictcli.BoolFlag("only-branches", "push all local branches to the remote, ignoring tags and other refs", strictcli.Default(false)),
				strictcli.BoolFlag("only-tags", "push all local tags to the remote without pushing any branches", strictcli.Default(false)),
				strictcli.BoolFlag("both-branches-and-tags", "push all local branches and all tags to the remote in one operation", strictcli.Default(false)),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to push to (defaults to origin)", strictcli.ArgRequired(false)),
		),
	)
	app.Command("pull", "fetch from remote and merge, defaulting to fast-forward-only mode", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(kwargs)
		// Determine merge mode from --merge-strategy
		var mode pullMode
		switch kwargs["merge_strategy"].(string) {
		case "ff-only":
			mode = pullFFOnly
		case "ff":
			mode = pullFF
		case "no-ff":
			mode = pullNoFF
		}
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		var branch string
		if v := kwargs["branch"]; v != nil {
			branch = v.(string)
		}
		return strictcli.Exit(runPull(gf, mode, remote, branch))
	},
		strictcli.WithFlags(
			strictcli.StringFlag("merge-strategy", "fast-forward merge strategy: ff, ff-only, or no-ff", strictcli.Choices("ff", "ff-only", "no-ff")),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to pull from (defaults to origin)", strictcli.ArgRequired(false)),
			strictcli.NewArg("branch", "name of the remote branch to fetch and merge into the current branch", strictcli.ArgRequired(false)),
		),
	)
	bg := app.Group("backup", "push, list, and restore per-branch history backups held in the tool-owned refs/backups namespace on a remote, so uncommitted-to-the-world work survives a lost machine without ever touching refs/heads")
	bg.Command("backup", "push the current branch to its backup slot refs/backups/<branch> on the remote, after fetching that slot and refusing when it holds commits your history does not contain; the push is pinned with --force-with-lease to the exact SHA that was just observed (or to \"this ref must not exist\" for a first backup), so a concurrent backup from another machine is rejected rather than clobbered; plain git equivalent: git push --force-with-lease=refs/backups/<branch>:<observed-sha> <remote> HEAD:refs/backups/<branch>", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		overwrite := kwargs["overwrite_remote_backup"].(bool)
		return strictcli.Exit(runBackupCreate(globalsToFlags(kwargs), remote, overwrite))
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("overwrite-remote-backup", "replace a backup slot whose commits are missing from your current history, leasing on the SHA observed during this run; without this flag such a slot is a hard error because overwriting it would drop work backed up from elsewhere", strictcli.Default(false)),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgRequired(false)),
		),
	)
	bg.Command("list", "list every backup slot present on the remote with the branch name and the commit each slot points at, so you can see which branches are backed up from which machine before restoring one; plain git equivalent: git ls-remote <remote> 'refs/backups/*'", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		return strictcli.Exit(runBackupList(globalsToFlags(kwargs), remote))
	},
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgRequired(false)),
		),
	)
	bg.Command("restore", "fetch the current branch's backup slot from the remote and fast-forward the branch onto it, refusing when the local branch carries commits the backup does not contain so no local work is ever discarded; plain git equivalent: git fetch <remote> refs/backups/<branch> && git merge --ff-only FETCH_HEAD", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		return strictcli.Exit(runBackupRestore(globalsToFlags(kwargs), remote))
	},
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgRequired(false)),
		),
	)
	cg := app.Group("config", "show, get, or set safegit configuration key-value pairs")
	cg.Command("show", "show all configuration values currently in effect for this repository, including built-in defaults and any user overrides from the .git/safegit/config.json file, printed as key-value pairs to stdout for inspection and debugging purposes", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runConfigShow(globalsToFlags(kwargs)))
	})
	cg.Command("get", "get the current value of a single configuration key from the .git/safegit/config.json file, printing the raw value to stdout so it can be captured by scripts or used in automation pipelines", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		key := kwargs["key"].(string)
		return strictcli.Exit(runConfigGet(globalsToFlags(kwargs), key))
	},
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key whose current value should be retrieved")),
	)
	cg.Command("set", "set a configuration key to a new value in the .git/safegit/config.json file, creating the file if it does not exist yet, and persisting the change for all future safegit invocations in this repository", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		key := kwargs["key"].(string)
		value := kwargs["value"].(string)
		return strictcli.Exit(runConfigSet(globalsToFlags(kwargs), key, value))
	},
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key to set to the specified value in config.json"), strictcli.NewArg("value", "the new value to assign to the specified configuration key")),
	)

	hg := app.Group("hook", "manage pre-pre-push hook scripts that run before every push")
	hg.Command("list", "list all pre-pre-push hooks currently installed in the .git/safegit/hooks directory, showing each hook name, file path, and whether it is executable, so you can audit which checks run before every push", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(hookList(globalsToFlags(kwargs)))
	})
	hg.Command("run", "run all installed pre-pre-push hooks (or a single named hook) immediately without performing an actual push, so you can verify that all configured hooks pass before committing to a real push operation", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		var name string
		if v := kwargs["name"]; v != nil {
			name = v.(string)
		}
		return strictcli.Exit(hookRun(globalsToFlags(kwargs), name))
	},
		strictcli.WithArgs(strictcli.NewArg("name", "name of a specific hook to run; omit to run all installed hooks", strictcli.ArgRequired(false))),
	)
	hg.Command("install", "install a pre-pre-push hook by copying a script file into the .git/safegit/hooks directory, making it executable, and registering it so that safegit push will run it before any network I/O occurs", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		path := kwargs["path"].(string)
		return strictcli.Exit(hookInstall(globalsToFlags(kwargs), path))
	},
		strictcli.WithArgs(strictcli.NewArg("path", "filesystem path to the hook script file to install into safegit")),
	)
	app.Command("doctor", "run diagnostic health checks on the repository and optionally repair issues", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		runDoctor(globalsToFlags(kwargs), kwargs)
		return strictcli.Exit(0)
	},
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("diagnose", "run all health checks and report results without fixing any issues", strictcli.Default(false)),
				strictcli.BoolFlag("fix", "run all health checks and automatically repair any issues found", strictcli.Default(false)),
				strictcli.BoolFlag("uninstall", "remove all safegit hooks and metadata from this repository entirely", strictcli.Default(false)),
			},
		}),
	)
	ag := app.Group("author", "audit and rewrite commit author/committer identity — list all identities, check against expected values, and rewrite name or email across history")
	ag.Command("list", "list all distinct author and committer identities across the entire commit history, showing name, email, role, and commit count for each unique identity — useful for auditing repositories with multiple contributors or detecting unwanted identity variations such as typos, old email addresses, or bot accounts that should be consolidated before a rewrite", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runAuthorList(globalsToFlags(kwargs)))
	},
		strictcli.WithTags("json"),
	)
	ag.Command("check", "check that all commits use the expected author and committer identity by scanning every commit in the repository history, reporting any deviations with the exact commit hashes and mismatched fields, and suggesting the corresponding safegit author rewrite command to fix each deviation found", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runAuthorCheck(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("name", "expected author and committer display name that all commits should use", strictcli.Default(nil)),
			strictcli.StringFlag("email", "expected author and committer email address that all commits should use", strictcli.Default(nil)),
		),
	)
	ag.Command("rewrite", "rewrite author and committer name or email across all commit history using git filter-branch style rewriting, replacing every occurrence of the old identity with the new one in both author and committer fields while preserving timestamps, commit messages, tree contents, and parent relationships so the rewritten history is otherwise identical to the original", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runRewriteAuthor(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("old-name", "current author or committer display name to search for and replace", strictcli.Default(nil)),
			strictcli.StringFlag("new-name", "new display name to substitute wherever the old name is found in history", strictcli.Default(nil)),
			strictcli.StringFlag("old-email", "current author or committer email address to search for and replace", strictcli.Default(nil)),
			strictcli.StringFlag("new-email", "new email address to substitute wherever the old email is found in history", strictcli.Default(nil)),
		),
		strictcli.WithDependencies(
			strictcli.CoRequired{Flags: []string{"old-name", "new-name"}},
			strictcli.CoRequired{Flags: []string{"old-email", "new-email"}},
		),
	)
	app.Deprecated("rewrite-author", "use 'safegit author rewrite' instead")
	sg := app.Group("scrub", "surgically rewrite git history to remove or replace sensitive content using 4 subcommands (file, match, run, verify) that operate on all commits, trees, and blobs in the repository")
	sg.Command("file", "replace or remove a specific file across all commits in the repository history, rewriting each affected commit tree to either substitute the file contents with a sanitized version or delete the file entirely from every historical snapshot", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubFile(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("from", "first commit hash to include when rewriting history (default: root commit)"),
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true)),
		),
		strictcli.WithArgs(
			strictcli.NewArg("file", "repository-relative path to the file that should be scrubbed from history"),
		),
	)
	sg.Command("match", "replace all occurrences of a regex pattern across every blob in the repository history, rewriting commit trees to substitute matched text with a replacement string so that sensitive values like secrets and credentials are permanently removed from all historical snapshots", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubMatch(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all blobs in history"),
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
			strictcli.StringFlag("scope", "glob pattern limiting which file paths are searched (e.g. '*.env', 'config/**')", strictcli.Default(nil)),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true)),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("replace", "literal string to substitute for each regex match found in history", strictcli.Default(nil)),
				strictcli.BoolFlag("mangle", "replace matches with random printable ASCII of same length", strictcli.Default(false)),
			},
		}),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("from", "first commit hash to include when rewriting history (default: root commit)", strictcli.Default(nil)),
				strictcli.BoolFlag("entire-history", "rewrite all commits from the root of the repository to HEAD", strictcli.Default(false)),
			},
		}),
	)
	sg.Command("run", "execute a multi-operation scrub recipe from a TOML file, applying all pattern replacements and file removals across history in a single coordinated pass with topological commit ordering, overlap detection between operations, and automatic verification that no matched content survives in the rewritten object store — use --diff to preview all changes as unified diffs before committing to the rewrite", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubRun(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
			strictcli.BoolFlag("diff", "preview what would change without modifying any objects, showing unified diffs", strictcli.Default(false)),
			strictcli.IntFlag("limit", "maximum number of blob diffs to show in --diff mode (default: 50)", strictcli.Default(50)),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true)),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("from", "first commit hash to include when rewriting history", strictcli.Default(nil)),
				strictcli.BoolFlag("entire-history", "rewrite all commits from the root of the repository to HEAD", strictcli.Default(false)),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("recipe", "path to the TOML recipe file containing scrub operations"),
		),
	)
	sg.Command("verify", "check all scrub policies defined in the repository configuration to confirm that previously scrubbed secrets and sensitive patterns remain completely absent from every object in the git object store, scanning blobs, commit messages, and tag annotations and reporting detailed per-policy pass or fail results with match locations for any violations found", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubVerify(globalsToFlags(kwargs)))
	},
		strictcli.WithTags("json"),
	)
	app.Passthrough("cherry-pick", "cherry-pick one or more commits onto HEAD with safety guards", pt)
	app.Passthrough("revert", "revert one or more commits creating inverse patches, with safety guards", pt)
	app.Command("undo", "reverse the last commit, amend, or reword operation using the oplog", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		bypassSession := kwargs["bypass_session"].(bool)
		count := kwargs["count"].(int)
		// Read the session handshake through the framework accessor so the
		// dependency is declared rather than an ambient os.Getenv.
		sessionID, _ := ctx.InfraValue(sessionIDEnvVar)
		runUndo(globalsToFlags(kwargs), bypassSession, count, sessionID)
		return strictcli.Exit(0)
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "undo across all sessions by ignoring the session ID ownership check", strictcli.Default(false)),
			strictcli.IntFlag("count", "number of oplog operations to undo in a single invocation", strictcli.Default(1)),
		),
	)
	app.Command("unlock", "release a stale .lock file left behind by a crashed git process", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		ref := kwargs["ref"].(string)
		return strictcli.Exit(runUnlock(globalsToFlags(kwargs), ref))
	},
		strictcli.WithArgs(strictcli.NewArg("ref", "the ref name (e.g. refs/heads/main) whose stale .lock file to remove")),
	)
	app.Command("scan", "search git history for regex pattern matches across all objects and working tree files, scanning blobs, commit messages, tag annotations, and trailers with optional scope filtering and commit range selection", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScan(globalsToFlags(kwargs), kwargs))
	},
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all objects in history"),
			strictcli.StringFlag("scope", "glob pattern limiting which blob file paths are included (e.g. '*.env', 'config/**')", strictcli.Default(nil)),
			strictcli.StringFlag("from", "first commit hash to include when scanning history (mutually exclusive with --entire-history)", strictcli.Default(nil)),
			strictcli.BoolFlag("entire-history", "scan all commits from the root of the repository to HEAD (mutually exclusive with --from)", strictcli.Default(false)),
			strictcli.StringFlag("target", "comma-separated list of match types to include: blobs,commits,tags,trailers,files (default: all)", strictcli.Default(nil)),
		),
	)
	app.Command("version", "print safegit version, Go runtime version, and git version", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		runVersion(globalsToFlags(kwargs))
		return strictcli.Exit(0)
	})

	app.Run()
}

// kwargsStrSlice converts a []interface{} value (from repeatable flags or
// variadic args) to []string. Returns nil if v is nil.
func kwargsStrSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	raw := v.([]interface{})
	out := make([]string, len(raw))
	for i, elem := range raw {
		out[i] = elem.(string)
	}
	return out
}

// globalsToFlags converts the strictcli globals map to the globalFlags struct.
// strictcli converts flag names like "dry-run" to map keys "dry_run".
func globalsToFlags(globals map[string]interface{}) globalFlags {
	gf := globalFlags{
		quiet:       globals["quiet"].(bool),
		verbose:     globals["verbose"].(bool),
		dryRun:      globals["dry_run"].(bool),
		yes:         globals["yes"].(bool),
		yesExplicit: globals["yes"].(bool),
		configPath:  globals["config_file"].(string),
		json:        globals["json"].(bool),
	}
	if gf.json {
		gf.quiet = true
		gf.yes = true
	}
	return gf
}

func runVersion(flags globalFlags) {
	gitVer := gitVersion()
	fmt.Printf("safegit %s\n", version)
	fmt.Printf("go      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("git     %s\n", gitVer)
}

func gitVersion() string {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// loadConfig loads the safegit config, using the override path if --config-file was set.
func loadConfig(flags globalFlags, gitDir string) (*repo.Config, error) {
	if flags.configPath != "" {
		return repo.LoadConfigFrom(flags.configPath)
	}
	return repo.LoadConfig(gitDir)
}

// mustGitDir resolves the .git directory or exits with an error.
func mustGitDir(flags globalFlags, cmd string) string {
	ctx := context.Background()
	gitDir, err := git.GitDir(ctx)
	if err != nil {
		die(flags, cmd, 3, "not a git repository (or git is not installed)")
	}
	// Resolve to absolute path
	abs, err := filepath.Abs(gitDir)
	if err != nil {
		abs = gitDir
	}
	return abs
}

// confirmOrAbort prompts the user for confirmation, returning true if
// confirmed (via --yes or interactive y/Y) and false otherwise.
func confirmOrAbort(flags globalFlags, format string, args ...interface{}) bool {
	return confirmWith(flags.yes, format, args...)
}

// confirmDeliberate is confirmOrAbort for decisions that a machine-readable run
// must never answer on the operator's behalf: only an explicit --yes
// pre-approves them, never the --yes that --json implies.
func confirmDeliberate(flags globalFlags, format string, args ...interface{}) bool {
	if flags.yesExplicit {
		return true
	}
	if flags.json {
		// A --json run has nobody to prompt, and a dangling prompt would land
		// in the JSON stream. Refuse, and name the flag that consents.
		fmt.Fprintf(os.Stderr, "refusing: "+format+"\n", args...)
		fmt.Fprintf(os.Stderr, "          --json does not answer this confirmation; pass --yes to consent deliberately\n")
		return false
	}
	return confirmWith(false, format, args...)
}

// confirmWith prompts unless the decision was already pre-approved.
func confirmWith(preApproved bool, format string, args ...interface{}) bool {
	if preApproved {
		return true
	}
	fmt.Printf("\n"+format+" [y/N] ", args...)
	var answer string
	fmt.Scanln(&answer)
	return answer == "y" || answer == "Y"
}

// infof prints a formatted message unless quiet mode is active.
func infof(flags globalFlags, format string, args ...interface{}) {
	if !flags.quiet {
		fmt.Printf(format, args...)
	}
}

// emitJSON marshals v as indented JSON to stdout. Exits on marshal error.
func emitJSON(v interface{}) {
	jsonEmitted = true
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshaling JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// requireCleanTree dies if the working tree has uncommitted changes.
func requireCleanTree(ctx context.Context, flags globalFlags, cmd string) {
	statusOut, _, err := git.Run(ctx, "status", "--porcelain")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("checking working tree: %v", err))
	}
	if strings.TrimSpace(statusOut) != "" {
		die(flags, cmd, 1, "working tree is dirty; commit changes before proceeding")
	}
}

// commandHelp prints per-command help and exits.
func commandHelp(cmd, usage string) {
	fmt.Fprintf(os.Stderr, "Usage: safegit %s\n\n%s\n", cmd, usage)
	os.Exit(0)
}

// die prints an error for the given subcommand and exits with code.
// When --json is active and no JSON has been emitted yet, it also writes
// a JSON error envelope to stdout so callers always get structured output.
func die(flags globalFlags, cmd string, code int, msg string) {
	if flags.json && !jsonEmitted {
		emitJSON(struct {
			Error string `json:"error"`
		}{Error: msg})
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	os.Exit(code)
}

// refShortName strips the refs/heads/ prefix from a ref for display.
func refShortName(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// firstLine returns the first line of a multi-line string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// parseFileSpecs converts raw file arguments (possibly with hunk suffixes like
// "file.txt:1,3") into commit.FileSpec structs. Dies on malformed hunk specs.
func parseFileSpecs(files []string, flags globalFlags, cmd string) []commit.FileSpec {
	specs := make([]commit.FileSpec, 0, len(files))
	for _, f := range files {
		spec := commit.FileSpec{}
		colonIdx := strings.LastIndex(f, ":")
		// Only attempt hunk parsing when:
		// 1. There is a colon (not at position 0)
		// 2. The suffix looks like a hunk spec
		// 3. The full string doesn't exist as a file (avoids misidentifying "1:2" as hunk spec)
		if colonIdx > 0 && isHunkSpec(f[colonIdx+1:]) && !fileExists(f) {
			hunks, err := stage.ParseHunkSpec(f[colonIdx+1:])
			if err != nil {
				die(flags, cmd, 2, fmt.Sprintf("invalid hunk spec in %q: %v", f, err))
			}
			spec.Path = f[:colonIdx]
			spec.Hunks = hunks
		} else {
			spec.Path = f
		}
		specs = append(specs, spec)
	}
	return specs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isHunkSpec returns true if s looks like a hunk specifier (digits, commas, dashes only).
func isHunkSpec(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c != ',' && c != '-' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

