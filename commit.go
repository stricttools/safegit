package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

func runCommit(flags globalFlags, messages []string, messageFile string, branch string, amend bool, allowEmpty bool, trailers []string, files []string) {
	gitDir := mustGitDir(flags, "commit")
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(4)
	}

	// Validate: -m and -F are mutually exclusive
	if len(messages) > 0 && messageFile != "" {
		die(flags, "commit", 2, "-m and -F are mutually exclusive")
	}

	if amend {
		// --amend mode: amend (with files) or reword (without files)
		if allowEmpty {
			die(flags, "commit", 2, "--allow-empty cannot be used with --amend")
		}
		if messageFile != "" {
			die(flags, "commit", 2, "-F cannot be used with --amend")
		}

		runCommitAmend(flags, gitDir, messages, branch, trailers, files)
		return
	}

	// Normal commit path
	if messageFile != "" {
		data, err := os.ReadFile(messageFile)
		if err != nil {
			die(flags, "commit", 1, fmt.Sprintf("reading message file: %v", err))
		}
		messages = append(messages, strings.TrimRight(string(data), "\n"))
	}

	if len(messages) == 0 {
		die(flags, "commit", 2, "commit message required (-m or -F)")
	}
	if len(files) == 0 && !allowEmpty {
		die(flags, "commit", 2, "no files specified (use -- file1 file2 ...)")
	}

	msg := strings.Join(messages, "\n")

	fileSpecs := parseFileSpecs(files, flags, "commit")

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, "commit", 1, fmt.Sprintf("loading config: %v", err))
	}

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
	result, err := p.Execute(context.Background(), commit.CommitRequest{
		Message:    msg,
		FileSpecs:  fileSpecs,
		Branch:     branch,
		Trailers:   trailers,
		AllowEmpty: allowEmpty,
		DryRun:     flags.dryRun,
	})
	if err != nil {
		code := 1
		if ce, ok := err.(*commit.CommitError); ok {
			code = ce.Code
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(code)
	}

	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
		fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
		fmt.Fprintf(os.Stderr, "  parent: %s\n", result.Parent)
		fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
	}

	if err := maybeAutoBumpParent(context.Background(), flags, gitDir, result.SHA, "commit", firstLine(msg)); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		os.Exit(1)
	}

	recordCommitRefUpdate(flags, result.Ref, result.SHA, result.Parent)

	if !flags.quiet {
		fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
		if flags.dryRun {
			fmt.Printf(" %d file(s) would be committed", len(files)+len(result.AutoStagedDeletions))
		} else {
			fmt.Printf(" %d file(s) committed", len(files)+len(result.AutoStagedDeletions))
		}
		if result.Attempts > 1 {
			fmt.Printf(" (%d CAS retries)", result.Attempts-1)
		}
		fmt.Println()
		for _, del := range result.AutoStagedDeletions {
			fmt.Fprintf(os.Stderr, "  auto-staged deletion: %s (rename detected)\n", del)
		}
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
		oldSHA = "0000000000000000000000000000000000000000"
	}
	_, _ = flags.effects().Run(
		[]interface{}{"git", "update-ref", ref, newSHA, oldSHA},
		strictcli.Resource("ref:"+ref),
	)
}

func runCommitAmend(flags globalFlags, gitDir string, messages []string, branch string, trailers []string, files []string) {
	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, "commit", 1, fmt.Sprintf("loading config: %v", err))
	}
	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}

	if len(files) > 0 {
		// Amend: add new files to the tip commit
		var msg string
		if len(messages) > 0 {
			msg = strings.Join(messages, "\n")
		}

		fileSpecs := parseFileSpecs(files, flags, "commit")

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

		result, err := p.Amend(context.Background(), commit.AmendRequest{
			Message:   msg,
			FileSpecs: fileSpecs,
			Branch:    branch,
			Trailers:  trailers,
			DryRun:    flags.dryRun,
		})
		if err != nil {
			code := 1
			if ce, ok := err.(*commit.CommitError); ok {
				code = ce.Code
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(code)
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  tree: %s\n", result.Tree)
			fmt.Fprintf(os.Stderr, "  parent: %s\n", result.Parent)
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
		}

		if err := maybeAutoBumpParent(context.Background(), flags, gitDir, result.SHA, "amend", firstLine(msg)); err != nil {
			fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
			os.Exit(1)
		}

		recordCommitRefUpdate(flags, result.Ref, result.SHA, result.OldSHA)

		if !flags.quiet {
			msgDisplay := msg
			if msgDisplay == "" {
				msgDisplay = "(message preserved)"
			}
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msgDisplay))
			if flags.dryRun {
				fmt.Printf(" would amend (was %s)", result.OldSHA[:8])
			} else {
				fmt.Printf(" amended (was %s)", result.OldSHA[:8])
			}
			if result.Attempts > 1 {
				fmt.Printf(" (%d CAS retries)", result.Attempts-1)
			}
			fmt.Println()
			for _, del := range result.AutoStagedDeletions {
				fmt.Fprintf(os.Stderr, "  auto-staged deletion: %s (rename detected)\n", del)
			}
		}
	} else {
		// Reword: change the tip commit message without touching files
		if len(messages) == 0 {
			die(flags, "commit", 2, "commit message required (-m) when using --amend without files")
		}

		msg := strings.Join(messages, "\n")

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  reword message: %s\n", firstLine(msg))
			if branch != "" {
				fmt.Fprintf(os.Stderr, "  branch: %s\n", branch)
			}
		}

		result, err := p.Reword(context.Background(), commit.RewordRequest{
			Message:  msg,
			Branch:   branch,
			Trailers: trailers,
			DryRun:   flags.dryRun,
		})
		if err != nil {
			code := 1
			if ce, ok := err.(*commit.CommitError); ok {
				code = ce.Code
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(code)
		}

		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  ref: %s\n", result.Ref)
			fmt.Fprintf(os.Stderr, "  old: %s\n", result.OldSHA)
			fmt.Fprintf(os.Stderr, "  sha: %s\n", result.SHA)
		}

		if err := maybeAutoBumpParent(context.Background(), flags, gitDir, result.SHA, "reword", firstLine(msg)); err != nil {
			fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
			os.Exit(1)
		}

		recordCommitRefUpdate(flags, result.Ref, result.SHA, result.OldSHA)

		if !flags.quiet {
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
			if flags.dryRun {
				fmt.Printf(" would reword (was %s)\n", result.OldSHA[:8])
			} else {
				fmt.Printf(" reworded (was %s)\n", result.OldSHA[:8])
			}
		}
	}
}
