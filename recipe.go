package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"

	tomledit "github.com/smm-h/go-toml-edit"
	"github.com/smm-h/safegit/internal/git"
)

// Recipe is the raw TOML schema for a scrub recipe file.
type Recipe struct {
	Operations []RecipeOperation `toml:"operations"`
}

// RecipeOperation is a single operation within a recipe.
type RecipeOperation struct {
	Pattern   string  `toml:"pattern"`
	Replace   *string `toml:"replace"`    // nil if not set
	Mangle    bool    `toml:"mangle"`     // true to mangle matches
	Scope     *string `toml:"scope"`      // nil if not set
	Target    *string `toml:"target"`     // nil = all
	DependsOn []int   `toml:"depends_on"` // zero-indexed operation indices
}

// ParsedRecipe is the validated, compiled form of a Recipe.
type ParsedRecipe struct {
	Operations []RecipeOperation
	Patterns   []*regexp.Regexp // compiled patterns, indexed same as Operations
	TopoOrder  []int            // topologically sorted operation indices
}

// parseRecipe reads a TOML recipe file, validates it, compiles patterns, and
// returns a ParsedRecipe with operations in topological order.
func parseRecipe(path string) (*ParsedRecipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading recipe file: %w", err)
	}

	var recipe Recipe
	if err := tomledit.Unmarshal(data, &recipe); err != nil {
		return nil, fmt.Errorf("parsing recipe TOML: %w", err)
	}

	if len(recipe.Operations) == 0 {
		return nil, fmt.Errorf("recipe must contain at least one operation")
	}

	n := len(recipe.Operations)
	patterns := make([]*regexp.Regexp, n)

	for i, op := range recipe.Operations {
		// Validate pattern is present.
		if op.Pattern == "" {
			return nil, fmt.Errorf("operation %d: pattern is required", i)
		}

		// Validate exactly one of replace/mangle.
		hasReplace := op.Replace != nil
		hasMangle := op.Mangle
		if hasReplace && hasMangle {
			return nil, fmt.Errorf("operation %d: cannot set both replace and mangle", i)
		}
		if !hasReplace && !hasMangle {
			return nil, fmt.Errorf("operation %d: must set exactly one of replace or mangle", i)
		}

		// Compile the regex pattern.
		compiled, err := regexp.Compile(op.Pattern)
		if err != nil {
			return nil, fmt.Errorf("operation %d: invalid regex pattern %q: %w", i, op.Pattern, err)
		}
		patterns[i] = compiled

		// Validate depends_on indices.
		for _, dep := range op.DependsOn {
			if dep < 0 || dep >= n {
				return nil, fmt.Errorf("operation %d: depends_on index %d out of range [0, %d)", i, dep, n)
			}
			if dep == i {
				return nil, fmt.Errorf("operation %d: depends_on cannot reference self", i)
			}
		}
	}

	// Validate the dependency graph is a DAG (no cycles).
	topoOrder, err := topoSort(n, recipe.Operations)
	if err != nil {
		return nil, err
	}

	return &ParsedRecipe{
		Operations: recipe.Operations,
		Patterns:   patterns,
		TopoOrder:  topoOrder,
	}, nil
}

// topoSort performs Kahn's algorithm on the operation dependency graph. Returns
// indices in topological order (independent operations first, then dependent
// ones). Returns an error if the graph contains cycles.
func topoSort(n int, ops []RecipeOperation) ([]int, error) {
	// Build adjacency list and in-degree counts.
	// Edge: depends_on[j] -> i means "i depends on j", so j must come before i.
	inDegree := make([]int, n)
	// successors[j] = list of operations that depend on j
	successors := make([][]int, n)
	for i := range successors {
		successors[i] = nil
	}

	for i, op := range ops {
		inDegree[i] = len(op.DependsOn)
		for _, dep := range op.DependsOn {
			successors[dep] = append(successors[dep], i)
		}
	}

	// Seed the queue with all operations that have no dependencies.
	// Use a sorted queue for deterministic output.
	var queue []int
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}

	var order []int
	for len(queue) > 0 {
		// Sort the queue to ensure deterministic ordering among
		// operations at the same topological level.
		sort.Ints(queue)

		// Pop the smallest index.
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		for _, succ := range successors[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	if len(order) != n {
		return nil, fmt.Errorf("recipe dependency graph contains a cycle")
	}

	return order, nil
}

// byteRange represents a matched byte range [start, end) within a blob.
type byteRange struct {
	start int
	end   int
	opIdx int // which operation produced this match
}

// byteReplacement pairs a matched byte range with its replacement data.
type byteReplacement struct {
	br      byteRange
	newData []byte
}

// BuildRecipeBlobContent applies a parsed recipe to a set of blobs, producing
// a mapping from old blob SHA to modified content bytes. It reads each blob,
// applies recipe operations in memory, and returns only blobs whose content
// changed. No objects are written to the object store -- this is purely
// in-memory content computation for dry-run and diff use cases.
func BuildRecipeBlobContent(ctx context.Context, recipe *ParsedRecipe, blobSHAs []string) (map[string][]byte, error) {
	result := make(map[string][]byte)

	for _, blobSHA := range blobSHAs {
		content, err := git.CatFileBlob(ctx, blobSHA)
		if err != nil {
			return nil, fmt.Errorf("reading blob %s: %w", blobSHA, err)
		}

		// Skip binary blobs.
		if isBinaryContent(content) {
			continue
		}

		modified, err := applyRecipeToContent(recipe, content)
		if err != nil {
			return nil, fmt.Errorf("blob %s: %w", blobSHA, err)
		}

		if bytes.Equal(content, modified) {
			continue
		}

		result[blobSHA] = modified
	}

	return result, nil
}

// buildRecipeBlobMap applies a parsed recipe to a set of blobs, producing a
// mapping from old blob SHA to new blob SHA. Operations are applied in
// topological order. Independent operations (no depends_on) have their matches
// collected against the ORIGINAL content and applied simultaneously (offset-
// descending to preserve positions). Overlapping byte ranges across independent
// operations are a hard error. Dependent operations match against the post-
// dependency content.
func buildRecipeBlobMap(ctx context.Context, recipe *ParsedRecipe, blobSHAs []string) (map[string]string, error) {
	contentMap, err := BuildRecipeBlobContent(ctx, recipe, blobSHAs)
	if err != nil {
		return nil, err
	}

	blobMap := make(map[string]string, len(contentMap))
	for oldSHA, modified := range contentMap {
		newSHA, err := git.HashObjectWriteBytes(ctx, modified)
		if err != nil {
			return nil, fmt.Errorf("writing replaced blob for %s: %w", oldSHA, err)
		}
		blobMap[oldSHA] = newSHA
	}

	return blobMap, nil
}

// applyRecipeToContent applies all recipe operations to content in topological
// order, handling independent and dependent operations correctly.
func applyRecipeToContent(recipe *ParsedRecipe, content []byte) ([]byte, error) {
	n := len(recipe.Operations)

	// Track which operations are "independent" (no depends_on).
	isIndependent := make([]bool, n)
	for i, op := range recipe.Operations {
		isIndependent[i] = len(op.DependsOn) == 0
	}

	// current tracks the content state. Initially it's the original content.
	// After independent ops are applied simultaneously, it becomes the
	// post-independent content. Dependent ops then match against current.
	current := content

	// Phase 1: Collect all independent operation matches against original content.
	var allRanges []byteRange
	var replacements []byteReplacement

	// Walk topo order: process all independent ops first (they come first in
	// topo order since they have no dependencies).
	independentDone := false
	for _, idx := range recipe.TopoOrder {
		if isIndependent[idx] {
			op := recipe.Operations[idx]
			pat := recipe.Patterns[idx]

			matches := pat.FindAllIndex(current, -1)
			for _, m := range matches {
				br := byteRange{start: m[0], end: m[1], opIdx: idx}
				allRanges = append(allRanges, br)

				var newData []byte
				if op.Mangle {
					newData = mangleBytes(current[m[0]:m[1]])
				} else {
					// Use ReplaceAll on just the matched segment to handle
					// backreferences correctly.
					newData = pat.ReplaceAll(current[m[0]:m[1]], []byte(*op.Replace))
				}
				replacements = append(replacements, byteReplacement{br: br, newData: newData})
			}
		} else {
			if !independentDone {
				// We've reached the first dependent op. Apply all independent
				// replacements simultaneously before proceeding.
				if len(replacements) > 0 {
					// Check for overlapping ranges across independent ops.
					if err := checkOverlaps(allRanges); err != nil {
						return nil, err
					}

					current = applyReplacements(current, replacements)
				}
				independentDone = true
			}

			// Phase 2: Apply dependent operation against post-dependency content.
			op := recipe.Operations[idx]
			pat := recipe.Patterns[idx]

			if op.Mangle {
				current = pat.ReplaceAllFunc(current, mangleBytes)
			} else {
				current = pat.ReplaceAll(current, []byte(*op.Replace))
			}
		}
	}

	// If all operations were independent, apply them now.
	if !independentDone && len(replacements) > 0 {
		if err := checkOverlaps(allRanges); err != nil {
			return nil, err
		}
		current = applyReplacements(current, replacements)
	}

	return current, nil
}

// checkOverlaps detects overlapping byte ranges across different operations.
// Ranges from the same operation cannot overlap (regex FindAllIndex returns
// non-overlapping matches). Overlaps across different operations are a hard error.
func checkOverlaps(ranges []byteRange) error {
	if len(ranges) <= 1 {
		return nil
	}

	// Sort by start position, then by end position.
	sorted := make([]byteRange, len(ranges))
	copy(sorted, ranges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end < sorted[j].end
	})

	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1]
		curr := sorted[i]
		// Two ranges overlap if curr.start < prev.end (since ranges are [start, end)).
		if curr.start < prev.end && prev.opIdx != curr.opIdx {
			return fmt.Errorf("overlapping byte ranges between operation %d [%d,%d) and operation %d [%d,%d)",
				prev.opIdx, prev.start, prev.end, curr.opIdx, curr.start, curr.end)
		}
	}

	return nil
}

// applyReplacements applies all replacements simultaneously by sorting them in
// descending offset order and applying from end to start, so earlier offsets
// remain valid.
func applyReplacements(content []byte, repls []byteReplacement) []byte {
	// Sort by start offset descending.
	sort.Slice(repls, func(i, j int) bool {
		return repls[i].br.start > repls[j].br.start
	})

	result := make([]byte, len(content))
	copy(result, content)

	for _, r := range repls {
		// Replace the byte range [start, end) with newData.
		var buf []byte
		buf = append(buf, result[:r.br.start]...)
		buf = append(buf, r.newData...)
		buf = append(buf, result[r.br.end:]...)
		result = buf
	}

	return result
}
