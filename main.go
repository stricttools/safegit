package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// Set via -ldflags "-X main.version=..." at build time.
// Falls back to the module version embedded by go install.
var version = ""

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

// globalFlags holds the values every command handler needs: the framework-owned
// reserved quartet (delivered on the Context, never as kwargs) plus safegit's
// own app-level flags.
type globalFlags struct {
	quiet   bool
	verbose bool
	dryRun  bool
	// approved records that --approve-consequential was actually passed.
	// Every prompt safegit raises gates something irreversible, so this is the
	// ONLY thing that pre-approves one: --json never implies it, and adding
	// --json to a command line can never disarm a confirmation.
	approved   bool
	configPath string
	json       bool
	// sc is the framework context for this dispatch. It is the only route to
	// ctx.Effects(), so carrying it here gives every existing handler the
	// effects handle without rewriting ~60 call signatures. Nil in unit tests
	// that never dispatch; effects() is the guarded accessor.
	sc *strictcli.Context
	// root caches this dispatch's repository root, resolved at most once. It is
	// a pointer so the cache survives globalFlags being copied by value into
	// every handler.
	root *executionRoot
}

// executionRoot resolves the repository root once per dispatch.
type executionRoot struct {
	once sync.Once
	dir  string
}

// resolve returns the repository root, asking git at most once. A nil receiver
// (a globalFlags built by a unit test rather than by dispatch) resolves without
// caching rather than reporting no root at all.
func (r *executionRoot) resolve() string {
	if r == nil {
		return repoRootOrEmpty()
	}
	r.once.Do(func() { r.dir = repoRootOrEmpty() })
	return r.dir
}

// repoRootOrEmpty asks git for the top of the working tree, starting from the
// OPERATOR'S own directory -- discovery is the one thing the pin cannot itself
// be applied to. Something with no working tree (a bare repository, or no
// repository at all) has no root; the empty string means "no pin", which
// gitexec.WithRoot reads as leaving the context unchanged.
func repoRootOrEmpty() string {
	root, err := git.RepoRoot(context.Background())
	if err != nil {
		return ""
	}
	return root
}

// ctx builds this dispatch's execution context, and is the ONE place a context
// for a git call is created: a bare context.Background() in a handler is a git
// call that escaped the pin.
//
// Every git subprocess safegit itself constructs from this context runs with
// its working directory pinned to the repository root. A large part of git's
// plumbing vocabulary is scoped to the process working directory -- `ls-files`
// defaults to the pathspec ".", `ls-tree` prefixes the current directory onto
// the tree it reads, `apply` resolves the paths inside a patch against it -- so
// without the pin an operator invoking safegit from a subdirectory silently
// narrows what safegit sees, what it protects and what it rewrites.
//
// User-typed relative PATH ARGUMENTS are unaffected: they are canonicalized
// against the invoking directory at intake, before any git call is built.
//
// The sites that cannot take the pin are not decided here: they are enumerated
// in internal/gitexec's declared directory-pin exemption table.
// A dry run additionally marks the context as a PREVIEW, which is what lets
// the boundary refuse an object-writing git invocation that is running without
// an object quarantine. The mark is set here, for every command at once,
// because every command's dry run makes the same promise; installing the
// quarantine itself stays with the commands that actually write objects in a
// preview (see internal/commit's BeginPreview), since only they know when the
// throwaway store can be created and cleaned up.
func (g globalFlags) ctx() context.Context {
	ctx := gitexec.WithRoot(context.Background(), g.root.resolve())
	if g.dryRun {
		ctx = gitexec.WithPreview(ctx)
	}
	return ctx
}

// effects returns the effects handle for this dispatch. Handlers mint every
// mutation through it so that --dry-run records instead of executing.
func (g globalFlags) effects() *strictcli.Effects { return g.sc.Effects() }

// payload supplies this dispatch's machine payload. The framework validates it
// against the command's declared schema and emits it as the envelope's payload
// member under --json; outside machine mode it is not printed at all, so the
// call is unconditional and handlers never branch on the mode to build it.
//
// Only commands that declare a PayloadSchema may call this: without a
// declaration the framework has nothing to validate against and refuses.
func (g globalFlags) payload(value interface{}) {
	if g.sc == nil {
		return
	}
	g.sc.Payload(value)
}

// silent reports that safegit must not write human text to stdout.
//
// Two independent reasons, and neither is the other: --quiet is the operator
// asking for silence, and machine mode is stdout being owned by the framework's
// envelope. safegit's handlers print with fmt.Printf rather than through the
// context writers, so those writes bypass the framework entirely (contract
// §19.1's accepted ceiling) and would land beside the envelope as a second
// document. Machine mode therefore suppresses them here -- it does NOT set
// quiet: ctx.Quiet() keeps reporting exactly what the operator passed.
func (g globalFlags) silent() bool { return g.quiet || g.json }

func main() {
	newApp().Run()
}

// The scrub commands' two member-spelled selectors, declared once because
// `scrub match` and `scrub run` share the range selection and because the
// handlers in scrub_match.go and scrub_run.go identify the elected choice
// against these very values.
//
// Member spelling keeps the flags an operator types exactly as they were --
// `--replace <s>`, `--mangle`, `--from <sha>`, `--entire-history` -- while the
// selector, not a hand-written guard, is what makes exactly one of each pair
// mandatory. Each member flag declares Required(), read as "required once this
// member is elected".
var (
	scrubReplaceChoice = strictcli.MemberChoice(
		strictcli.StringFlag("replace", "literal string to substitute for each regex match found in history", strictcli.Required()),
		"substitute a literal string for every match")
	scrubMangleChoice = strictcli.MemberChoice(
		strictcli.BoolFlag("mangle", "replace matches with random printable ASCII of same length", strictcli.Required()),
		"substitute random printable ASCII of the same length for every match")

	// `scrub file`'s mode. It was inferred until now -- os.Stat on the target
	// path, present means replace, absent means delete -- which made the same
	// command line mean opposite things depending on which directory it was
	// typed in, and made a typo in the path a silent deletion from history. The
	// caller says which one, or the parser refuses the command.
	scrubDeleteChoice = strictcli.MemberChoice(
		strictcli.BoolFlag("delete", "remove the file from every commit in the rewritten range", strictcli.Required()),
		"delete the file from every commit in range")
	scrubReplaceWithChoice = strictcli.MemberChoice(
		strictcli.StringFlag("replace-with", "path to the file whose contents replace the target in every commit; resolved against YOUR current directory, unlike the target argument, which is repository-relative", strictcli.Required()),
		"replace the file's contents with those of a sanitized file")

	scrubFromChoice = strictcli.MemberChoice(
		strictcli.StringFlag("from", "first commit hash to include when rewriting history", strictcli.Required()),
		"rewrite the commits from a given commit forward")
	scrubEntireHistoryChoice = strictcli.MemberChoice(
		strictcli.BoolFlag("entire-history", "rewrite all commits from the root of the repository to HEAD", strictcli.Required()),
		"rewrite every commit from the root of the repository to HEAD")
)

// newApp builds the fully registered application without running it. main()
// only runs what this returns, so a test can hold the same app and inspect
// what was registered -- which is what pins every command's effect
// classification (see classification_test.go).
func newApp() *strictcli.App {
	// The description states NO command count, deliberately. It carried one for
	// a long time ("providing 20 commands" while there were 31, then 33 while
	// docs/cli-index.md said 31), and a number in prose cannot heal itself: it
	// is true only until the next command is registered, and every reader who
	// finds it stale has been told something false by the tool about itself.
	// `safegit --help` enumerates the commands, which is the answer that cannot
	// go out of date. TestAppDescriptionStatesNoCommandCount pins the absence.
	app := strictcli.NewApp("safegit", version, "concurrency-safe git wrapper for multi-agent use with atomic commits, oplog-based undo, and history rewriting",
		strictcli.WithHandshakeEnv(sessionIDEnvVar, "Claude Code session identifier set by the invoking agent session; scopes 'safegit undo' to operations this session performed and is recorded as a commit trailer"),
		// The observe authorization, GENERATED from the git argv classification
		// table's read view (see gitexec.ObservePrefixes) rather than written
		// out here. An effects-handle invocation matching one of these prefixes
		// is an observe: it executes even in a dry run and is never written to
		// the would-do log, which is right for an invocation the table says
		// changes nothing and wrong for anything else.
		//
		// Generating it is what keeps that promise. Only verbs the table
		// declares observe-only UNCONDITIONALLY are admitted, because the match
		// is on a PREFIX: `reflog` reads until `expire` follows it, and a
		// prefix ending there would let a ref deletion execute in the middle of
		// a preview. No prefix here can match a mutating argv, and
		// TestObserveAllowlistCannotAdmitAMutation binds that to the argv
		// runGitMutation actually builds.
		strictcli.WithProcObserveAllowlist(gitexec.ObservePrefixes()),
	)

	// --quiet, --verbose, --dry-run and --approve-consequential are owned by
	// the framework: they are pre-scanned out of argv anywhere they appear and
	// delivered on the Context. Registering them here is a hard error, and
	// their former short forms (-q, -n, -y) are gone with them -- the reserved
	// quartet has no short forms by ratified design.
	// --json is framework-owned too, on the same unconditional every-level
	// tier as the quartet: it selects machine mode, where stdout carries the
	// framework's envelope and nothing else. Declaring it here is a hard error,
	// and the value reaches handlers through ctx.JSON() like the other four.
	app.GlobalFlag(strictcli.StringFlag("config-file", "path to a custom safegit config file instead of the default location; when omitted the default location is used", strictcli.Optional()))

	pt := func(ctx *strictcli.Context, name string, args []string, globals map[string]interface{}) int {
		gf := globalsToFlags(ctx, globals)
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
			return runRevert(gf, args)
		}
		return exitcode.General
	}

	app.Command("commit", "stage and commit specified files in a single atomic operation", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(ctx, kwargs)
		messages := kwargsStrSlice(kwargs["m"])
		var messageFile string
		if v := kwargs["F"]; v != nil {
			messageFile = v.(string)
		}
		var branch string
		if v := kwargs["branch"]; v != nil {
			branch = v.(string)
		}
		amend := optBool(kwargs["amend"], false)
		allowEmpty := optBool(kwargs["allow_empty"], false)
		trailers := kwargsStrSlice(kwargs["trailer"])
		files := kwargsStrSlice(kwargs["files"])
		hunks := kwargsStrSlice(kwargs["hunks"])
		untrack := kwargsStrSlice(kwargs["untrack"])
		moved := kwargsStrSlice(kwargs["moved"])
		movedRetract := kwargsStrSlice(kwargs["moved_retract"])
		runCommit(gf, messages, messageFile, branch, amend, allowEmpty, trailers, files, hunks, untrack, moved, movedRetract)
		return strictcli.Exit(exitcode.OK)
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(commitPayloadSchema),
		strictcli.WithGrants(strictcli.Grant{
			Name:   "parent-bump",
			Reason: "committing in a submodule moves the parent's gitlink, so safegit commits the parent too when commit.autoBumpParent is on",
			Kind:   strictcli.ProcMutate,
		}),
		strictcli.WithFlags(
			strictcli.StringFlag("m", "commit message paragraph; repeating it joins the values with a blank line between them, so the first is the subject and the rest are the body", strictcli.Short("m"), strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional()),
			strictcli.StringFlag("F", "read the full commit message body from a file instead of --m flags", strictcli.Short("F"), strictcli.Optional()),
			strictcli.StringFlag("branch", "commit the staged files onto a different branch without switching to it", strictcli.Optional()),
			strictcli.BoolFlag("amend", "amend the current HEAD commit by replacing it with updated content; omitted means a new commit", strictcli.Optional()),
			strictcli.BoolFlag("allow-empty", "allow creating a commit even when no files have been changed; omitted means an empty commit is refused", strictcli.Optional()),
			strictcli.StringFlag("trailer", "add a key-value trailer line to the commit message (repeatable)", strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional()),
			// The only way to select hunks. A positional path is always the
			// literal name of a file, so this flag is what distinguishes a
			// selection from a filename that happens to contain a colon --
			// nothing on disk is ever consulted to tell them apart.
			strictcli.StringFlag("hunks", "commit only the selected hunks of one file, as 'path:1,3' or 'path:2-4' (the split is on the last colon, so a path containing colons stays intact); repeatable, once per path; omitted means every named file is committed whole", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional(), strictcli.ValidateFn(validateHunkSelection)),
			// The cleanup half of `git rm --cached`: the path leaves the index
			// and stays on disk. Any tracked path qualifies, not only an
			// ignored one, and a target the commit's parent does not track is a
			// hard error rather than a no-op.
			strictcli.StringFlag("untrack", "stop tracking a path, leaving the file itself on disk: the commit records its removal from the index (repeatable, one path each); the path must be tracked in the commit's parent; omitted means nothing is untracked", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
			// Moves are DECLARED, never detected: safegit reads no blob
			// equality and guesses no renames. This flag is how a caller says
			// one happened, and the record it writes into the commit message is
			// what every later reader resolves against the trees.
			strictcli.StringFlag("moved", "declare that content moved, as 'old -> new' (repeatable, one pair each). End BOTH paths with a slash to declare a whole subtree. Quote a path C-style when it holds a space, a quote, a backslash or the arrow itself. The old path must be tracked in the commit's parent and gone from disk, and the new one must exist; nothing is inferred from file contents. Omitted means the commit declares no moves", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional(), strictcli.ValidateFn(validateMovedPair)),
			// Retraction is the only correction a record has: a record already
			// written is never edited, because editing the commit that carries
			// it rewrites history. A replacement is this flag plus --moved in
			// one commit.
			strictcli.StringFlag("moved-retract", "retract a move record declared earlier in this branch's history, by its id -- the token a 'Moved:' trailer begins with (repeatable, one id each). The id must name a record that exists and is not already retracted in the history this commit is built on; one that does not is refused rather than written. To write an unchecked retraction, use --trailer 'Moved-Retract: <id>'. Omitted means the commit retracts nothing", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
		),
		strictcli.WithArgs(
			strictcli.NewArg("files", "files to commit, taken literally -- a colon in an argument is part of the filename, and hunk selection is --hunks", strictcli.ArgOptional(), strictcli.Variadic()),
		),
	)

	app.Command("mv", "move tracked paths and commit the moves with their records in one operation", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(ctx, kwargs)
		return strictcli.Exit(runMv(gf, kwargsStrSlice(kwargs["m"]), kwargsStrSlice(kwargs["pairs"])))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(mvPayloadSchema),
		strictcli.WithGrants(strictcli.Grant{
			Name:   "parent-bump",
			Reason: "moving paths in a submodule moves the parent's gitlink, so safegit commits the parent too when commit.autoBumpParent is on",
			Kind:   strictcli.ProcMutate,
		}),
		strictcli.WithFlags(
			// Required, with no fallback of any kind. A message the framework
			// chose would be a message the framework wrote into history, which
			// is exactly what the mutating-default ban forbids -- and "moved
			// some files" is not a commit message anyone wanted.
			strictcli.StringFlag("m", "commit message paragraph; repeating it joins the values with a blank line between them, so the first is the subject and the rest are the body", strictcli.Short("m"), strictcli.Repeatable(), strictcli.Unique(false), strictcli.Required()),
		),
		strictcli.WithArgs(
			strictcli.NewArg("pairs", "one move each, written 'old -> new'. End BOTH paths with a slash to move a whole directory, which is recorded as ONE subtree record however many files it holds. Quote a path C-style when it holds a space, a quote, a backslash or the arrow itself. Every pair is checked before the first file is touched -- the source must be tracked and on disk, the destination must be free, and no two pairs may speak for the same path or chain into one another -- and a failure part-way through puts back everything already moved. The commit is the rename and nothing else: each path is carried across as the blob its parent commit held, so uncommitted content changes stay uncommitted", strictcli.ArgRequired(), strictcli.Variadic()),
		),
	)

	// The three conclusion commands, in git's own word order. They are flat
	// verbs rather than a `commit --conclude` mode because concluding an
	// operation is whole-index by construction and `commit` is pathspec-only:
	// one flag switching between two opposite contracts is exactly the
	// ambiguity this tool exists to remove. They share one engine
	// (sequencer_continue.go) and one flag vocabulary, and each declares its own
	// classification, payload schema and help.
	registerContinue(app, mergeContinueOp, mergeContinuePayloadSchema,
		"the content merged in from the other side",
		"conclude a merge git stopped before committing. Every conflicted path is named with --resolve (or in a --resolve-file), and safegit writes the merge commit itself: HEAD plus EVERY MERGE_HEAD line as parents, an octopus merge included; the merge's whole staged result as its tree, so a path the merge staged cleanly is never dropped; git's own message draft with its comment block stripped, or -m; the repository's commit-msg hook run and safegit's trailers injected; and the merge's whole state-file set removed afterwards, so a later commit is not refused. An empty merge needs no flag -- a merge commit records its parents whether or not the tree changed")
	registerContinue(app, cherryPickContinueOp, cherryPickContinuePayloadSchema,
		"the result of applying the cherry-picked commit",
		"conclude a cherry-pick git stopped before committing. Every conflicted path is named with --resolve (or in a --resolve-file). For a SINGLE cherry-pick safegit writes the commit itself: one parent, the AUTHOR preserved from the commit being applied while the committer is you, git's own message draft with its comment block stripped or -m, the repository's commit-msg hook run, and the cherry-pick's state files removed afterwards. A QUEUED sequence -- git cherry-pick with more than one commit -- is DELEGATED instead: safegit stages the same declared resolutions into a copy of the index, runs the same completeness and conflict-marker checks, and then hands the rest of the queue to git's own 'cherry-pick --continue' with that copy as its index, because concluding one step natively would strand the rest of the queue. git authors those commits, so they carry no safegit trailers and 'safegit undo' does not reverse them; -m, --trailer and --dry-run are refused for a queued sequence rather than silently ignored")
	registerContinue(app, revertContinueOp, revertContinuePayloadSchema,
		"the result of UNDOING the reverted commit, which is what its parent held -- not the reverted commit's own content",
		"conclude a revert git stopped before committing. Every conflicted path is named with --resolve (or in a --resolve-file). For a SINGLE revert safegit writes the commit itself: one parent, YOU as both author and committer -- a revert is your own new change, not the reverted commit author's, which is git's own division and the opposite of what a cherry-pick does -- git's own message draft with its comment block stripped or -m, the repository's commit-msg hook run, and the revert's state files removed afterwards. Note the stage keywords: a revert applies an INVERSE patch, so theirs is what the reverted commit's parent held -- resolving to theirs keeps the revert, resolving to ours keeps the commit being reverted. A QUEUED sequence -- git revert with more than one commit -- is DELEGATED to git's own 'revert --continue' with safegit's index copy as its index, after the same completeness and conflict-marker checks; git authors those commits, so they carry no safegit trailers and 'safegit undo' does not reverse them, and -m, --trailer and --dry-run are refused for a queued sequence rather than silently ignored")

	app.Passthrough("checkout", "checkout a branch or ref, guarded twice before git runs: the worktree operation lock, held for the whole command, and then a check for uncommitted work", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Passthrough("merge", "merge a branch into HEAD, guarded twice before git runs: the worktree operation lock, held for the whole command, and then a check for uncommitted work. A merge git stops on a conflict is concluded by safegit, not by git: 'safegit merge --continue' is refused and names 'safegit merge-continue', which commits the merge's whole staged result with safegit's trailers on it", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Passthrough("rebase", "rebase the current branch onto upstream, guarded twice before git runs: the worktree operation lock -- held for the whole rebase, an interactive one's editor session included, so a second safegit process in this worktree waits that long -- and then a check for uncommitted work. A rebase's own --continue and --abort stay git's: safegit has no verb that finishes one", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Passthrough("reset", "reset HEAD with guards that prevent accidental --hard data loss. The worktree operation lock is taken for EVERY reset, because every reset moves HEAD; the uncommitted-work check applies to --hard alone, since only --hard mutates the working tree", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Passthrough("bisect", "binary search through commits to find a bug. The worktree operation lock is taken for EVERY invocation; the uncommitted-work check applies to the tree-moving subcommands (good, bad, old, new, reset, start)", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Command("push", "push refs to remote with pre-pre-push hooks and automatic retry", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(ctx, kwargs)
		prePushHook := optBool(kwargs["pre_push_hook"], true)
		forceWithLease := optBool(kwargs["force_with_lease"], false)
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		var mode pushMode
		switch kwargs["refs"].(string) {
		case "head":
			mode = pushModeHead
		case "branches":
			mode = pushModeBranches
		case "tags":
			mode = pushModeTags
		case "both":
			mode = pushModeBoth
		default:
			// --refs is required and closed over its four declared choices, so
			// the framework refuses a fifth value before dispatch. Refusing
			// loudly keeps the zero value (pushModeHead) from silently becoming
			// the answer for any future non-CLI caller: that silent
			// fall-through is exactly how `--no-only-tags` used to push HEAD.
			die(exitcode.Internal, "unreachable: --refs is required and closed over head, branches, tags, both")
		}
		return strictcli.Exit(runPush(gf, !prePushHook, forceWithLease, remote, mode))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithGrants(
			strictcli.Grant{
				Name:   "push",
				Reason: "publishing local refs to a remote is what this command is for",
				Kind:   strictcli.ProcMutate,
			},
			strictcli.Grant{
				Name:   "force-push",
				Reason: "--force-with-lease overwrites the remote ref, discarding whatever the lease expectation did not cover",
				Kind:   strictcli.ProcMutate,
			},
		),
		strictcli.WithFlags(
			strictcli.BoolFlag("pre-push-hook", "run pre-pre-push hook scripts before pushing to remote; omitted means the hooks run. Under --dry-run only EXECUTION is skipped -- a hook is an arbitrary script, so running one is a mutation a preview may not perform -- and the preview says so on stderr and in the payload's pre_pre_push_hooks_skipped member. DISCOVERY still runs in a preview: it reads the filesystem and mutates nothing, and its two refusals (exit 24 for hooks left in the pre-migration .git/hooks location, exit 25 for a checkout-provided hook without its execute bit) are verdicts about the checkout that hold whether or not anything is pushed, so a preview cannot report success for a push that could only ever exit 24 or 25. Passing --no-pre-push-hook skips discovery too", strictcli.Optional()),
			strictcli.BoolFlag("force-with-lease", "force push, pinning each ref to the SHA safegit just observed on the remote (--force-with-lease=<remoteRef>:<sha>, or the empty expectation for a ref the remote does not have yet), so a ref somebody else moved in the meantime is refused rather than overwritten; forcing is consequential, so it is confirmed at the terminal and --approve-consequential answers it in advance; omitted means an ordinary push", strictcli.Optional()),
			// One required choice replaces the four mode bools the mutex group
			// held. A bool member could be negated (`--no-only-tags` pushed
			// everything), and the negation had nowhere honest to go; a value
			// flag closed over four choices has no such spelling.
			strictcli.StringFlag("refs", "which refs to push", strictcli.Required(), strictcli.Choices(
				strictcli.Ch("head", "push only the current HEAD branch to the remote, ignoring other refs"),
				strictcli.Ch("branches", "push all local branches to the remote, ignoring tags and other refs"),
				strictcli.Ch("tags", "push all local tags to the remote without pushing any branches"),
				strictcli.Ch("both", "push all local branches and all tags to the remote in one operation"),
			)),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to push to (defaults to origin)", strictcli.ArgOptional()),
		),
		strictcli.PayloadSchema(pushPayloadSchema),
	)
	app.Command("pull", "fetch from a remote and merge, with the merge strategy stated explicitly: --merge-strategy is required and has no default, so a pull never depends on git's own configuration to decide whether it may create a merge commit", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(ctx, kwargs)
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
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithFlags(
			strictcli.StringFlag("merge-strategy", "how the fetched commits are merged into the current branch", strictcli.Required(), strictcli.Choices(
				strictcli.Ch("ff", "fast-forward when possible, otherwise create a merge commit"),
				strictcli.Ch("ff-only", "fast-forward only, refusing the pull when the branches have diverged"),
				strictcli.Ch("no-ff", "always create a merge commit, even when a fast-forward is possible"),
			)),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository to pull from (defaults to origin)", strictcli.ArgOptional()),
			strictcli.NewArg("branch", "name of the remote branch to fetch and merge into the current branch", strictcli.ArgOptional()),
		),
	)
	bg := app.Group("backup", "push, list, and restore per-branch history backups held in the tool-owned refs/backups namespace on a remote, so uncommitted-to-the-world work survives a lost machine without ever touching refs/heads")
	bg.Command("backup", "push the current branch to its backup slot refs/backups/<branch> on the remote, after fetching that slot and refusing when it holds commits your history does not contain; the push is pinned with --force-with-lease to the exact SHA that was just observed (or to \"this ref must not exist\" for a first backup), so a concurrent backup from another machine is rejected rather than clobbered; plain git equivalent: git push --force-with-lease=refs/backups/<branch>:<observed-sha> <remote> HEAD:refs/backups/<branch>", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		overwrite := optBool(kwargs["overwrite_remote_backup"], false)
		allowPublicRemote := optBool(kwargs["allow_public_remote"], false)
		return strictcli.Exit(runBackupCreate(globalsToFlags(ctx, kwargs), remote, overwrite, allowPublicRemote))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithGrants(
			strictcli.Grant{
				Name:   "push",
				Reason: "a backup slot is only useful once it is on the remote",
				Kind:   strictcli.ProcMutate,
			},
			strictcli.Grant{
				Name:   "force-push",
				Reason: "a backup slot is a single overwritten slot, pinned by a lease to the SHA observed a moment earlier",
				Kind:   strictcli.ProcMutate,
			},
		),
		strictcli.WithFlags(
			strictcli.BoolFlag("overwrite-remote-backup", "replace a backup slot whose commits are missing from your current history, leasing on the SHA observed during this run; omitted, and with --no-overwrite-remote-backup, such a slot is a hard error because overwriting it would drop work backed up from elsewhere", strictcli.Optional()),
			strictcli.BoolFlag("allow-public-remote", "consent to backing up to a remote that is public, or whose visibility safegit cannot determine; omitted, and with --no-allow-public-remote, such a target is a question, asked at the terminal and refused outright when there is none, because a backup pushes the whole branch and --approve-consequential says nothing about where", strictcli.Optional()),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgOptional()),
		),
	)
	bg.Command("list", "list every backup slot present on the remote with the branch name and the commit each slot points at, so you can see which branches are backed up from which machine before restoring one; plain git equivalent: git ls-remote <remote> 'refs/backups/*'", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		return strictcli.Exit(runBackupList(globalsToFlags(ctx, kwargs), remote))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgOptional()),
		),
	)
	bg.Command("restore", "fetch the current branch's backup slot from the remote and fast-forward the branch onto it, refusing when the local branch carries commits the backup does not contain so no local work is ever discarded; plain git equivalent: git fetch <remote> refs/backups/<branch> && git merge --ff-only FETCH_HEAD", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		return strictcli.Exit(runBackupRestore(globalsToFlags(ctx, kwargs), remote))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "name of the remote repository holding the backup slots (defaults to origin)", strictcli.ArgOptional()),
		),
	)
	cg := app.Group("config", "show, get, or set safegit configuration key-value pairs")
	cg.Command("show", "show all configuration values currently in effect for this repository, including built-in defaults and any user overrides from the .git/safegit/config.json file, printed as key-value pairs to stdout for inspection and debugging purposes", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runConfigShow(globalsToFlags(ctx, kwargs)))
	}, strictcli.WithEffect(strictcli.EffectReadOnly))
	cg.Command("get", "get the current value of a single configuration key from the .git/safegit/config.json file, printing the raw value to stdout so it can be captured by scripts or used in automation pipelines", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		key := kwargs["key"].(string)
		return strictcli.Exit(runConfigGet(globalsToFlags(ctx, kwargs), key))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key whose current value should be retrieved", strictcli.ArgRequired())),
	)
	cg.Command("set", "set a configuration key to a new value in the .git/safegit/config.json file, creating the file if it does not exist yet, and persisting the change for all future safegit invocations in this repository", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		key := kwargs["key"].(string)
		value := kwargs["value"].(string)
		return strictcli.Exit(runConfigSet(globalsToFlags(ctx, kwargs), key, value))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithArgs(strictcli.NewArg("key", "the configuration key to set to the specified value in config.json", strictcli.ArgRequired()), strictcli.NewArg("value", "the new value to assign to the specified configuration key", strictcli.ArgRequired())),
	)

	hg := app.Group("hook", "manage pre-pre-push hook scripts that run before every push")
	hg.Command("list", "list every pre-pre-push hook location safegit knows about, with its origin, its path and whether it is executable, so you can audit which checks run before every push. Three origins are shown: local, the tool-owned live store under the repository's common .git/safegit/hooks that hook install writes to and every worktree shares; tracked, the hooks the CHECKOUT provides in .safegit/hooks, which run because they are in that directory whether or not git tracks them, so cloning a repository and pushing from that checkout runs the repository's scripts; and legacy, the pre-migration location in git's own .git/hooks, which nothing runs any more and safegit hook migrate relocates. Non-executable and non-hook entries are listed too, because the hook an operator is asking about is usually the one that is NOT running", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(hookList(globalsToFlags(ctx, kwargs)))
	}, strictcli.WithEffect(strictcli.EffectReadOnly))
	hg.Command("run", "run all installed pre-pre-push hooks (or a single named hook) immediately without performing an actual push, so you can verify that all configured hooks pass before committing to a real push operation", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		var name string
		if v := kwargs["name"]; v != nil {
			name = v.(string)
		}
		return strictcli.Exit(hookRun(globalsToFlags(ctx, kwargs), name))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		// Running a hook means running an operator-supplied script whose effects
		// safegit cannot know, and the effects handle's `run` carries no stdin
		// parameter, so the invocation cannot be minted either. Any preview here
		// would be invented, so the flag is refused instead.
		strictcli.WithDryRunUnsupported("running a hook executes an operator-supplied script whose effects safegit cannot know in advance, so there is nothing honest to preview; run 'safegit hook list' to see which scripts would run"),
		strictcli.WithArgs(strictcli.NewArg("name", "name of a specific hook to run; omit to run all installed hooks", strictcli.ArgOptional())),
	)
	hg.Command("install", "install a pre-pre-push hook by copying a script file into the live store under the repository's common .git/safegit/hooks directory and making it executable, so that safegit push runs it before any network I/O occurs. The store is keyed on the common git dir, so a hook installed from a linked worktree is the same hook every worktree of the repository runs. An existing destination is refused rather than overwritten", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		path := kwargs["path"].(string)
		return strictcli.Exit(hookInstall(globalsToFlags(ctx, kwargs), path))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithArgs(strictcli.NewArg("path", "filesystem path to the hook script file to install into safegit", strictcli.ArgRequired())),
	)
	hg.Command("remove", "remove one hook from the tool-owned live store under the repository's common .git/safegit/hooks directory by name, naming either the store-relative path such as pre-pre-push.d/20-lint or just the base name, so a hook can be retired or replaced without deleting files by hand; a name that resolves only to a hook the checkout provides in .safegit/hooks is refused, because removing that one means deleting the file and committing that, and a name carried by both stores removes the live one and says the other still runs", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		name := kwargs["name"].(string)
		return strictcli.Exit(hookRemove(globalsToFlags(ctx, kwargs), name))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithArgs(strictcli.NewArg("name", "name of the installed hook to remove, as shown by 'safegit hook list'", strictcli.ArgRequired())),
	)
	hg.Command("migrate", "move safegit's hooks out of git's own .git/hooks directory into the tool-owned .git/safegit/hooks store, relocating the pre-pre-push file and the pre-pre-push.d directory unconditionally because those two names are the only ones safegit ever wrote there, and reporting success with an explanation when there is nothing to move. Both ends are under the repository's COMMON git dir, which is git's own hook directory in every worktree, so migration run from a linked worktree relocates the repository's hooks", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(hookMigrate(globalsToFlags(ctx, kwargs)))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
	)
	app.Command("doctor", "run diagnostic health checks on the repository and optionally repair issues", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runDoctor(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithFlags(
			// One required choice replaces the three mode bools the mutex group
			// held: a bool member could be declined (`--no-fix`) into a state
			// that elected nothing, and the handler had to refuse it by hand.
			strictcli.StringFlag("action", "what doctor does with the health checks it runs", strictcli.Required(), strictcli.Choices(
				strictcli.Ch("diagnose", "run all health checks and report results without fixing any issues"),
				strictcli.Ch("fix", "run all health checks and automatically repair any issues found"),
				strictcli.Ch("uninstall", "remove safegit's state from this REPOSITORY -- every worktree's state directory plus the shared locks and hook store, not only the worktree you are standing in -- after listing every path it is about to remove"),
			)),
		),
	)
	ag := app.Group("author", "audit and rewrite commit author/committer identity — list all identities, check against expected values, and rewrite name or email across history")
	ag.Command("list", "list all distinct author and committer identities across the entire commit history, showing name, email, role, and commit count for each unique identity — useful for auditing repositories with multiple contributors or detecting unwanted identity variations such as typos, old email addresses, or bot accounts that should be consolidated before a rewrite", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runAuthorList(globalsToFlags(ctx, kwargs)))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(authorListPayloadSchema),
	)
	ag.Command("check", "check that all commits use the expected author and committer identity by scanning every commit in the repository history, reporting any deviations with the exact commit hashes and mismatched fields, and suggesting the corresponding safegit author rewrite command to fix each deviation found", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runAuthorCheck(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(authorCheckPayloadSchema),
		strictcli.WithFlags(
			strictcli.StringFlag("name", "expected author and committer display name that all commits should use", strictcli.Optional()),
			strictcli.StringFlag("email", "expected author and committer email address that all commits should use", strictcli.Optional()),
		),
	)
	ag.Command("rewrite", "rewrite author and committer name or email across all commit history using git filter-branch style rewriting, replacing every occurrence of the old identity with the new one in both author and committer fields while preserving timestamps, commit messages, tree contents, and parent relationships so the rewritten history is otherwise identical to the original", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runRewriteAuthor(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.PayloadSchema(rewriteAuthorPayloadSchema),
		// Every commit from the rewrite point forward gets a new SHA. Anyone
		// who already pulled this history keeps the old commits and will have
		// to recover by hand, and the rewrite is not undoable from safegit's
		// oplog. That is worth interrupting someone for.
		strictcli.WithConsequential(),
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("old-name", "current author or committer display name to search for and replace", strictcli.Optional()),
			strictcli.StringFlag("new-name", "new display name to substitute wherever the old name is found in history", strictcli.Optional()),
			strictcli.StringFlag("old-email", "current author or committer email address to search for and replace", strictcli.Optional()),
			strictcli.StringFlag("new-email", "new email address to substitute wherever the old email is found in history", strictcli.Optional()),
		),
		// The two pairs and the at-least-one over them are one declaration each:
		// the handler's own "at least one of --old-name or --old-email is
		// required" guard is deleted with them (contract §26.7).
		strictcli.WithConstraints(
			strictcli.AllOrNone("author-name", strictcli.Member("old-name"), strictcli.Member("new-name")),
			strictcli.AllOrNone("author-email", strictcli.Member("old-email"), strictcli.Member("new-email")),
			strictcli.AtLeastOne("author-change", strictcli.Member("author-name"), strictcli.Member("author-email")),
		),
	)
	app.Deprecated("rewrite-author", "use 'safegit author rewrite' instead")
	sg := app.Group("scrub", "surgically rewrite git history to remove or replace sensitive content: file and match rewrite the commits, trees and blobs of a range the caller selects (--from or --entire-history), run applies a recipe of such operations in one coordinated pass, and verify only reads -- it confirms that the patterns named on its command line are absent from the whole object store")
	sg.Command("file", "replace or remove a specific file across every commit in a SELECTED RANGE of history -- --from <commit> or --entire-history, one of which is required -- rewriting each affected commit tree to either substitute the file's contents with those of a sanitized file or delete it entirely from every snapshot in that range. A --delete also removes the move records naming that path, whole, since the path they refer to is being erased; a --replace-with edits no message, because the path still exists and a record naming it is still true", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubFile(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.PayloadSchema(scrubFilePayloadSchema),
		// An irreversible history rewrite: every commit downstream of --from is
		// replaced, refs move, and a clone that already pulled the old history
		// cannot be reconciled automatically.
		strictcli.WithConsequential(),
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed", strictcli.Required()),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
			strictcli.MemberChoiceFlag("mode", "what happens to the file at every commit in range", strictcli.Required(),
				scrubDeleteChoice, scrubReplaceWithChoice),
			strictcli.MemberChoiceFlag("range", "how much of the history is rewritten", strictcli.Required(),
				scrubFromChoice, scrubEntireHistoryChoice),
		),
		strictcli.WithArgs(
			strictcli.NewArg("file", "repository-relative path to the file that should be scrubbed from history", strictcli.ArgRequired()),
		),
	)
	sg.Command("match", "replace every occurrence of a regex pattern in the blobs, commit messages and tag annotations of a SELECTED RANGE of history -- --from <sha> or --entire-history, one of which is required -- rewriting commit trees so that sensitive values like secrets and credentials are removed from every snapshot in that range. A move record is rewritten as a record rather than as text: the substitution applies to the decoded paths and the pair is re-encoded, so the output always parses, a pattern written against the escaped spelling matches nothing, and a substitution whose result would no longer be a move is refused before any ref moves", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubMatch(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.PayloadSchema(scrubMatchPayloadSchema),
		// Same irreversible rewrite as `scrub file`, driven by a regex whose
		// blast radius is not visible in the command line.
		strictcli.WithConsequential(),
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all blobs in history", strictcli.Required()),
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed", strictcli.Required()),
			strictcli.StringFlag("scope", "glob pattern limiting which file paths are searched (e.g. '*.env', 'config/**')", strictcli.Optional()),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
			strictcli.MemberChoiceFlag("substitution", "what replaces each match", strictcli.Required(),
				scrubReplaceChoice, scrubMangleChoice),
			strictcli.MemberChoiceFlag("range", "how much of the history is rewritten", strictcli.Required(),
				scrubFromChoice, scrubEntireHistoryChoice),
		),
	)
	sg.Command("run", "execute a multi-operation scrub recipe from a TOML file, applying all pattern replacements and file removals across history in a single coordinated pass with topological commit ordering, overlap detection between operations, and automatic verification that no matched content survives in the rewritten object store — use --diff to preview all changes as unified diffs before committing to the rewrite", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubRun(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.PayloadSchema(scrubRunPayloadSchema),
		// A recipe applies many irreversible rewrites in one coordinated pass;
		// the operations live in a TOML file, so the command line shows even
		// less about what is about to happen than `scrub file` does.
		strictcli.WithConsequential(),
		strictcli.WithTags("json"),
		strictcli.WithFlags(
			strictcli.StringFlag("reason", "mandatory audit trail message explaining why this scrub operation is needed", strictcli.Required()),
			strictcli.BoolFlag("diff", "preview what would change without modifying any objects, showing unified diffs; omitted means the rewrite is performed", strictcli.Optional()),
			strictcli.IntFlag("limit", "maximum number of blob diffs to show in --diff mode; omitted means 50", strictcli.Optional()),
			strictcli.StringFlag("remap-shas-in", "glob selecting files whose full 40-character commit hashes are remapped to the rewritten SHAs during the walk, keeping hash-referencing files like JSONL changelogs self-consistent at every commit (repeatable; same matching semantics as --scope; not applied inside submodule histories)", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
			strictcli.MemberChoiceFlag("range", "how much of the history is rewritten", strictcli.Required(),
				scrubFromChoice, scrubEntireHistoryChoice),
		),
		strictcli.WithArgs(
			strictcli.NewArg("recipe", "path to the TOML recipe file containing scrub operations", strictcli.ArgRequired()),
		),
	)
	sg.Command("verify", "confirm that the patterns named on the command line -- repeatable --pattern regexes, the operations of a scrub recipe file, or both -- are absent from every object in the git object store, scanning blobs, commit messages, and tag annotations and reporting detailed per-pattern pass or fail results with match locations for any violations found", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScrubVerify(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(scrubVerifyPayloadSchema),
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression that must be absent from every object in the repository (repeatable)", strictcli.Repeatable(), strictcli.Unique(true), strictcli.Optional()),
			strictcli.StringFlag("scope", "glob pattern limiting which blob file paths a --pattern match counts against (e.g. '*.env', 'config/**'); recipe operations carry their own scope in the recipe file", strictcli.Optional()),
		),
		strictcli.WithArgs(
			strictcli.NewArg("recipe", "path to a scrub recipe TOML file whose operations' patterns are verified; the format is the one 'scrub run' takes, and its replace/mangle/depends_on fields are ignored here because verification substitutes nothing", strictcli.ArgOptional()),
		),
		// Verification is stateless: it reads no policy file and remembers
		// nothing between runs, so an invocation that names no pattern has
		// nothing to check and must not be representable. --scope is a
		// modifier on --pattern, so it cannot be the only thing given.
		strictcli.WithConstraints(
			strictcli.AtLeastOne("verify-input", strictcli.Member("pattern"), strictcli.Member("recipe")),
			strictcli.Requires("verify-scope", "scope", "pattern"),
		),
	)
	app.Passthrough("cherry-pick", "cherry-pick one or more commits onto HEAD, guarded twice before git runs: the worktree operation lock, held for the whole command, and then a check for uncommitted work. A pick git stops on a conflict is concluded by safegit, not by git: 'safegit cherry-pick --continue' is refused and names 'safegit cherry-pick-continue', which declares each conflicted path with --resolve and then commits -- natively for a single pick, and by delegating the rest of the queue to git for a sequence of them", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Passthrough("revert", "revert one or more commits creating inverse patches, with safety guards. Reverting a SINGLE commit produces a safegit commit: git computes the inverse patch and safegit writes the commit, so it carries safegit's trailers and, when the reverted commit declared moves, the INVERSE of each of those move records. Reverting MORE THAN ONE commit is git's own sequencer and git authors those commits, so they carry no trailers and no records at all -- the asymmetry is deliberate, and it is the same one that governs whether 'safegit undo' can reverse the result", pt, strictcli.WithEffect(strictcli.EffectMutating))
	app.Command("undo", "reverse the last safegit-authored operation using the oplog -- a commit, an mv, an amend, a reword, or a conclusion (merge-continue, cherry-pick-continue, revert-continue). It moves a REF and never the working tree", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		bypassSession := optBool(kwargs["bypass_session"], false)
		count := optInt(kwargs["count"], 1)
		// Read the session handshake through the framework accessor so the
		// dependency is declared rather than an ambient os.Getenv.
		sessionID, _ := ctx.InfraValue(sessionIDEnvVar)
		runUndo(globalsToFlags(ctx, kwargs), bypassSession, count, sessionID)
		return strictcli.Exit(exitcode.OK)
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithGrants(strictcli.Grant{
			Name:   "parent-bump",
			Reason: "undoing a submodule commit moves the parent's gitlink back, so safegit commits the parent too when commit.autoBumpParent is on",
			Kind:   strictcli.ProcMutate,
		}),
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "undo across all sessions by ignoring the session ID ownership check; omitted means only this session's operations are undone", strictcli.Optional()),
			strictcli.IntFlag("count", "number of oplog operations to undo in a single invocation; omitted means one", strictcli.Optional()),
		),
	)
	app.Command("unlock", "release one of SAFEGIT'S OWN lock files -- a per-ref lock, this worktree's operation lock, or the repository-wide rewrite lock -- left behind by a safegit process that was killed while holding it. It has nothing to do with git's .git/index.lock or any other lock git takes for itself. A lock whose holder is still alive is refused; ordinarily nothing needs this command, because a stale lock is reclaimed automatically by the next contender and 'safegit doctor --action fix' sweeps them, so it is the last-resort path for a filesystem where that reclamation cannot work", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		ref := kwargs["ref"].(string)
		return strictcli.Exit(runUnlock(globalsToFlags(ctx, kwargs), ref))
	},
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithArgs(strictcli.NewArg("ref", "which lock to release: a branch name (main), a full ref (refs/tags/v1), or a tool-owned lock -- safegit/rewrite for the repository-wide history-rewrite lock, safegit/operation for this worktree's operation lock", strictcli.ArgRequired())),
	)
	app.Command("scan", "search git history for regex pattern matches across all objects and working tree files, scanning blobs, commit messages, tag annotations, and trailers with optional scope filtering and commit range selection", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		return strictcli.Exit(runScan(globalsToFlags(ctx, kwargs), kwargs))
	},
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(scanPayloadSchema),
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regular expression pattern to search for across all objects in history", strictcli.Required()),
			strictcli.StringFlag("scope", "glob pattern limiting which blob file paths are included (e.g. '*.env', 'config/**')", strictcli.Optional()),
			strictcli.StringFlag("from", "first commit hash to include when scanning history (mutually exclusive with --entire-history)", strictcli.Optional()),
			strictcli.BoolFlag("entire-history", "scan all commits from the root of the repository to HEAD (mutually exclusive with --from)", strictcli.Default(false)),
			strictcli.StringFlag("target", "comma-separated list of match types to include: blobs,commits,tags,trailers,files (default: all)", strictcli.Optional()),
		),
	)
	app.Command("version", "print safegit version, Go runtime version, and git version", func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		runVersion(globalsToFlags(ctx, kwargs))
		return strictcli.Exit(exitcode.OK)
	}, strictcli.WithEffect(strictcli.EffectReadOnly), strictcli.PayloadSchema(versionPayloadSchema))

	return app
}

// optBool, optStr and optInt resolve an optional flag's absence to the fallback
// its own help text declares.
//
// strictcli's mutating-default ban (contract §27.1) forbids Default() on any
// flag or positional arg of a command declaring effect="mutating": absence must
// never resolve to a value the invocation did not state, because on a mutating
// command a value the framework picked is a value the framework writes. safegit's
// opt-in and opt-out switches therefore declare Optional() and name their
// fallback in their help, and these three functions are the ONLY place where
// absence becomes that fallback -- so no handler further down ever receives a
// nil it would misread as the zero value.
func optBool(v interface{}, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return v.(bool)
}

func optStr(v interface{}, fallback string) string {
	if v == nil {
		return fallback
	}
	return v.(string)
}

func optInt(v interface{}, fallback int) int {
	if v == nil {
		return fallback
	}
	return v.(int)
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

// reservedFlags is the framework-owned quartet plus --json, all read off the
// Context. It is a separate struct so the --json/--approve-consequential
// independence below stays testable without a live dispatch context.
type reservedFlags struct {
	quiet    bool
	verbose  bool
	dryRun   bool
	approved bool
}

// newGlobalFlags carries the reserved flags and safegit's app-level flags into
// the handler-facing struct.
//
// Machine mode does not rewrite any of them. It used to force quiet, which made
// ctx.Quiet() and flags.quiet disagree and pushed every command into a separate
// machine-mode code path; the envelope is structurally exempt from quiet, so
// there is nothing left for the coupling to protect. Suppressing safegit's own
// stdout writes in machine mode is silent()'s job and stays there.
func newGlobalFlags(r reservedFlags, configPath string, jsonOut bool) globalFlags {
	return globalFlags{
		quiet:      r.quiet,
		verbose:    r.verbose,
		dryRun:     r.dryRun,
		approved:   r.approved,
		configPath: configPath,
		json:       jsonOut,
		root:       &executionRoot{},
	}
}

// globalsToFlags builds globalFlags from the dispatch context (the reserved
// quartet) and the app's own global flag values (the kwargs map). strictcli
// converts flag names like "config-file" to map keys "config_file".
func globalsToFlags(ctx *strictcli.Context, globals map[string]interface{}) globalFlags {
	gf := newGlobalFlags(
		reservedFlags{
			quiet:    ctx.Quiet(),
			verbose:  ctx.Verbose(),
			dryRun:   ctx.DryRun(),
			approved: ctx.ApproveConsequential(),
		},
		optStr(globals["config_file"], ""),
		ctx.JSON(),
	)
	gf.sc = ctx
	return gf
}

// versionResult is what `version` reports, in both renderings.
type versionResult struct {
	Safegit string `json:"safegit"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Git     string `json:"git"`
}

// versionPayloadSchema declares what `version` puts in the envelope's payload.
var versionPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"safegit": strictcli.SchemaType("string"),
		"go":      strictcli.SchemaType("string"),
		"os":      strictcli.SchemaType("string"),
		"arch":    strictcli.SchemaType("string"),
		"git":     strictcli.SchemaType("string"),
	},
	[]string{"safegit", "go", "os", "arch", "git"},
	false,
)

func runVersion(flags globalFlags) {
	v := versionResult{
		Safegit: version,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Git:     gitVersion(flags.ctx()),
	}
	flags.payload(v)
	outf(flags, "safegit %s\n", v.Safegit)
	outf(flags, "go      %s %s/%s\n", v.Go, v.OS, v.Arch)
	outf(flags, "git     %s\n", v.Git)
}

// gitVersion reports the git binary's own version banner verbatim, which is
// what `safegit version` prints and puts in its payload. It does NOT go through
// git.Version: that one parses the banner down to major/minor/patch for the
// feature floors, dropping the vendor suffix ("(Apple Git-154)") an operator
// reporting a bug needs to see.
func gitVersion(ctx context.Context) string {
	out, _, err := git.Run(ctx, "--version")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

// loadConfig loads the safegit config, using the override path if --config-file was set.
func loadConfig(flags globalFlags, gitDir string) (*repo.Config, error) {
	if flags.configPath != "" {
		return repo.LoadConfigFrom(flags.configPath)
	}
	if flags.dryRun && !repo.IsInitialized(gitDir) {
		// A dry run never auto-initializes (see ensureInitialized), so on a
		// first-ever invocation in this repo there is no config.json to read.
		// The values an execute run would read are exactly the defaults
		// auto-init writes, so the preview computes from those rather than
		// failing on a file the preview itself declined to create.
		cfg := repo.DefaultConfig()
		return &cfg, nil
	}
	return repo.LoadConfig(gitDir)
}

// ensureInitialized is the single seam every command goes through to get
// .git/safegit/ auto-created. It exists because auto-init is a WRITE: creating
// the directory tree, config.json and the log file. A --dry-run promises to
// change nothing, so under it the auto-init is skipped entirely and deferred to
// the next executing invocation; a preview that has to write first is not a
// preview. No command in this package may call repo.EnsureInitialized directly.
func ensureInitialized(flags globalFlags, gitDir string) error {
	if flags.dryRun {
		return nil
	}
	return repo.EnsureInitialized(flags.ctx(), gitDir)
}

// mustGitDir resolves the .git directory or exits with an error.
//
// It asks git from the OPERATOR'S own directory, on a bare context -- discovery
// is the one thing the repository-root pin cannot itself be applied to, because
// there is no root to pin to until this call has found one. (repoRootOrEmpty is
// the same case for the work-tree top.) git.GitDir answers absolutely, so every
// later use of the result is independent of the working directory.
func mustGitDir() string {
	ctx := context.Background()
	gitDir, err := git.GitDir(ctx)
	if err != nil {
		die(exitcode.NoRepository, "not a git repository (or git is not installed)")
	}
	return gitDir
}

// consent names what pre-answers ONE deliberate confirmation when there is no
// terminal to ask at. Every confirmation site owns its own token, because the
// statements are not interchangeable: the framework's --approve-consequential
// says "yes, run this consequential command", which is not the same as "yes, to
// THAT remote". A site whose question is about a condition discovered at run
// time -- something the caller could not have known when it composed the
// command line -- declares a per-condition flag instead of borrowing the
// blanket one.
type consent struct {
	// granted reports that this site's own consent token was passed.
	granted bool
	// flag is the flag a refusal names, e.g. "--allow-public-remote".
	flag string
}

// confirmDeliberate asks for confirmation of a decision that a machine-readable
// run must never answer on the operator's behalf -- uninstalling the tool,
// publishing a branch to a remote we cannot prove is private. --json never
// consents, and neither does any flag other than the one the site declares.
//
// It does NOT gate the history rewrites (scrub file/match/run, author rewrite).
// Those declare themselves `consequential`, so the framework's confirm protocol
// obtains consent for exactly that act before dispatch; a second prompt behind
// it asked the same question twice and told an automated caller nothing the
// first had not already settled.
func confirmDeliberate(flags globalFlags, c consent, format string, args ...interface{}) bool {
	if c.granted {
		return true
	}
	if flags.json {
		// A --json run has nobody to prompt, and a dangling prompt would land
		// in the JSON stream. Refuse, and name the flag that consents.
		fmt.Fprintf(os.Stderr, "refusing: "+format+"\n", args...)
		fmt.Fprintf(os.Stderr, "          --json does not answer this confirmation; pass %s to consent deliberately\n", c.flag)
		return false
	}
	// The prompt goes to STDERR, and --quiet never suppresses it. stdout is a
	// structured channel -- the command's own result, and under --json exactly
	// one document -- so a question written there interleaves with the answer to
	// a different one; and a prompt a quiet run hid would be a prompt that hangs.
	fmt.Fprintf(os.Stderr, "\n"+format+" [y/N] ", args...)
	var answer string
	fmt.Scanln(&answer)
	return answer == "y" || answer == "Y"
}

// infof writes one line of human progress text, suppressed in the two modes
// where safegit's stdout is not the operator's to read (see silent()). It is
// the ONE spelling of that job: a handler printing several such lines calls it
// per line instead of repeating the silent() check around a fmt.Printf block.
//
// Its counterpart is outf, which prints a command's own RESULT -- the thing the
// command exists to say -- and is suppressed only in machine mode.
func infof(flags globalFlags, format string, args ...interface{}) {
	if !flags.silent() {
		fmt.Printf(format, args...)
	}
}

// outf prints a command's own result text -- the thing the command exists to
// say, which --quiet deliberately does NOT suppress (a `config get` is its
// output). Machine mode still suppresses it: stdout there carries the
// envelope and nothing else.
func outf(flags globalFlags, format string, args ...interface{}) {
	if !flags.json {
		fmt.Printf(format, args...)
	}
}

// requireCleanTree dies if the working tree has uncommitted changes.
func requireCleanTree(ctx context.Context) {
	statusOut, _, err := git.Run(ctx, "status", "--porcelain")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("checking working tree: %v", err))
	}
	if strings.TrimSpace(statusOut) != "" {
		die(exitcode.General, "working tree is dirty; commit changes before proceeding")
	}
}

// die prints an error and exits with code.
//
// It writes no JSON of its own any more. Machine mode's stdout carries the
// framework's envelope and nothing else, and safegit cannot mint one: die exits
// the process directly, below the seam that emits it. So an error path answers
// with the exit code and the stderr line in both modes -- never with a second
// document that would have to imitate the envelope. It took a globalFlags and a
// command name while the JSON branch existed; both went unread once that branch
// died, so neither is a parameter any more.
// Locks are released first. os.Exit runs no deferred function, so a command
// that had taken the worktree operation lock or a ref lock and then died on an
// unrelated error would leave its lock file on disk. The holder is dead, so the
// staleness rules would let the next contender reclaim it -- but only after
// waiting, and doctor would report it in the meantime. Releasing here costs
// nothing and keeps the failure local to the command that failed.
func die(code int, msg string) {
	lock.ReleasePending()
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

// The hunk grammar lives in ONE flag, and a positional path is always the
// literal name of a file.
//
// It used to be the other way round: a positional argument was split into a
// path and a hunk selection when its tail looked numeric AND os.Stat could not
// see the whole string as a file. That made the GRAMMAR of a command line a
// function of disk state -- `notes:1` was a hunk selection from one directory
// and a literal filename from another, one argv with two meanings -- and it
// made a file whose name really does end in a colon plus digits impossible to
// delete, because once it is gone from disk the probe reparses its own name.
//
// `--hunks 'path:1,3'` has neither problem: the flag says a hunk selection is
// coming, the split is on the last colon, and nothing on disk is ever consulted
// to decide how to read a command line.

// hunkSelection is one --hunks element: the path it names and the hunk indices
// it selects within that path.
type hunkSelection struct {
	Path  string
	Hunks []int
}

// parseHunkSelection reads one --hunks element. The split is on the LAST colon,
// so a path that itself contains colons stays intact: "sprint:1:2,3" is hunks
// 2 and 3 of the file named "sprint:1".
//
// It is both the flag's ValidateFn (which is what makes a malformed element a
// parse-time refusal, before any command handler runs) and the parser the
// handler uses, so the accepted language and the parsed language are the same
// language by construction.
func parseHunkSelection(value string) (hunkSelection, error) {
	colon := strings.LastIndex(value, ":")
	if colon < 0 {
		return hunkSelection{}, fmt.Errorf("%q needs a path and a hunk selection separated by a colon, as in 'path:1,3'", value)
	}
	path := value[:colon]
	if path == "" {
		return hunkSelection{}, fmt.Errorf("%q names no path before the colon", value)
	}
	hunks, err := stage.ParseHunkSpec(value[colon+1:])
	if err != nil {
		return hunkSelection{}, fmt.Errorf("invalid hunk selection in %q: %w", value, err)
	}
	return hunkSelection{Path: path, Hunks: hunks}, nil
}

// validateHunkSelection is the flag's per-element ValidateFn.
func validateHunkSelection(v interface{}) error {
	_, err := parseHunkSelection(v.(string))
	return err
}

// validateMovedPair is --moved's per-element ValidateFn: the same parser the
// pipeline uses, so a malformed pair is refused at parse time rather than after
// a repository has been read. Both call internal/trailer's ParsePair, which is
// also what `safegit mv` parses its arguments with -- one grammar, one
// implementation, three consumers.
func validateMovedPair(v interface{}) error {
	_, _, err := trailer.ParsePair(v.(string))
	return err
}

// buildFileSpecs combines the literal positional paths with the --hunks
// selections into the pipeline's file specs. Positionals first, then the
// selections, each in the order given.
//
// It parses and nothing more. Whether two arguments name the SAME file, and so
// contradict each other, is not a question about the strings a caller typed:
// `./a.go`, `a.go` and `sub/../a.go` are one path, and only canonicalization
// against the repository root can see that. Intake canonicalizes every
// argument already, so the conflict is decided there
// (internal/commit/intake.go, conflictingSpellings) and only there -- a
// spelling comparison here would be a second authority that answers the same
// question wrongly for every spelling but one.
func buildFileSpecs(files []string, hunks []string) ([]commit.FileSpec, error) {
	specs := make([]commit.FileSpec, 0, len(files)+len(hunks))
	for _, f := range files {
		specs = append(specs, commit.FileSpec{Path: f})
	}
	for _, h := range hunks {
		sel, err := parseHunkSelection(h)
		if err != nil {
			return nil, err
		}
		specs = append(specs, commit.FileSpec{Path: sel.Path, Hunks: sel.Hunks})
	}
	return specs, nil
}
