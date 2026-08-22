package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// pipelineExitCode is the one place a commit-pipeline error becomes an exit
// code. commit, amend and reword all end the same way -- die(code, err) -- and
// all three read the code from here, so the three paths cannot disagree.
//
// Two typed sources, in order:
//
//   - a *commit.CommitError, which carries the code the pipeline chose
//     deliberately (WriteTree, CommitTree, CoordinationBusy, CASExhausted).
//     errors.As rather than a type assertion: the pipeline annotates some
//     failures with the path they happened on, so the CommitError arrives
//     wrapped.
//   - a *lock.TimeoutError, which the pipeline does not wrap in a CommitError
//     at all -- it returns the plain "acquiring lock on <ref>: %w" error from
//     its ref-lock acquisition. Recognizing it here is what makes a contended
//     ref lock exit LockTimeout from commit, amend and reword the way it
//     already did from undo and the four rewrite commands. It is the same
//     situation with the same remedy (wait, or release the lock), and it was
//     reported as the undifferentiated General only because of where in the
//     pipeline it happened.
//
// Anything else is General.
func pipelineExitCode(err error) int {
	var ce *commit.CommitError
	if errors.As(err, &ce) {
		return ce.Code
	}
	if lock.IsTimeout(err) {
		return exitcode.LockTimeout
	}
	return exitcode.General
}

// commitPayload is what `commit` puts in the envelope's payload, in all three
// of its forms: a new commit, an amend, and a reword.
//
// Nothing here is counted from the arguments. `files` is the changed-path list
// the pipeline read off the objects -- for a commit, against its parent; for an
// amend, against the tip it replaced; empty for a reword, which changes no path
// at all. A caller that wants to know what a commit contains reads this rather
// than assuming its own argument list survived intake unchanged, which it does
// not: a directory expands, and a gitignored path under one is skipped.
type commitPayload struct {
	Ref string `json:"ref"`
	// Parents is the commit's parent list: empty for a root commit. It is a
	// list because a merge commit has more than one.
	Parents []string `json:"parents"`
	Tree    string   `json:"tree"`
	// SHA is the commit that was created, and null under --dry-run: the
	// preview builds an object to compute the tree honestly, but no commit
	// exists at that name for anyone to fetch, so reporting it as this run's
	// commit would be a lie a machine consumer cannot detect.
	SHA *string `json:"sha"`
	// OldSHA is the commit an amend or reword replaced, and null for a plain
	// commit, which replaces nothing.
	OldSHA         *string  `json:"old_sha"`
	Files          []string `json:"files"`
	SkippedIgnored []string `json:"skipped_ignored"`
	Attempts       int      `json:"attempts"`
	DryRun         bool     `json:"dry_run"`
}

// commitPayloadSchema declares what `commit` puts in the envelope's payload.
// The framework validates the value against it at emission, so the declaration
// and the struct above cannot drift.
var commitPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"ref":             strictcli.SchemaType("string"),
		"parents":         strictcli.SchemaArray(strictcli.SchemaType("string")),
		"tree":            strictcli.SchemaType("string"),
		"sha":             strictcli.SchemaType("string", "null"),
		"old_sha":         strictcli.SchemaType("string", "null"),
		"files":           strictcli.SchemaArray(strictcli.SchemaType("string")),
		"skipped_ignored": strictcli.SchemaArray(strictcli.SchemaType("string")),
		"attempts":        strictcli.SchemaType("integer"),
		"dry_run":         strictcli.SchemaType("boolean"),
	},
	[]string{"ref", "parents", "tree", "sha", "old_sha", "files", "skipped_ignored", "attempts", "dry_run"},
	false,
)

// joinMessages composes the commit message from repeated -m values, separating
// them with a BLANK line -- `-m subject -m body` is a subject and a body, which
// is what `git commit -m ... -m ...` means and what every reader of a git log
// assumes. Joined with a single newline instead, git reads the whole thing as
// one subject and `git log --oneline` prints every paragraph on one line.
//
// commit, amend and reword all compose their message here, so the three cannot
// disagree about what repeating -m means.
func joinMessages(messages []string) string {
	return strings.Join(messages, "\n\n")
}

// realSHA reports the commit SHA a run actually created, and nothing under a
// dry run.
func realSHA(flags globalFlags, sha string) *string {
	if flags.dryRun {
		return nil
	}
	return &sha
}

// orEmpty renders an absent list as an empty one, so a payload member is never
// null where the schema declares an array.
func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func runCommit(flags globalFlags, messages []string, messageFile string, branch string, amend bool, allowEmpty bool, trailers []string, files []string, hunks []string, untrack []string, moved []string, movedRetract []string) {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitcode.NotInitialized)
	}

	// Validate: -m and -F are mutually exclusive
	if len(messages) > 0 && messageFile != "" {
		die(exitcode.Usage, "-m and -F are mutually exclusive")
	}

	if amend {
		// --amend mode: amend (with files) or reword (without files)
		if allowEmpty {
			die(exitcode.Usage, "--allow-empty cannot be used with --amend")
		}
		if messageFile != "" {
			die(exitcode.Usage, "-F cannot be used with --amend")
		}

		runCommitAmend(flags, gitDir, messages, branch, trailers, files, hunks, untrack, moved, movedRetract)
		return
	}

	// Normal commit path
	if messageFile != "" {
		data, err := os.ReadFile(messageFile)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("reading message file: %v", err))
		}
		messages = append(messages, strings.TrimRight(string(data), "\n"))
	}

	if len(messages) == 0 {
		die(exitcode.Usage, "commit message required (-m or -F)")
	}
	if len(files) == 0 && len(hunks) == 0 && len(untrack) == 0 && !allowEmpty {
		die(exitcode.Usage, "no files specified (use -- file1 file2 ..., --hunks path:1,3 or --untrack path)")
	}

	msg := joinMessages(messages)

	fileSpecs, err := buildFileSpecs(files, hunks)
	if err != nil {
		die(exitcode.Usage, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}

	// Before the pipeline runs at all: a submodule commit moves the parent's
	// gitlink, and a parent that has not answered the auto-bump question is a
	// refusal, not a commit followed by one.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	// Outermost, around the pipeline's whole run: the in-flight-operation check
	// inside it reads state a concurrent passthrough would otherwise be free to
	// create between the check and the ref update. The pipeline's per-ref CAS
	// lock is taken inside this one.
	release, code := acquireOperationLock(flags, gitDir, "commit")
	if code != 0 {
		os.Exit(code)
	}
	defer release()

	if flags.verbose {
		paths := make([]string, len(fileSpecs))
		for i, fs := range fileSpecs {
			paths[i] = fs.Path
		}
		fmt.Fprintf(os.Stderr, "  files: %s\n", strings.Join(paths, ", "))
		if branch != "" {
			fmt.Fprintf(os.Stderr, "  branch: %s\n", branch)
		}
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(flags.ctx(), commit.CommitRequest{
		Message:      msg,
		FileSpecs:    fileSpecs,
		Branch:       branch,
		Trailers:     trailers,
		AllowEmpty:   allowEmpty,
		DryRun:       flags.dryRun,
		Untrack:      untrack,
		Moved:        moved,
		MovedRetract: movedRetract,
	})
	if err != nil {
		die(pipelineExitCode(err), err.Error())
	}

	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
		fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
		fmt.Fprintf(os.Stderr, "  parents: %s\n", strings.Join(result.Parents, " "))
		fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
	}

	if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "commit", firstLine(msg)); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	flags.payload(commitPayload{
		Ref:            result.Ref,
		Parents:        orEmpty(result.Parents),
		Tree:           result.Tree,
		SHA:            realSHA(flags, result.SHA),
		OldSHA:         nil,
		Files:          result.Files,
		SkippedIgnored: orEmpty(result.SkippedIgnored),
		Attempts:       result.Attempts,
		DryRun:         flags.dryRun,
	})

	if !flags.silent() {
		if flags.dryRun {
			fmt.Println(wouldWriteHeader("commit", result.Ref, result.Tree, firstLine(msg)))
			fmt.Printf(" %d file(s) would be committed", len(result.Files))
		} else {
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
			fmt.Printf(" %d file(s) committed", len(result.Files))
		}
		if result.Attempts > 1 {
			fmt.Printf(" (%d CAS retries)", result.Attempts-1)
		}
		fmt.Println()
	}
}

// wouldWriteHeader is a preview's answer to the `[branch sha]` line a real
// commit prints, and it deliberately does not look like one.
//
// A preview cannot know the commit SHA: the commit object a real run builds
// carries the committer timestamp, so the object the preview could name is
// never the object that will exist. What it CAN state is read off objects the
// preview really computed -- the branch the commit would go on and the tree it
// would carry -- so those are what it prints. The line does not start with `[`,
// which is what the submodule auto-bump reads a child's commit SHA out of: a
// preview line can therefore never be parsed as one.
func wouldWriteHeader(verb, ref, tree, subject string) string {
	return fmt.Sprintf("would %s on %s (tree %s): %s", verb, refShortName(ref), shortSHA(tree), subject)
}

// runCommitAmend handles the --amend path: amend with files, or reword without.
// previewCommitPlaceholder stands where the new commit's SHA goes in a recorded
// ref update.
//
// A preview cannot know that SHA. The commit object a real run builds carries
// the committer timestamp, so the object a preview could name is never the
// object that will exist -- and a would-do log stating `update-ref
// refs/heads/main <some sha> <old>` invites a reader to go looking for a commit
// that neither exists now nor will exist under that name later. The rest of the
// argv is exact: the ref that moves and the value it moves away from are both
// known, and the whole line is what the execute path really runs.
//
// It mirrors rewrittenPlaceholder in scrub_preview.go, which stands for the
// same thing on the history-rewrite side.
const previewCommitPlaceholder = "<new-commit>"

// effectsRefUpdate is the commit pipeline's ref update, minted through the
// framework's effects handle.
//
// It is the pipeline's ONLY way to move a ref, and it is one mint site rather
// than two: a handler-side record alongside the pipeline's own update would
// fire twice per commit, and the second would describe a move that had already
// happened. The pipeline calls this from inside its compare-and-swap retry
// loop, holding the ref lock, with the argv that loop needs -- so an executing
// run performs exactly this invocation, retries included, and a preview records
// it and performs nothing.
//
// Check(false) is what lets a failure keep its meaning: the framework's checked
// form turns a nonzero child into a formatted string with git's own stderr
// dropped, and the pipeline reads that stderr to tell a transient ref-lock
// contention ("cannot lock ref") from a real refusal.
type effectsRefUpdate struct{ flags globalFlags }

func (u effectsRefUpdate) Update(_ context.Context, ref, newSHA, expected string) error {
	if expected == "" {
		expected = git.ZeroSHA
	}
	// A preview cannot name the commit it would create, so the record carries
	// the placeholder; the ref and the expected value are real.
	recorded := newSHA
	if u.flags.dryRun {
		recorded = previewCommitPlaceholder
	}

	argv, err := gitexec.ArgvAny(gitexec.ExemptCommitRefUpdate, "update-ref", ref, recorded, expected)
	if err != nil {
		return err
	}
	done, err := u.flags.effects().Run(argv, strictcli.Resource("ref:"+ref), strictcli.Check(false))
	if err != nil {
		// A framework-level refusal: no child ran, and the message is the
		// framework's own.
		return err
	}
	if u.flags.dryRun {
		// Recorded instead of performed. No child process ran, so the carrier
		// is unsettled and asking it anything would panic -- and there is
		// nothing to ask: the pipeline reads nil as "the ref did not move",
		// which is exactly what happened.
		return nil
	}
	if code := done.ExitCode(); code != 0 {
		return fmt.Errorf("update-ref %s %s %s: exit %d: %s",
			ref, newSHA, expected, code, strings.TrimSpace(done.Stderr()))
	}
	return nil
}

func runCommitAmend(flags globalFlags, gitDir string, messages []string, branch string, trailers []string, files []string, hunks []string, untrack []string, moved []string, movedRetract []string) {
	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}

	// Same refusal the plain commit path makes, for both the amend and the
	// reword below: an unanswered auto-bump question in the parent stops the
	// operation before it rewrites anything.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	// Same ordering as the plain commit path: operation lock outermost, the
	// pipeline's per-ref CAS lock inside it.
	release, code := acquireOperationLock(flags, gitDir, "amend")
	if code != 0 {
		os.Exit(code)
	}
	defer release()

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}

	// Three ways to amend, in the order they are decided:
	//
	//   - files, hunks or untrack targets: an amend of the tip's content, with
	//     the message replaced by -m or kept as it is;
	//   - no files but a message: a reword;
	//   - no files and no message, but declared moves: an amend that changes
	//     nothing but the records the message carries, which is how a move
	//     committed without its record gets one.
	if len(files) > 0 || len(hunks) > 0 || len(untrack) > 0 || ((len(moved) > 0 || len(movedRetract) > 0) && len(messages) == 0) {
		// Amend: add new files to the tip commit
		var msg string
		if len(messages) > 0 {
			msg = joinMessages(messages)
		}

		fileSpecs, err := buildFileSpecs(files, hunks)
		if err != nil {
			die(exitcode.Usage, err.Error())
		}

		if flags.verbose {
			paths := make([]string, len(fileSpecs))
			for i, fs := range fileSpecs {
				paths[i] = fs.Path
			}
			fmt.Fprintf(os.Stderr, "  amend files: %s\n", strings.Join(paths, ", "))
			if branch != "" {
				fmt.Fprintf(os.Stderr, "  branch: %s\n", branch)
			}
		}

		result, err := p.Amend(flags.ctx(), commit.AmendRequest{
			Message:      msg,
			FileSpecs:    fileSpecs,
			Branch:       branch,
			Trailers:     trailers,
			DryRun:       flags.dryRun,
			Untrack:      untrack,
			Moved:        moved,
			MovedRetract: movedRetract,
		})
		if err != nil {
			die(pipelineExitCode(err), err.Error())
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
			fmt.Fprintf(os.Stderr, "  parents: %s\n", strings.Join(result.Parents, " "))
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
		}

		if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "amend", firstLine(msg)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		flags.payload(commitPayload{
			Ref:            result.Ref,
			Parents:        orEmpty(result.Parents),
			Tree:           result.Tree,
			SHA:            realSHA(flags, result.SHA),
			OldSHA:         &result.OldSHA,
			Files:          result.Files,
			SkippedIgnored: orEmpty(result.SkippedIgnored),
			Attempts:       result.Attempts,
			DryRun:         flags.dryRun,
		})

		if !flags.silent() {
			msgDisplay := msg
			if msgDisplay == "" {
				msgDisplay = "(message preserved)"
			}
			if flags.dryRun {
				fmt.Println(wouldWriteHeader("amend", result.Ref, result.Tree, firstLine(msgDisplay)))
				fmt.Printf(" %d file(s) would be amended (was %s)", len(result.Files), shortSHA(result.OldSHA))
			} else {
				fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msgDisplay))
				fmt.Printf(" %d file(s) amended (was %s)", len(result.Files), shortSHA(result.OldSHA))
			}
			if result.Attempts > 1 {
				fmt.Printf(" (%d CAS retries)", result.Attempts-1)
			}
			fmt.Println()
		}
	} else {
		// Reword: change the tip commit message without touching files
		if len(messages) == 0 {
			die(exitcode.Usage, "commit message required (-m) when using --amend without files")
		}

		msg := joinMessages(messages)

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  reword message: %s\n", firstLine(msg))
			if branch != "" {
				fmt.Fprintf(os.Stderr, "  branch: %s\n", branch)
			}
		}

		result, err := p.Reword(flags.ctx(), commit.RewordRequest{
			Message:      msg,
			Branch:       branch,
			Trailers:     trailers,
			DryRun:       flags.dryRun,
			Moved:        moved,
			MovedRetract: movedRetract,
		})
		if err != nil {
			die(pipelineExitCode(err), err.Error())
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			// A preview of a reword builds no commit object, so there is no
			// SHA to name; the line is omitted rather than printed empty.
			if result.SHA != "" {
				fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
			}
		}

		if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "reword", firstLine(msg)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		// A reword replaces a message and nothing else, so its changed-path
		// list is empty by construction rather than by measurement.
		flags.payload(commitPayload{
			Ref:            result.Ref,
			Parents:        orEmpty(result.Parents),
			Tree:           result.Tree,
			SHA:            realSHA(flags, result.SHA),
			OldSHA:         &result.OldSHA,
			Files:          []string{},
			SkippedIgnored: []string{},
			Attempts:       result.Attempts,
			DryRun:         flags.dryRun,
		})

		if !flags.silent() {
			if flags.dryRun {
				fmt.Println(wouldWriteHeader("reword", result.Ref, result.Tree, firstLine(msg)))
				fmt.Printf(" would reword (was %s)\n", shortSHA(result.OldSHA))
			} else {
				fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
				fmt.Printf(" reworded (was %s)\n", shortSHA(result.OldSHA))
			}
		}
	}
}
