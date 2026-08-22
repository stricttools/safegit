package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// replaceInTree returns a new tree SHA with the blob at filePath replaced by
// newBlobSHA (or removed if newBlobSHA is empty). The original tree is never
// mutated. If filePath does not exist in the tree, the original treeSHA is
// returned unchanged (no error) so the scrub walker can skip commits where
// the file is absent.
func replaceInTree(ctx context.Context, treeSHA string, filePath string, newBlobSHA string, cache map[string]string) (string, error) {
	if cached, ok := cache[treeSHA]; ok {
		return cached, nil
	}

	segments := strings.SplitN(filePath, "/", 2)
	name := segments[0]

	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		return "", fmt.Errorf("ls-tree %s: %w", treeSHA, err)
	}

	idx := -1
	for i, e := range entries {
		if e.Path == name {
			idx = i
			break
		}
	}

	// File not found in this tree level -- return original tree unchanged.
	if idx < 0 {
		cache[treeSHA] = treeSHA
		return treeSHA, nil
	}

	if len(segments) == 2 {
		// Nested path: recurse into the subtree.
		subtreeEntry := entries[idx]
		if subtreeEntry.ObjectType != "tree" {
			// Path component exists but is not a tree -- file not found.
			cache[treeSHA] = treeSHA
			return treeSHA, nil
		}
		newSubtreeSHA, err := replaceInTree(ctx, subtreeEntry.SHA, segments[1], newBlobSHA, cache)
		if err != nil {
			return "", err
		}
		// If the subtree didn't change (file not found deeper), propagate.
		if newSubtreeSHA == subtreeEntry.SHA {
			cache[treeSHA] = treeSHA
			return treeSHA, nil
		}
		entries[idx].SHA = newSubtreeSHA
	} else {
		// Base case: leaf entry.
		if newBlobSHA == "" {
			// Remove the entry.
			entries = append(entries[:idx], entries[idx+1:]...)
		} else {
			// Replace the blob SHA, preserving mode and type.
			entries[idx].SHA = newBlobSHA
		}
	}

	newTreeSHA, err := git.MkTree(ctx, entries)
	if err != nil {
		return "", fmt.Errorf("mktree: %w", err)
	}
	cache[treeSHA] = newTreeSHA
	return newTreeSHA, nil
}

// lookupBlobAtPath returns the blob SHA at the given file path within a tree,
// or "" if the path does not exist. This is used to track which old blobs
// get replaced during a scrub rewrite.
func lookupBlobAtPath(ctx context.Context, treeSHA string, filePath string) string {
	segments := strings.SplitN(filePath, "/", 2)
	name := segments[0]

	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		return ""
	}

	for _, e := range entries {
		if e.Path != name {
			continue
		}
		if len(segments) == 2 {
			// Nested path: recurse into the subtree.
			if e.ObjectType != "tree" {
				return ""
			}
			return lookupBlobAtPath(ctx, e.SHA, segments[1])
		}
		// Leaf entry.
		if e.ObjectType == "blob" {
			return e.SHA
		}
		return ""
	}
	return ""
}

// treeRewrite is one tree transformation, cached: the tree the operation
// produced, and the paths -- relative to the tree that was transformed -- whose
// entries it decided to change.
//
// The paths are the operation's own DECLARATION of what it set out to change,
// collected while the decisions are being made rather than re-derived from the
// result afterwards. Tier A verification checks the produced commits against
// this declaration, so a re-derivation would be comparing the rewrite to itself.
type treeRewrite struct {
	SHA   string
	Paths []string
}

// replaceInTreeByBlobMap walks a tree recursively and replaces any blob whose
// SHA is a key in blobMap with the corresponding value. Subtrees are recursed
// into. Gitlink entries (submodules, ObjectType "commit") are passed through
// unchanged unless gitlinkMap is non-nil and contains a mapping for the SHA.
// If no entries match, the original treeSHA is returned unchanged.
//
// It returns the paths whose entries it replaced, relative to treeSHA, so the
// caller can declare per commit what the rewrite is supposed to change. The
// cache carries those paths with each cached tree: a cache hit short-circuits
// the recursion, and without them a tree seen twice would report its changes
// only the first time.
func replaceInTreeByBlobMap(ctx context.Context, treeSHA string, blobMap map[string]string, gitlinkMap map[string]string, cache map[string]treeRewrite) (string, []string, error) {
	if cached, ok := cache[treeSHA]; ok {
		return cached.SHA, cached.Paths, nil
	}

	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		return "", nil, fmt.Errorf("ls-tree %s: %w", treeSHA, err)
	}

	var changedPaths []string
	for i, e := range entries {
		switch e.ObjectType {
		case "blob":
			if newSHA, ok := blobMap[e.SHA]; ok {
				entries[i].SHA = newSHA
				changedPaths = append(changedPaths, e.Path)
			}
		case "tree":
			newSubSHA, subPaths, err := replaceInTreeByBlobMap(ctx, e.SHA, blobMap, gitlinkMap, cache)
			if err != nil {
				return "", nil, err
			}
			if newSubSHA != e.SHA {
				entries[i].SHA = newSubSHA
				for _, p := range subPaths {
					changedPaths = append(changedPaths, e.Path+"/"+p)
				}
			}
		case "commit":
			// Gitlink (submodule reference). Pass through unchanged unless
			// gitlinkMap has an explicit remapping for this SHA.
			if gitlinkMap != nil {
				if newSHA, ok := gitlinkMap[e.SHA]; ok {
					entries[i].SHA = newSHA
					changedPaths = append(changedPaths, e.Path)
				}
			}
		}
	}

	if len(changedPaths) == 0 {
		cache[treeSHA] = treeRewrite{SHA: treeSHA}
		return treeSHA, nil, nil
	}

	newTreeSHA, err := git.MkTree(ctx, entries)
	if err != nil {
		return "", nil, fmt.Errorf("mktree: %w", err)
	}
	cache[treeSHA] = treeRewrite{SHA: newTreeSHA, Paths: changedPaths}
	return newTreeSHA, changedPaths, nil
}
