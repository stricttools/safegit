package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
)

// writeRecipeFile writes TOML content to a temp file and returns its path.
func writeRecipeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing recipe file: %v", err)
	}
	return path
}

func TestParseRecipeValid(t *testing.T) {
	toml := `
[[operations]]
pattern = "secret_key_\\w+"
replace = "REDACTED"

[[operations]]
pattern = "password=\\S+"
mangle = true
scope = "*.env"

[[operations]]
pattern = "REDACTED"
replace = "[REMOVED]"
depends_on = [0]
`
	path := writeRecipeFile(t, toml)
	recipe, err := parseRecipe(path)
	if err != nil {
		t.Fatalf("parseRecipe: %v", err)
	}

	if len(recipe.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(recipe.Operations))
	}

	// Operation 0: replace mode.
	if recipe.Operations[0].Replace == nil || *recipe.Operations[0].Replace != "REDACTED" {
		t.Errorf("op 0: expected replace=REDACTED")
	}
	if recipe.Operations[0].Mangle {
		t.Errorf("op 0: expected mangle=false")
	}

	// Operation 1: mangle mode with scope.
	if recipe.Operations[1].Replace != nil {
		t.Errorf("op 1: expected replace=nil")
	}
	if !recipe.Operations[1].Mangle {
		t.Errorf("op 1: expected mangle=true")
	}
	if recipe.Operations[1].Scope == nil || *recipe.Operations[1].Scope != "*.env" {
		t.Errorf("op 1: expected scope=*.env")
	}

	// Operation 2: depends on op 0.
	if len(recipe.Operations[2].DependsOn) != 1 || recipe.Operations[2].DependsOn[0] != 0 {
		t.Errorf("op 2: expected depends_on=[0], got %v", recipe.Operations[2].DependsOn)
	}

	// Check compiled patterns.
	if recipe.Patterns[0] == nil || recipe.Patterns[1] == nil || recipe.Patterns[2] == nil {
		t.Fatal("expected all patterns to be compiled")
	}

	// Check topo order: ops 0 and 1 are independent, op 2 depends on 0.
	// So order should be [0, 1, 2] (independent first, deterministic by index).
	if len(recipe.TopoOrder) != 3 {
		t.Fatalf("expected topo order length 3, got %d", len(recipe.TopoOrder))
	}
	// Op 0 and 1 must come before op 2.
	pos := make(map[int]int)
	for i, idx := range recipe.TopoOrder {
		pos[idx] = i
	}
	if pos[0] >= pos[2] {
		t.Errorf("topo order: op 0 (pos %d) should come before op 2 (pos %d)", pos[0], pos[2])
	}
	if pos[1] >= pos[2] {
		t.Errorf("topo order: op 1 (pos %d) should come before op 2 (pos %d)", pos[1], pos[2])
	}
}

func TestParseRecipeMissingPattern(t *testing.T) {
	toml := `
[[operations]]
replace = "REDACTED"
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("expected 'pattern is required' in error, got: %v", err)
	}
}

func TestParseRecipeBothReplaceAndMangle(t *testing.T) {
	toml := `
[[operations]]
pattern = "foo"
replace = "bar"
mangle = true
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for both replace and mangle")
	}
	if !strings.Contains(err.Error(), "cannot set both replace and mangle") {
		t.Errorf("expected 'cannot set both replace and mangle' in error, got: %v", err)
	}
}

func TestParseRecipeNeitherReplaceNorMangle(t *testing.T) {
	toml := `
[[operations]]
pattern = "foo"
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for neither replace nor mangle")
	}
	if !strings.Contains(err.Error(), "must set exactly one of replace or mangle") {
		t.Errorf("expected 'must set exactly one' in error, got: %v", err)
	}
}

func TestParseRecipeCyclicDependsOn(t *testing.T) {
	toml := `
[[operations]]
pattern = "a"
replace = "A"
depends_on = [1]

[[operations]]
pattern = "b"
replace = "B"
depends_on = [0]
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for cyclic depends_on")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected 'cycle' in error, got: %v", err)
	}
}

func TestParseRecipeOutOfRangeDependsOn(t *testing.T) {
	toml := `
[[operations]]
pattern = "a"
replace = "A"
depends_on = [5]
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for out-of-range depends_on")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected 'out of range' in error, got: %v", err)
	}
}

func TestParseRecipeSelfReference(t *testing.T) {
	toml := `
[[operations]]
pattern = "a"
replace = "A"
depends_on = [0]
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for self-referencing depends_on")
	}
	if !strings.Contains(err.Error(), "cannot reference self") {
		t.Errorf("expected 'cannot reference self' in error, got: %v", err)
	}
}

func TestTopoSortLinearChain(t *testing.T) {
	// 0 -> 1 -> 2 (2 depends on 1, 1 depends on 0)
	ops := []RecipeOperation{
		{Pattern: "a", DependsOn: nil},
		{Pattern: "b", DependsOn: []int{0}},
		{Pattern: "c", DependsOn: []int{1}},
	}
	order, err := topoSort(3, ops)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(order))
	}

	// Must be exactly [0, 1, 2].
	for i, expected := range []int{0, 1, 2} {
		if order[i] != expected {
			t.Errorf("order[%d] = %d, want %d", i, order[i], expected)
		}
	}
}

func TestTopoSortDiamond(t *testing.T) {
	// 0 and 1 are independent, 2 depends on both, 3 depends on 2.
	ops := []RecipeOperation{
		{Pattern: "a", DependsOn: nil},
		{Pattern: "b", DependsOn: nil},
		{Pattern: "c", DependsOn: []int{0, 1}},
		{Pattern: "d", DependsOn: []int{2}},
	}
	order, err := topoSort(4, ops)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}

	pos := make(map[int]int)
	for i, idx := range order {
		pos[idx] = i
	}

	// 0 and 1 must come before 2, 2 must come before 3.
	if pos[0] >= pos[2] {
		t.Errorf("op 0 (pos %d) should come before op 2 (pos %d)", pos[0], pos[2])
	}
	if pos[1] >= pos[2] {
		t.Errorf("op 1 (pos %d) should come before op 2 (pos %d)", pos[1], pos[2])
	}
	if pos[2] >= pos[3] {
		t.Errorf("op 2 (pos %d) should come before op 3 (pos %d)", pos[2], pos[3])
	}
}

func TestTopoSortAllIndependent(t *testing.T) {
	ops := []RecipeOperation{
		{Pattern: "a", DependsOn: nil},
		{Pattern: "b", DependsOn: nil},
		{Pattern: "c", DependsOn: nil},
	}
	order, err := topoSort(3, ops)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	// All independent: should be in index order [0, 1, 2].
	for i, expected := range []int{0, 1, 2} {
		if order[i] != expected {
			t.Errorf("order[%d] = %d, want %d", i, order[i], expected)
		}
	}
}

// --- buildRecipeBlobMap tests ---

func TestBuildRecipeBlobMapIndependentNonOverlapping(t *testing.T) {
	dir := initTestRepo(t)
	ctx := context.Background()

	// Create a blob with content that has 3 non-overlapping match sites.
	content := "aaa bbb ccc"
	blobSHA := hashBlob(t, dir, content)

	replaceA := "AAA"
	replaceB := "BBB"
	replaceC := "CCC"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "aaa", Replace: &replaceA},
			{Pattern: "bbb", Replace: &replaceB},
			{Pattern: "ccc", Replace: &replaceC},
		},
		Patterns: compilePatterns(t, "aaa", "bbb", "ccc"),
		TopoOrder: []int{0, 1, 2},
	}

	blobMap, err := buildRecipeBlobMap(ctx, recipe, []string{blobSHA})
	if err != nil {
		t.Fatalf("buildRecipeBlobMap: %v", err)
	}

	if len(blobMap) != 1 {
		t.Fatalf("expected 1 entry in blobMap, got %d", len(blobMap))
	}

	newSHA := blobMap[blobSHA]
	newContent, err := readBlob(ctx, newSHA)
	if err != nil {
		t.Fatalf("reading new blob: %v", err)
	}

	expected := "AAA BBB CCC"
	if string(newContent) != expected {
		t.Errorf("expected %q, got %q", expected, string(newContent))
	}
}

func TestBuildRecipeBlobMapDependsOnChain(t *testing.T) {
	dir := initTestRepo(t)
	ctx := context.Background()

	// op0: "foo" -> "bar", op1 (depends on 0): "bar" -> "baz"
	// Result: "foo" -> "baz"
	content := "foo"
	blobSHA := hashBlob(t, dir, content)

	replaceBar := "bar"
	replaceBaz := "baz"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "foo", Replace: &replaceBar},
			{Pattern: "bar", Replace: &replaceBaz, DependsOn: []int{0}},
		},
		Patterns:  compilePatterns(t, "foo", "bar"),
		TopoOrder: []int{0, 1},
	}

	blobMap, err := buildRecipeBlobMap(ctx, recipe, []string{blobSHA})
	if err != nil {
		t.Fatalf("buildRecipeBlobMap: %v", err)
	}

	if len(blobMap) != 1 {
		t.Fatalf("expected 1 entry in blobMap, got %d", len(blobMap))
	}

	newSHA := blobMap[blobSHA]
	newContent, err := readBlob(ctx, newSHA)
	if err != nil {
		t.Fatalf("reading new blob: %v", err)
	}

	if string(newContent) != "baz" {
		t.Errorf("expected %q, got %q", "baz", string(newContent))
	}
}

func TestBuildRecipeBlobMapOverlappingRangesError(t *testing.T) {
	dir := initTestRepo(t)
	ctx := context.Background()

	// Two independent ops with overlapping matches on "foobar":
	// op0 matches "foob", op1 matches "obar" -- they overlap at "ob".
	content := "foobar"
	blobSHA := hashBlob(t, dir, content)

	replace1 := "XXXX"
	replace2 := "YYYY"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "foob", Replace: &replace1},
			{Pattern: "obar", Replace: &replace2},
		},
		Patterns:  compilePatterns(t, "foob", "obar"),
		TopoOrder: []int{0, 1},
	}

	_, err := buildRecipeBlobMap(ctx, recipe, []string{blobSHA})
	if err == nil {
		t.Fatal("expected error for overlapping byte ranges")
	}
	if !strings.Contains(err.Error(), "overlapping byte ranges") {
		t.Errorf("expected 'overlapping byte ranges' in error, got: %v", err)
	}
}

func TestApplyRecipeToContentIndependentSimultaneous(t *testing.T) {
	// Verify that independent operations are applied simultaneously against the
	// original content, not sequentially (which would cause op1's match to fail
	// if op0's replacement changed the content layout).
	replaceA := "XXXXX" // longer than "aaa" to shift offsets
	replaceB := "Y"     // shorter than "bbb"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "aaa", Replace: &replaceA},
			{Pattern: "bbb", Replace: &replaceB},
		},
		Patterns:  compilePatterns(t, "aaa", "bbb"),
		TopoOrder: []int{0, 1},
	}

	content := []byte("aaa bbb")
	result, err := applyRecipeToContent(recipe, content)
	if err != nil {
		t.Fatalf("applyRecipeToContent: %v", err)
	}

	expected := "XXXXX Y"
	if string(result) != expected {
		t.Errorf("expected %q, got %q", expected, string(result))
	}
}

func TestApplyRecipeToContentDependentMatchesPostDependency(t *testing.T) {
	// op0 replaces "hello" with "world", op1 (depends on 0) replaces "world" with "done".
	// If op1 matched against original content, it would find nothing (no "world" in original).
	// Since it depends on op0, it should match against post-op0 content.
	replaceWorld := "world"
	replaceDone := "done"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "hello", Replace: &replaceWorld},
			{Pattern: "world", Replace: &replaceDone, DependsOn: []int{0}},
		},
		Patterns:  compilePatterns(t, "hello", "world"),
		TopoOrder: []int{0, 1},
	}

	content := []byte("hello")
	result, err := applyRecipeToContent(recipe, content)
	if err != nil {
		t.Fatalf("applyRecipeToContent: %v", err)
	}

	if string(result) != "done" {
		t.Errorf("expected %q, got %q", "done", string(result))
	}
}

func TestParseRecipeEmptyOperations(t *testing.T) {
	toml := `
# Empty recipe
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for empty operations")
	}
	if !strings.Contains(err.Error(), "at least one operation") {
		t.Errorf("expected 'at least one operation' in error, got: %v", err)
	}
}

func TestParseRecipeInvalidRegex(t *testing.T) {
	toml := `
[[operations]]
pattern = "[invalid"
replace = "x"
`
	path := writeRecipeFile(t, toml)
	_, err := parseRecipe(path)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex pattern") {
		t.Errorf("expected 'invalid regex pattern' in error, got: %v", err)
	}
}

func TestParseRecipeReplaceEmptyString(t *testing.T) {
	// Replacing with empty string is valid (deletion).
	toml := `
[[operations]]
pattern = "secret"
replace = ""
`
	path := writeRecipeFile(t, toml)
	recipe, err := parseRecipe(path)
	if err != nil {
		t.Fatalf("parseRecipe: %v", err)
	}
	if recipe.Operations[0].Replace == nil {
		t.Error("expected replace to be non-nil (empty string)")
	}
	if *recipe.Operations[0].Replace != "" {
		t.Errorf("expected replace to be empty string, got %q", *recipe.Operations[0].Replace)
	}
}

// --- BuildRecipeBlobContent tests ---

func TestBuildRecipeBlobContentNoWrite(t *testing.T) {
	dir := initTestRepo(t)
	ctx := context.Background()

	// Create a blob with known content.
	content := "secret_key_abc123"
	blobSHA := hashBlob(t, dir, content)

	// Count objects before applying the recipe.
	countBefore := countObjects(t, dir)

	replaceVal := "REDACTED"
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{
			{Pattern: "secret_key_\\w+", Replace: &replaceVal},
		},
		Patterns:  compilePatterns(t, "secret_key_\\w+"),
		TopoOrder: []int{0},
	}

	contentMap, err := BuildRecipeBlobContent(ctx, recipe, []string{blobSHA})
	if err != nil {
		t.Fatalf("BuildRecipeBlobContent: %v", err)
	}

	// Verify the map contains the expected replacement.
	if len(contentMap) != 1 {
		t.Fatalf("expected 1 entry in contentMap, got %d", len(contentMap))
	}

	modified, ok := contentMap[blobSHA]
	if !ok {
		t.Fatalf("contentMap missing entry for blob %s", blobSHA)
	}

	if string(modified) != "REDACTED" {
		t.Errorf("expected %q, got %q", "REDACTED", string(modified))
	}

	// Verify no new objects were written to the object store.
	countAfter := countObjects(t, dir)
	if countAfter != countBefore {
		t.Errorf("object count changed: before=%d, after=%d; expected no new objects", countBefore, countAfter)
	}
}

// countObjects returns the number of loose objects in the git object store.
func countObjects(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "count-objects")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git count-objects: %v\n%s", err, out)
	}
	// Output format: "N objects, M kilobytes"
	parts := strings.Fields(string(out))
	if len(parts) < 1 {
		t.Fatalf("unexpected git count-objects output: %q", string(out))
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatalf("parsing object count from %q: %v", string(out), err)
	}
	return n
}

// --- helpers ---

// compilePatterns compiles a list of regex pattern strings for test use.
func compilePatterns(t *testing.T, patterns ...string) []*regexp.Regexp {
	t.Helper()
	result := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		compiled, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("compiling pattern %q: %v", p, err)
		}
		result[i] = compiled
	}
	return result
}

// readBlob reads blob content by SHA.
func readBlob(ctx context.Context, sha string) ([]byte, error) {
	return git.CatFileBlob(ctx, sha)
}
