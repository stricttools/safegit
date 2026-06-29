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
	quiet      bool
	verbose    bool
	dryRun     bool
	yes        bool
	configPath string
	json       bool
}

func main() {
	app := strictcli.NewApp("safegit", version, "concurrency-safe git for multi-agent use")

	app.GlobalFlag(strictcli.BoolFlag("quiet", "suppress all informational output, only showing errors and results", strictcli.Short("q")))
	app.GlobalFlag(strictcli.BoolFlag("verbose", "enable verbose output with detailed progress and diagnostic info"))
	app.GlobalFlag(strictcli.BoolFlag("dry-run", "preview what would happen without writing any changes to disk", strictcli.Short("n")))
	app.GlobalFlag(strictcli.BoolFlag("yes", "automatically confirm all interactive prompts without asking", strictcli.Short("y")))
	app.GlobalFlag(strictcli.StringFlag("config", "path to a custom safegit config file instead of the default location", strictcli.Default("")))
	app.GlobalFlag(strictcli.BoolFlag("json", "emit machine-readable JSON output to stdout instead of human text"))

	pt := func(name string, args []string, globals map[string]interface{}) int {
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

	app.Command("commit", "stage and commit specified files in a single atomic operation", func(kwargs map[string]interface{}) int {
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
		return 0
	},
		strictcli.WithFlags(
			strictcli.StringFlag("m", "commit message line; can be repeated to build multi-line messages", strictcli.Short("m"), strictcli.Repeatable()),
			strictcli.StringFlag("F", "read the full commit message body from a file instead of --m flags", strictcli.Short("F"), strictcli.Default(nil)),
			strictcli.StringFlag("branch", "commit the staged files onto a different branch without switching to it", strictcli.Default(nil)),
			strictcli.BoolFlag("amend", "amend the current HEAD commit by replacing it with updated content"),
			strictcli.BoolFlag("allow-empty", "allow creating a commit even when no files have been changed"),
			strictcli.StringFlag("trailer", "add a key-value trailer line to the commit message (repeatable)", strictcli.Repeatable()),
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
	app.Command("push", "push refs to remote with pre-pre-push hooks and automatic retry", func(kwargs map[string]interface{}) int {
		gf := globalsToFlags(kwargs)
		noPrePrePush := kwargs["no_pre_pre_push"].(bool)
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
		return runPush(gf, noPrePrePush, forceWithLease, remote, mode)
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("no-pre-pre-push", "skip running pre-pre-push hook scripts before pushing to remote"),
			strictcli.BoolFlag("force-with-lease", "force push using --force-with-lease to prevent overwriting others' work"),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("only-head", "push only the current HEAD branch to the remote, ignoring other refs"),
				strictcli.BoolFlag("only-branches", "push all local branches to the remote, ignoring tags and other refs"),
				strictcli.BoolFlag("only-tags", "push all local tags to the remote without pushing any branches"),
				strictcli.BoolFlag("both-branches-and-tags", "push all local branches and all tags to the remote in one operation"),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to push to (defaults to origin)", strictcli.ArgRequired(false)),
		),
	)
	app.Command("pull", "fetch from remote and merge, defaulting to fast-forward-only mode", func(kwargs map[string]interface{}) int {
		gf := globalsToFlags(kwargs)
		// Determine merge mode from mutex group
		var mode pullMode
		if kwargs["ff_only"].(bool) {
			mode = pullFFOnly
		} else if kwargs["ff"].(bool) {
			mode = pullFF
		} else if kwargs["no_ff"].(bool) {
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
		return runPull(gf, mode, remote, branch)
	},
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("ff-only", "fast-forward only; abort with an error if a merge commit is required"),
				strictcli.BoolFlag("ff", "fast-forward when possible, otherwise create a merge commit automatically"),
				strictcli.BoolFlag("no-ff", "always create a merge commit even when fast-forward is possible"),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to pull from (defaults to origin)", strictcli.ArgRequired(false)),
			strictcli.NewArg("branch", "name of the remote branch to fetch and merge into the current branch", strictcli.ArgRequired(false)),
		),
	)
	cg := app.Group("config", "show, get, or set safegit configuration key-value pairs")
	cg.Command("show", "show all configuration values currently in effect for this repository, including built-in defaults and any user overrides from the .git/safegit/config.json file, printed as key-value pairs to stdout for inspection and debugging purposes", func(kwargs map[string]interface{}) int {
		return runConfigShow(globalsToFlags(kwargs))
	})
	cg.Command("get", "get the current value of a single configuration key from the .git/safegit/config.json file, printing the raw value to stdout so it can be captured by scripts or used in automation pipelines", func(kwargs map[string]interface{}) int {
		key := kwargs["key"].(string)
		return runConfigGet(globalsToFlags(kwargs), key)
	},
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key whose current value should be retrieved")),
	)
	cg.Command("set", "set a configuration key to a new value in the .git/safegit/config.json file, creating the file if it does not exist yet, and persisting the change for all future safegit invocations in this repository", func(kwargs map[string]interface{}) int {
		key := kwargs["key"].(string)
		value := kwargs["value"].(string)
		return runConfigSet(globalsToFlags(kwargs), key, value)
	},
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key to set to the specified value in config.json"), strictcli.NewArg("value", "the new value to assign to the specified configuration key")),
	)

	hg := app.Group("hook", "manage pre-pre-push hook scripts that run before every push")
	hg.Command("list", "list all pre-pre-push hooks currently installed in the .git/safegit/hooks directory, showing each hook name, file path, and whether it is executable, so you can audit which checks run before every push", func(kwargs map[string]interface{}) int {
		return hookList(globalsToFlags(kwargs))
	})
	hg.Command("run", "run all installed pre-pre-push hooks (or a single named hook) immediately without performing an actual push, so you can verify that all configured hooks pass before committing to a real push operation", func(kwargs map[string]interface{}) int {
		var name string
		if v := kwargs["name"]; v != nil {
			name = v.(string)
		}
		return hookRun(globalsToFlags(kwargs), name)
	},
		strictcli.WithArgs(strictcli.NewArg("name", "name of a specific hook to run; omit to run all installed hooks", strictcli.ArgRequired(false))),
	)
	hg.Command("install", "install a pre-pre-push hook by copying a script file into the .git/safegit/hooks directory, making it executable, and registering it so that safegit push will run it before any network I/O occurs", func(kwargs map[string]interface{}) int {
		path := kwargs["path"].(string)
		return hookInstall(globalsToFlags(kwargs), path)
	},
		strictcli.WithArgs(strictcli.NewArg("path", "filesystem path to the hook script file to install into safegit")),
	)
	app.Command("doctor", "run diagnostic health checks on the repository and optionally repair issues", func(kwargs map[string]interface{}) int {
		runDoctor(globalsToFlags(kwargs), kwargs)
		return 0
	},
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("diagnose", "run all health checks and report results without fixing any issues"),
				strictcli.BoolFlag("fix", "run all health checks and automatically repair any issues found"),
				strictcli.BoolFlag("uninstall", "remove all safegit hooks and metadata from this repository entirely"),
			},
		}),
	)
	ag := app.Group("author", "audit and rewrite commit author/committer identity")
	ag.Command("list", "list all distinct author and committer identities across the entire commit history, showing name, email, role, and commit count for each unique identity", func(kwargs map[string]interface{}) int {
		return runAuthorList(globalsToFlags(kwargs))
	})
	ag.Command("check", "check that all commits use the expected author and committer identity, reporting any deviations and suggesting a rewrite command to fix them", func(kwargs map[string]interface{}) int {
		return runAuthorCheck(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("name", "expected author and committer display name that all commits should use", strictcli.Default(nil)),
			strictcli.StringFlag("email", "expected author and committer email address that all commits should use", strictcli.Default(nil)),
		),
	)
	ag.Command("rewrite", "rewrite author and committer name or email across all commit history", func(kwargs map[string]interface{}) int {
		return runRewriteAuthor(globalsToFlags(kwargs), kwargs)
	},
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
	sg := app.Group("scrub", "surgically rewrite git history to remove or replace sensitive content such as secrets, credentials, and private data from all commits, trees, and blobs in the repository")
	sg.Command("file", "replace or remove a specific file across all commits in the repository history, rewriting each affected commit tree to either substitute the file contents with a sanitized version or delete the file entirely from every historical snapshot", func(kwargs map[string]interface{}) int {
		return runScrubFile(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("from", "first commit hash to include when rewriting history (default: root commit)"),
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
		),
		strictcli.WithArgs(
			strictcli.NewArg("file", "repository-relative path to the file that should be scrubbed from history"),
		),
	)
	sg.Command("match", "replace all occurrences of a regex pattern across every blob in the repository history, rewriting commit trees to substitute matched text with a replacement string so that sensitive values like secrets and credentials are permanently removed from all historical snapshots", func(kwargs map[string]interface{}) int {
		return runScrubMatch(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all blobs in history"),
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
			strictcli.StringFlag("scope", "glob pattern limiting which file paths are searched (e.g. '*.env', 'config/**')", strictcli.Default(nil)),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("replace", "literal string to substitute for each regex match found in history", strictcli.Default(nil)),
				strictcli.BoolFlag("mangle", "replace matches with random printable ASCII of same length"),
			},
		}),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("from", "first commit hash to include when rewriting history (default: root commit)", strictcli.Default(nil)),
				strictcli.BoolFlag("entire-history", "rewrite all commits from the root of the repository to HEAD"),
			},
		}),
	)
	sg.Command("run", "execute a multi-operation scrub recipe from a TOML file, applying all pattern replacements across history in a single coordinated pass with topological ordering and overlap detection", func(kwargs map[string]interface{}) int {
		return runScrubRun(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed"),
			strictcli.BoolFlag("diff", "preview what would change without modifying any objects, showing unified diffs"),
			strictcli.IntFlag("limit", "maximum number of blob diffs to show in --diff mode (default: 50)", strictcli.Default(50)),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("from", "first commit hash to include when rewriting history", strictcli.Default(nil)),
				strictcli.BoolFlag("entire-history", "rewrite all commits from the root of the repository to HEAD"),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("recipe", "path to the TOML recipe file containing scrub operations"),
		),
	)
	sg.Command("verify", "check all scrub policies to confirm that previously scrubbed secrets remain absent from the git object store, reporting per-policy pass or fail results", func(kwargs map[string]interface{}) int {
		return runScrubVerify(globalsToFlags(kwargs))
	})
	app.Passthrough("cherry-pick", "cherry-pick one or more commits onto HEAD with safety guards", pt)
	app.Passthrough("revert", "revert one or more commits creating inverse patches, with safety guards", pt)
	app.Command("undo", "reverse the last commit, amend, or reword operation using the oplog", func(kwargs map[string]interface{}) int {
		bypassSession := kwargs["bypass_session"].(bool)
		runUndo(globalsToFlags(kwargs), bypassSession)
		return 0
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "undo across all sessions by ignoring the session ID ownership check"),
		),
	)
	app.Command("redo", "restore the commit that undo removed, as a one-shot counterpart to undo", func(kwargs map[string]interface{}) int {
		bypassSession := kwargs["bypass_session"].(bool)
		runRedo(globalsToFlags(kwargs), bypassSession)
		return 0
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "redo across all sessions by ignoring the session ID ownership check"),
		),
	)
	app.Command("unlock", "release a stale .lock file left behind by a crashed git process", func(kwargs map[string]interface{}) int {
		ref := kwargs["ref"].(string)
		return runUnlock(globalsToFlags(kwargs), ref)
	},
		strictcli.WithArgs(strictcli.NewArg("ref", "the ref name (e.g. refs/heads/main) whose stale .lock file to remove")),
	)
	app.Command("scan", "search git history for regex pattern matches across all objects and working tree files", func(kwargs map[string]interface{}) int {
		return runScan(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all objects in history"),
			strictcli.StringFlag("scope", "glob pattern limiting which blob file paths are included (e.g. '*.env', 'config/**')", strictcli.Default(nil)),
			strictcli.StringFlag("from", "first commit hash to include when scanning history (mutually exclusive with --entire-history)", strictcli.Default(nil)),
			strictcli.BoolFlag("entire-history", "scan all commits from the root of the repository to HEAD (mutually exclusive with --from)"),
			strictcli.StringFlag("target", "comma-separated list of match types to include: blobs,commits,tags,trailers,files (default: all)", strictcli.Default(nil)),
		),
	)
	app.Command("version", "print safegit version, Go runtime version, and git version", func(kwargs map[string]interface{}) int {
		runVersion(globalsToFlags(kwargs))
		return 0
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
		quiet:      globals["quiet"].(bool),
		verbose:    globals["verbose"].(bool),
		dryRun:     globals["dry_run"].(bool),
		yes:        globals["yes"].(bool),
		configPath: globals["config"].(string),
		json:       globals["json"].(bool),
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

// loadConfig loads the safegit config, using the override path if --config was set.
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
	if flags.yes {
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

