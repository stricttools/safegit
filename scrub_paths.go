package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/stricttools/safegit/internal/git"
)

// A recipe operation whose target is paths renames tree entries: every file
// or directory name, at any depth, that its pattern matches is rewritten with
// its replace text. The helpers below build that renamer and answer, for the
// preview and for both verification tiers, which names a pattern still
// matches.

// recipePathRenamer is the recipe's path operations applied, in topological
// order, to one entry name, or nil when no operation targets paths. A rename
// to a name no tree entry can carry is refused.
func recipePathRenamer(recipe *ParsedRecipe) entryRenamer {
	var ops []int
	for _, idx := range recipe.TopoOrder {
		if recipe.Operations[idx].appliesTo(TargetPaths) {
			ops = append(ops, idx)
		}
	}
	if len(ops) == 0 {
		return nil
	}
	return func(name string) (string, error) {
		renamed := name
		for _, idx := range ops {
			renamed = recipe.Patterns[idx].ReplaceAllString(renamed, *recipe.Operations[idx].Replace)
		}
		if renamed == name {
			return name, nil
		}
		if renamed == "" || renamed == "." || renamed == ".." || strings.ContainsAny(renamed, "/\x00") {
			return "", fmt.Errorf("the path operations rename the entry %q to %q, which no tree entry can be named", name, renamed)
		}
		return renamed, nil
	}
}

// recipeObjectTypes are the object types ("blob", "commit", "tag") whose
// content the operation rewrites, the matches its verification counts.
func recipeObjectTypes(op RecipeOperation) map[string]bool {
	return map[string]bool{
		"blob":   op.appliesTo(TargetBlobs),
		"commit": op.appliesTo(TargetCommits),
		"tag":    op.appliesTo(TargetTags),
	}
}

// rangeRootTrees are the root trees of the commits a rewrite range covers:
// every commit of HEAD's history, or fromSHA and the commits after it.
func rangeRootTrees(ctx context.Context, fromSHA string, entireHistory bool) ([]string, error) {
	if entireHistory {
		return rootTreesOf(ctx, []string{"HEAD"})
	}
	return rootTreesOf(ctx, []string{"HEAD", "--not", fromSHA + "^@"})
}

// rootTreesOf are the root trees of the commits git log lists for revs.
func rootTreesOf(ctx context.Context, revs []string) ([]string, error) {
	out, _, err := git.Run(ctx, append([]string{"log", "--format=%T"}, revs...)...)
	if err != nil {
		return nil, fmt.Errorf("listing the root trees of %s: %w", strings.Join(revs, " "), err)
	}
	return git.SplitNonEmpty(out), nil
}

// pathNameMatches are, for each pattern, the sorted paths of the entries
// under rootTrees whose names it matches. Each tree is read once, so a tree
// reached at several paths is reported at the first.
func pathNameMatches(ctx context.Context, rootTrees []string, patterns []*regexp.Regexp) ([][]string, error) {
	ctx, store := git.WithObjectStore(ctx)
	defer store.Close()
	found := make([]map[string]bool, len(patterns))
	for i := range found {
		found[i] = make(map[string]bool)
	}
	visited := make(map[string]bool)
	var walk func(tree, prefix string) error
	walk = func(tree, prefix string) error {
		if visited[tree] {
			return nil
		}
		visited[tree] = true
		entries, err := git.LsTree(ctx, tree)
		if err != nil {
			return fmt.Errorf("ls-tree %s: %w", tree, err)
		}
		for _, e := range entries {
			for i, pat := range patterns {
				if pat.MatchString(e.Path) {
					found[i][prefix+e.Path] = true
				}
			}
			if e.ObjectType == "tree" {
				if err := walk(e.SHA, prefix+e.Path+"/"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, root := range rootTrees {
		if err := walk(root, ""); err != nil {
			return nil, err
		}
	}
	out := make([][]string, len(patterns))
	for i, set := range found {
		for p := range set {
			out[i] = append(out[i], p)
		}
		sort.Strings(out[i])
	}
	return out, nil
}

// storeNameMatches are the entries of every tree object in the object store,
// reachable or not, whose names pattern matches, each spelled "tree <id>:
// <name>".
func storeNameMatches(ctx context.Context, pattern *regexp.Regexp) ([]string, error) {
	out, _, err := git.Run(ctx, "cat-file", "--batch-all-objects", "--batch-check=%(objecttype) %(objectname)")
	if err != nil {
		return nil, fmt.Errorf("listing the object store: %w", err)
	}
	ctx, store := git.WithObjectStore(ctx)
	defer store.Close()
	var matches []string
	for _, line := range git.SplitNonEmpty(out) {
		objectType, id, ok := strings.Cut(line, " ")
		if !ok || objectType != "tree" {
			continue
		}
		entries, err := git.LsTree(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("ls-tree %s: %w", id, err)
		}
		for _, e := range entries {
			if pattern.MatchString(e.Path) {
				matches = append(matches, fmt.Sprintf("tree %s: %s", shortSHA(id), e.Path))
			}
		}
	}
	return matches, nil
}

// describeNameMatches renders surviving path names for a verification error.
func describeNameMatches(what string, names []string) error {
	return fmt.Errorf("the pattern still matches %d path name(s) %s:\n    %s", len(names), what, strings.Join(names, "\n    "))
}

// verifyNamesAbsentFromTips is Tier A for a path operation: no entry name of
// the history the rewritten tips reach matches pattern.
func verifyNamesAbsentFromTips(ctx context.Context, pattern *regexp.Regexp, tips []string) error {
	if len(tips) == 0 {
		return nil
	}
	roots, err := rootTreesOf(ctx, tips)
	if err != nil {
		return err
	}
	found, err := pathNameMatches(ctx, roots, []*regexp.Regexp{pattern})
	if err != nil {
		return err
	}
	if len(found[0]) > 0 {
		return describeNameMatches("in the rewritten history", found[0])
	}
	return nil
}

// verifyNamesRemoved is Tier B for a path operation: no tree object of the
// whole object store holds an entry name pattern matches.
func verifyNamesRemoved(ctx context.Context, pattern *regexp.Regexp) error {
	found, err := storeNameMatches(ctx, pattern)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return describeNameMatches("in the object store", found)
	}
	return nil
}
