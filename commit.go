package main

import (
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

// parentList renders the pipeline's single parent as the payload's list form.
// An unborn ref has no parent, which is an empty list and not a null entry.
func parentList(parent string) []string {
	if parent == "" {
		return []string{}
	}
	return []string{parent}
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

func runCommit(flags globalFlags, messages []string, messageFile string, branch string, amend bool, allowEmpty bool, trailers []string, files []string, hunks []string) {
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

		runCommitAmend(flags, gitDir, messages, branch, trailers, files, hunks)
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
	if len(files) == 0 && len(hunks) == 0 && !allowEmpty {
		die(exitcode.Usage, "no files specified (use -- file1 file2 ... or --hunks path:1,3)")
	}

	msg := strings.Join(messages, "\n")

	fileSpecs, err := buildFileSpecs(files, hunks)
	if err != nil {
		die(exitcode.Usage, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
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

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}
	result, err := p.Execute(flags.ctx(), commit.CommitRequest{
		Message:    msg,
		FileSpecs:  fileSpecs,
		Branch:     branch,
		Trailers:   trailers,
		AllowEmpty: allowEmpty,
		DryRun:     flags.dryRun,
	})
	if err != nil {
		die(pipelineExitCode(err), err.Error())
	}

	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
		fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
		fmt.Fprintf(os.Stderr, "  parent: %s\n", result.Parent)
		fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
	}

	if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "commit", firstLine(msg)); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	recordCommitRefUpdate(flags, result.Ref, result.SHA, result.Parent)

	flags.payload(commitPayload{
		Ref:            result.Ref,
		Parents:        parentList(result.Parent),
		Tree:           result.Tree,
		SHA:            realSHA(flags, result.SHA),
		OldSHA:         nil,
		Files:          result.Files,
		SkippedIgnored: orEmpty(result.SkippedIgnored),
		Attempts:       result.Attempts,
		DryRun:         flags.dryRun,
	})

	if !flags.silent() {
		fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
		if flags.dryRun {
			fmt.Printf(" %d file(s) would be committed", len(result.Files))
		} else {
			fmt.Printf(" %d file(s) committed", len(result.Files))
		}
		if result.Attempts > 1 {
			fmt.Printf(" (%d CAS retries)", result.Attempts-1)
		}
		fmt.Println()
	}
}

// runCommitAmend handles the --amend path: amend with files, or reword without.
// recordCommitRefUpdate puts the commit pipeline's ref move into the
// framework's would-do log.
//
// The pipeline (internal/commit) owns its own dry-run seam: it builds the tree
// and the commit object, then returns WITHOUT the compare-and-swap ref update
// that would make the commit real. That seam predates the effects regime and
// cannot move onto the handle without breaking the CAS retry loop the update
// sits inside, so the mint here is deliberately dry-mode-only -- in a real run
// the pipeline performs the update itself. Without it a `commit --dry-run`
// would print an empty would-do log, which reads as "this would change
// nothing".
func recordCommitRefUpdate(flags globalFlags, ref, newSHA, oldSHA string) {
	if !flags.dryRun {
		return
	}
	if oldSHA == "" {
		oldSHA = git.ZeroSHA
	}
	argv, err := gitexec.ArgvAny(gitexec.ExemptCommitRefUpdateRecord, "update-ref", ref, newSHA, oldSHA)
	if err != nil {
		return
	}
	_, _ = flags.effects().Run(argv, strictcli.Resource("ref:"+ref))
}

func runCommitAmend(flags globalFlags, gitDir string, messages []string, branch string, trailers []string, files []string, hunks []string) {
	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}

	// Same ordering as the plain commit path: operation lock outermost, the
	// pipeline's per-ref CAS lock inside it.
	release, code := acquireOperationLock(flags, gitDir, "amend")
	if code != 0 {
		os.Exit(code)
	}
	defer release()

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}

	if len(files) > 0 || len(hunks) > 0 {
		// Amend: add new files to the tip commit
		var msg string
		if len(messages) > 0 {
			msg = strings.Join(messages, "\n")
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
			Message:   msg,
			FileSpecs: fileSpecs,
			Branch:    branch,
			Trailers:  trailers,
			DryRun:    flags.dryRun,
		})
		if err != nil {
			die(pipelineExitCode(err), err.Error())
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
			fmt.Fprintf(os.Stderr, "  parent: %s\n", result.Parent)
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
		}

		if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "amend", firstLine(msg)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		recordCommitRefUpdate(flags, result.Ref, result.SHA, result.OldSHA)

		flags.payload(commitPayload{
			Ref:            result.Ref,
			Parents:        parentList(result.Parent),
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
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msgDisplay))
			if flags.dryRun {
				fmt.Printf(" %d file(s) would be amended (was %s)", len(result.Files), result.OldSHA[:8])
			} else {
				fmt.Printf(" %d file(s) amended (was %s)", len(result.Files), result.OldSHA[:8])
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

		msg := strings.Join(messages, "\n")

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  reword message: %s\n", firstLine(msg))
			if branch != "" {
				fmt.Fprintf(os.Stderr, "  branch: %s\n", branch)
			}
		}

		result, err := p.Reword(flags.ctx(), commit.RewordRequest{
			Message:  msg,
			Branch:   branch,
			Trailers: trailers,
			DryRun:   flags.dryRun,
		})
		if err != nil {
			die(pipelineExitCode(err), err.Error())
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
		}

		if err := maybeAutoBumpParent(flags.ctx(), flags, gitDir, result.SHA, "reword", firstLine(msg)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		recordCommitRefUpdate(flags, result.Ref, result.SHA, result.OldSHA)

		// A reword replaces a message and nothing else, so its changed-path
		// list is empty by construction rather than by measurement.
		flags.payload(commitPayload{
			Ref:            result.Ref,
			Parents:        parentList(result.Parent),
			Tree:           result.Tree,
			SHA:            realSHA(flags, result.SHA),
			OldSHA:         &result.OldSHA,
			Files:          []string{},
			SkippedIgnored: []string{},
			Attempts:       result.Attempts,
			DryRun:         flags.dryRun,
		})

		if !flags.silent() {
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
			if flags.dryRun {
				fmt.Printf(" would reword (was %s)\n", result.OldSHA[:8])
			} else {
				fmt.Printf(" reworded (was %s)\n", result.OldSHA[:8])
			}
		}
	}
}
