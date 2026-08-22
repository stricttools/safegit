package main

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/scan"
	"github.com/smm-h/strictcli/go/strictcli"
)

// Where a verified pattern came from. The two spellings are the two input
// members of the command's required input selection.
const (
	scrubVerifySourceFlag   = "flag"
	scrubVerifySourceRecipe = "recipe"
)

// ScrubVerifyPatternResult is the per-pattern result for JSON output.
type ScrubVerifyPatternResult struct {
	Pattern string `json:"pattern"`
	// Source says which input carried this pattern: "flag" for a --pattern,
	// "recipe" for an operation read out of the recipe file.
	Source  string   `json:"source"`
	Scope   string   `json:"scope,omitempty"`
	Pass    bool     `json:"pass"`
	Details []string `json:"details,omitempty"` // failure details
}

// ScrubVerifyResult is the top-level JSON output for `scrub verify`.
type ScrubVerifyResult struct {
	Version  int                        `json:"version"`
	Patterns int                        `json:"patterns"`
	Passed   int                        `json:"passed"`
	Failed   int                        `json:"failed"`
	Results  []ScrubVerifyPatternResult `json:"results"`
}

// scrubVerifyPayloadSchema declares what `scrub verify` puts in the envelope's
// payload. scope and details are omitempty on the per-pattern record (an
// unscoped pattern has no scope; a passing one has no details), so they are
// declared without being required.
var scrubVerifyPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"version":  strictcli.SchemaType("integer"),
		"patterns": strictcli.SchemaType("integer"),
		"passed":   strictcli.SchemaType("integer"),
		"failed":   strictcli.SchemaType("integer"),
		"results": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"pattern": strictcli.SchemaType("string"),
				"source":  strictcli.SchemaEnum(scrubVerifySourceFlag, scrubVerifySourceRecipe),
				"scope":   strictcli.SchemaType("string"),
				"pass":    strictcli.SchemaType("boolean"),
				"details": strictcli.SchemaArray(strictcli.SchemaType("string")),
			},
			[]string{"pattern", "source", "pass"},
			false,
		)),
	},
	[]string{"version", "patterns", "passed", "failed", "results"},
	false,
)

// verifyTarget is one pattern the command was asked to check, with the scope
// that pattern is checked under. It is the only thing verification consumes:
// the command holds no state of its own between runs and reads no file safegit
// wrote, so what is verified is exactly what the invocation named.
type verifyTarget struct {
	pattern  string
	source   string
	scope    string
	compiled *regexp.Regexp
}

// collectVerifyTargets turns the command's inputs into the patterns to check.
//
// Both inputs may be given at once -- the input selection is at-least-one, not
// exactly-one -- and the two are simply concatenated in the order the command
// line states them: every --pattern first, then every recipe operation.
//
// The recipe is the EXISTING `scrub run` recipe format, read unchanged.
// `replace`, `mangle` and `depends_on` describe how a rewrite substitutes text
// and in what order, which verification never does, so they are read (the
// parser still validates them) and then ignored. `scope` is the one field
// verification does consume, because it says which paths a match at all counts
// against.
func collectVerifyTargets(patterns []string, scope string, recipePath string) []verifyTarget {
	var targets []verifyTarget

	for _, p := range patterns {
		compiled, err := regexp.Compile(p)
		if err != nil {
			die(exitcode.Usage, fmt.Sprintf("invalid --pattern %q: %v", p, err))
		}
		targets = append(targets, verifyTarget{
			pattern:  p,
			source:   scrubVerifySourceFlag,
			scope:    scope,
			compiled: compiled,
		})
	}

	if recipePath != "" {
		recipe, err := parseRecipe(recipePath)
		if err != nil {
			die(exitcode.Usage, fmt.Sprintf("reading recipe %q: %v", recipePath, err))
		}
		for i, op := range recipe.Operations {
			t := verifyTarget{
				pattern:  op.Pattern,
				source:   scrubVerifySourceRecipe,
				compiled: recipe.Patterns[i],
			}
			if op.Scope != nil {
				t.scope = *op.Scope
			}
			targets = append(targets, t)
		}
	}

	return targets
}

func runScrubVerify(flags globalFlags, kwargs map[string]interface{}) int {
	patterns := kwargsStrSlice(kwargs["pattern"])

	var scope string
	if v := kwargs["scope"]; v != nil {
		scope = v.(string)
		if _, err := path.Match(scope, ""); err != nil {
			die(exitcode.Usage, fmt.Sprintf("invalid --scope glob: %v", err))
		}
	}

	var recipePath string
	if v := kwargs["recipe"]; v != nil {
		recipePath = v.(string)
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}

	ctx := flags.ctx()

	// The input selection is declared (at-least-one over --pattern and the
	// recipe positional), so an invocation naming neither never reaches here.
	// A recipe that parses to no operations does, and it is the same error: a
	// verification with nothing to verify must never read as a clean bill of
	// health.
	targets := collectVerifyTargets(patterns, scope, recipePath)
	if len(targets) == 0 {
		die(exitcode.Usage, "nothing to verify: no patterns were given and the recipe declared no operations")
	}

	compiled := make([]*regexp.Regexp, len(targets))
	for i, t := range targets {
		compiled[i] = t.compiled
	}

	// One pass over the whole object store for every pattern at once. The
	// question verify answers is "is this content anywhere in this repository",
	// which is why the scan is deliberately not restricted to a commit set: an
	// unreachable object still holds the secret.
	allScanResults, err := scan.ScanObjectsMulti(ctx, compiled, scan.ScanOpts{EntireHistory: true})
	if err != nil {
		die(exitcode.General, fmt.Sprintf("scanning objects: %v", err))
	}

	// Attribution (blob SHA -> path) is only needed when some scoped pattern
	// actually matched something, so it is computed once, over the merged match
	// set, and only then.
	needsAttribution := false
	for i, t := range targets {
		if t.scope != "" && len(allScanResults[i].Matches) > 0 {
			needsAttribution = true
			break
		}
	}
	if needsAttribution {
		type matchRange struct{ start, end int }
		var combined scan.ScanResults
		ranges := make([]matchRange, len(targets))
		for i := range targets {
			start := len(combined.Matches)
			combined.Matches = append(combined.Matches, allScanResults[i].Matches...)
			ranges[i] = matchRange{start: start, end: len(combined.Matches)}
		}
		if err := scan.AddAttribution(ctx, &combined, scan.ScanOpts{}); err != nil {
			die(exitcode.General, fmt.Sprintf("adding attribution: %v", err))
		}
		for i := range targets {
			r := ranges[i]
			allScanResults[i].Matches = combined.Matches[r.start:r.end]
		}
	}

	// Scoped blob sets are built at most once per distinct scope.
	scopedBlobSets := make(map[string]map[string]bool)

	results := make([]ScrubVerifyPatternResult, 0, len(targets))
	passed := 0
	failed := 0

	for i, t := range targets {
		matches := allScanResults[i].Matches

		if t.scope != "" && len(matches) > 0 {
			scopedBlobs, ok := scopedBlobSets[t.scope]
			if !ok {
				scopedBlobs, err = buildScopedBlobSet(ctx, t.scope)
				if err != nil {
					die(exitcode.General, fmt.Sprintf("building scoped blob set for %q: %v", t.scope, err))
				}
				scopedBlobSets[t.scope] = scopedBlobs
			}
			var inScope []scan.Match
			for _, m := range matches {
				switch m.ObjectType {
				case "blob":
					if matchScope(t.scope, m.Path) || scopedBlobs[m.SHA] {
						inScope = append(inScope, m)
					}
				default:
					// Commit messages and tag annotations carry no path, so a
					// scope cannot exclude them: they are always checked.
					inScope = append(inScope, m)
				}
			}
			matches = inScope
		}

		record := ScrubVerifyPatternResult{
			Pattern: t.pattern,
			Source:  t.source,
			Scope:   t.scope,
		}
		if len(matches) == 0 {
			record.Pass = true
			results = append(results, record)
			passed++
			infof(flags, "  PASS [%d] %s\n", i+1, verifyTargetLabel(t))
			continue
		}
		detail := formatMatchFailure(matches)
		record.Details = []string{detail}
		results = append(results, record)
		failed++
		if !flags.silent() {
			fmt.Fprintf(os.Stderr, "  FAIL [%d] %s: %s\n", i+1, verifyTargetLabel(t), detail)
		}
	}

	// One computation, two renderings: the counts below are the same three
	// numbers the summary line prints.
	flags.payload(ScrubVerifyResult{
		Version:  1,
		Patterns: len(targets),
		Passed:   passed,
		Failed:   failed,
		Results:  results,
	})
	infof(flags, "\n%d pattern(s) checked: %d passed, %d failed\n", len(targets), passed, failed)

	if failed > 0 {
		return exitcode.General
	}
	return 0
}

// verifyTargetLabel spells one target for the human line.
func verifyTargetLabel(t verifyTarget) string {
	if t.scope == "" {
		return fmt.Sprintf("pattern=%q", t.pattern)
	}
	return fmt.Sprintf("pattern=%q scope=%q", t.pattern, t.scope)
}

// formatMatchFailure formats scan matches into an error string matching the
// format produced by verifySecretRemoved/verifySecretRemovedScoped.
func formatMatchFailure(matches []scan.Match) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("secret still present in %d object(s):\n", len(matches)))
	for _, m := range matches {
		reachable := "unreachable"
		if m.Reachable {
			reachable = "reachable"
		}
		sb.WriteString(fmt.Sprintf("  %s %s (%s, line %d): %s\n",
			m.ObjectType, shortSHA(m.SHA), reachable, m.Line, m.Context))
	}
	return sb.String()
}
