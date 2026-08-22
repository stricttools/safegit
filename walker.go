package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/git"
)

// TagRewrite records how a tag ref was updated during history rewriting.
type TagRewrite struct {
	Refname   string `json:"refname"`
	OldSHA    string `json:"old_sha"`
	NewSHA    string `json:"new_sha"`
	Annotated bool   `json:"annotated"`
}

// CommitTransform describes how a commit should be rewritten. Zero/empty
// fields mean "keep the original value."
type CommitTransform struct {
	TreeSHA   string         // new tree SHA (empty = use original)
	Message   string         // new message (empty = use original)
	Author    git.AuthorInfo // new author (zero value = use original)
	Committer git.AuthorInfo // new committer (zero value = use original)
}

// TransformFunc is called for each commit during a rewrite walk. It receives
// the original commit SHA, its parsed info, the already-remapped parent SHAs,
// and the growing old-to-new SHA map (which includes identity entries for
// already-walked unchanged commits; transforms must treat it as read-only —
// the walker owns it). It returns a CommitTransform describing what (if
// anything) to change.
type TransformFunc func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error)

// walkAndRewrite iterates commits in the provided topo-order slice, remaps
// parents through earlier rewrites, calls the transform function, and creates
// new commit objects when anything changed. Returns the old-to-new SHA map,
// the count of commits that were actually rewritten (new SHA differs from
// original), and any error.
func walkAndRewrite(ctx context.Context, shas []string, transform TransformFunc, verbose bool) (shaMap map[string]string, rewrittenCount int, err error) {
	shaMap = make(map[string]string, len(shas))

	for _, sha := range shas {
		info, err := git.ParseCommit(ctx, sha)
		if err != nil {
			return nil, 0, fmt.Errorf("parsing commit %s: %w", sha, err)
		}

		// Remap parents through earlier rewrites.
		remappedParents := make([]string, len(info.Parents))
		parentRemapped := false
		for i, p := range info.Parents {
			if mapped, ok := shaMap[p]; ok && mapped != p {
				remappedParents[i] = mapped
				parentRemapped = true
			} else {
				remappedParents[i] = p
			}
		}

		// Ask the caller what to change.
		xform, err := transform(ctx, sha, info, remappedParents, shaMap)
		if err != nil {
			// A refusal is already a complete statement about a specific
			// commit -- it names the commit, what it found there and what to do
			// about it -- so it travels verbatim. Framing it as a failure to
			// transform would be a second, weaker account of the same verdict.
			var refusal *rewriteRefusal
			if errors.As(err, &refusal) {
				return nil, 0, err
			}
			return nil, 0, fmt.Errorf("transforming commit %s: %w", sha, err)
		}

		// Determine effective values (transform overrides or originals).
		treeSHA := info.Tree
		if xform.TreeSHA != "" {
			treeSHA = xform.TreeSHA
		}
		message := info.Message
		if xform.Message != "" {
			message = xform.Message
		}
		author := info.Author
		if !isZeroAuthorInfo(xform.Author) {
			author = xform.Author
		}
		committer := info.Committer
		if !isZeroAuthorInfo(xform.Committer) {
			committer = xform.Committer
		}

		// Check if anything actually changed.
		treeChanged := treeSHA != info.Tree
		messageChanged := message != info.Message
		authorChanged := !isZeroAuthorInfo(xform.Author)
		committerChanged := !isZeroAuthorInfo(xform.Committer)
		needsRewrite := treeChanged || messageChanged || authorChanged || committerChanged || parentRemapped

		if needsRewrite {
			newSHA, err := git.CommitTree(ctx, treeSHA, remappedParents, message,
				&git.CommitIdentity{Author: author, Committer: committer})
			if err != nil {
				return nil, 0, fmt.Errorf("creating rewritten commit for %s: %w", sha, err)
			}
			shaMap[sha] = newSHA
			rewrittenCount++
			if verbose {
				reason := rewriteReason(treeChanged, messageChanged, authorChanged || committerChanged, parentRemapped)
				fmt.Fprintf(os.Stderr, "  %s -> %s  (%s)\n", sha[:12], newSHA[:12], reason)
			}
		} else {
			shaMap[sha] = sha
			if verbose {
				fmt.Fprintf(os.Stderr, "  %s                   (unchanged)\n", sha[:12])
			}
		}
	}

	return shaMap, rewrittenCount, nil
}

// isZeroAuthorInfo returns true when all fields of the AuthorInfo are empty.
func isZeroAuthorInfo(a git.AuthorInfo) bool {
	return a.Name == "" && a.Email == "" && a.Date == ""
}

// rewriteReason returns a human-readable label for verbose output.
func rewriteReason(tree, message, identity, inherited bool) string {
	switch {
	case tree:
		return "tree changed"
	case message:
		return "message changed"
	case identity:
		return "name changed"
	case inherited:
		return "inherited"
	default:
		return "rewritten"
	}
}
