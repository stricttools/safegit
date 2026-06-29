package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
)

// ScrubVerifyPolicyResult is the per-policy result for JSON output.
type ScrubVerifyPolicyResult struct {
	Pattern string   `json:"pattern"`
	Scope   string   `json:"scope,omitempty"`
	Reason  string   `json:"reason"`
	Pass    bool     `json:"pass"`
	Details []string `json:"details,omitempty"` // failure details
}

// ScrubVerifyResult is the top-level JSON output for `scrub verify`.
type ScrubVerifyResult struct {
	Version  int                       `json:"version"`
	Policies int                       `json:"policies"`
	Passed   int                       `json:"passed"`
	Failed   int                       `json:"failed"`
	Results  []ScrubVerifyPolicyResult `json:"results"`
}

func runScrubVerify(flags globalFlags) int {
	const cmd = "scrub verify"

	gitDir := mustGitDir(flags, cmd)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)

	policies, err := readScrubPolicies(sgDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("reading scrub policies: %v", err))
	}

	if len(policies) == 0 {
		if flags.json {
			emitJSON(ScrubVerifyResult{
				Version:  1,
				Policies: 0,
				Passed:   0,
				Failed:   0,
				Results:  []ScrubVerifyPolicyResult{},
			})
		} else {
			infof(flags, "No scrub policies found.\n")
		}
		return 0
	}

	ctx := context.Background()

	var results []ScrubVerifyPolicyResult
	passed := 0
	failed := 0

	// Phase 1: Compile all policy patterns upfront and identify valid "match"
	// policies. Invalid patterns are recorded as failures immediately.
	type validPolicy struct {
		index    int              // index into original policies slice (for display as 1-based)
		policy   ScrubPolicy
		compiled *regexp.Regexp
	}
	var valid []validPolicy
	for i, policy := range policies {
		if policy.Type != "match" {
			continue
		}
		compiled, err := regexp.Compile(policy.Pattern)
		if err != nil {
			detail := fmt.Sprintf("invalid pattern %q: %v", policy.Pattern, err)
			results = append(results, ScrubVerifyPolicyResult{
				Pattern: policy.Pattern,
				Scope:   policy.Scope,
				Reason:  policy.Reason,
				Pass:    false,
				Details: []string{detail},
			})
			failed++
			if !flags.quiet {
				fmt.Fprintf(os.Stderr, "  FAIL [%d] %s: %s\n", i+1, policy.Pattern, detail)
			}
			continue
		}
		valid = append(valid, validPolicy{index: i, policy: policy, compiled: compiled})
	}

	// Phase 2: Single scan pass over all git objects for all valid patterns.
	if len(valid) > 0 {
		patterns := make([]*regexp.Regexp, len(valid))
		for i, vp := range valid {
			patterns[i] = vp.compiled
		}

		allScanResults, err := scan.ScanObjectsMulti(ctx, patterns, scan.ScanOpts{EntireHistory: true})
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
		}

		// Phase 3: For scoped policies, we need attribution. Run AddAttribution
		// once on a combined result set, then build scoped blob sets per unique scope.
		//
		// Determine which scan results need attribution (those with scoped policies).
		needsAttribution := false
		for i, vp := range valid {
			if vp.policy.Scope != "" && len(allScanResults[i].Matches) > 0 {
				needsAttribution = true
				break
			}
		}

		if needsAttribution {
			// Merge all matches into a single ScanResults for one AddAttribution call,
			// then distribute the attributed matches back.
			//
			// Track match offsets so we can distribute back.
			type matchRange struct {
				start int
				end   int
			}
			var combined scan.ScanResults
			ranges := make([]matchRange, len(valid))
			for i := range valid {
				start := len(combined.Matches)
				combined.Matches = append(combined.Matches, allScanResults[i].Matches...)
				ranges[i] = matchRange{start: start, end: len(combined.Matches)}
			}

			if err := scan.AddAttribution(ctx, &combined, scan.ScanOpts{}); err != nil {
				die(flags, cmd, 1, fmt.Sprintf("adding attribution: %v", err))
			}

			// Distribute attributed matches back to per-pattern results.
			for i := range valid {
				r := ranges[i]
				allScanResults[i].Matches = combined.Matches[r.start:r.end]
			}
		}

		// Build scoped blob sets per unique scope (cache to avoid duplicate work).
		scopedBlobSets := make(map[string]map[string]bool)

		// Phase 4: Aggregate results per policy.
		for i, vp := range valid {
			scanResult := allScanResults[i]
			displayIdx := vp.index + 1

			if vp.policy.Scope == "" {
				// Unscoped: any match is a failure.
				if len(scanResult.Matches) == 0 {
					results = append(results, ScrubVerifyPolicyResult{
						Pattern: vp.policy.Pattern,
						Reason:  vp.policy.Reason,
						Pass:    true,
					})
					passed++
					infof(flags, "  PASS [%d] pattern=%q\n", displayIdx, vp.policy.Pattern)
				} else {
					detail := formatMatchFailure(scanResult.Matches)
					results = append(results, ScrubVerifyPolicyResult{
						Pattern: vp.policy.Pattern,
						Reason:  vp.policy.Reason,
						Pass:    false,
						Details: []string{detail},
					})
					failed++
					if !flags.quiet {
						fmt.Fprintf(os.Stderr, "  FAIL [%d] pattern=%q: %s\n", displayIdx, vp.policy.Pattern, detail)
					}
				}
			} else {
				// Scoped: only in-scope blob matches and all non-blob matches are failures.
				if len(scanResult.Matches) == 0 {
					results = append(results, ScrubVerifyPolicyResult{
						Pattern: vp.policy.Pattern,
						Scope:   vp.policy.Scope,
						Reason:  vp.policy.Reason,
						Pass:    true,
					})
					passed++
					infof(flags, "  PASS [%d] pattern=%q scope=%q\n", displayIdx, vp.policy.Pattern, vp.policy.Scope)
					continue
				}

				// Get or build scoped blob set for this scope.
				scopedBlobs, ok := scopedBlobSets[vp.policy.Scope]
				if !ok {
					scopedBlobs, err = buildScopedBlobSet(ctx, vp.policy.Scope)
					if err != nil {
						die(flags, cmd, 1, fmt.Sprintf("building scoped blob set for %q: %v", vp.policy.Scope, err))
					}
					scopedBlobSets[vp.policy.Scope] = scopedBlobs
				}

				var failures []scan.Match
				for _, m := range scanResult.Matches {
					switch m.ObjectType {
					case "blob":
						if matchScope(vp.policy.Scope, m.Path) || scopedBlobs[m.SHA] {
							failures = append(failures, m)
						}
					default:
						// Commit messages and tags are always checked.
						failures = append(failures, m)
					}
				}

				if len(failures) == 0 {
					results = append(results, ScrubVerifyPolicyResult{
						Pattern: vp.policy.Pattern,
						Scope:   vp.policy.Scope,
						Reason:  vp.policy.Reason,
						Pass:    true,
					})
					passed++
					infof(flags, "  PASS [%d] pattern=%q scope=%q\n", displayIdx, vp.policy.Pattern, vp.policy.Scope)
				} else {
					detail := formatMatchFailure(failures)
					results = append(results, ScrubVerifyPolicyResult{
						Pattern: vp.policy.Pattern,
						Scope:   vp.policy.Scope,
						Reason:  vp.policy.Reason,
						Pass:    false,
						Details: []string{detail},
					})
					failed++
					if !flags.quiet {
						fmt.Fprintf(os.Stderr, "  FAIL [%d] pattern=%q scope=%q: %s\n",
							displayIdx, vp.policy.Pattern, vp.policy.Scope, detail)
					}
				}
			}
		}
	}

	if flags.json {
		emitJSON(ScrubVerifyResult{
			Version:  1,
			Policies: len(policies),
			Passed:   passed,
			Failed:   failed,
			Results:  results,
		})
	} else {
		infof(flags, "\n%d policies checked: %d passed, %d failed\n", len(policies), passed, failed)
	}

	if failed > 0 {
		return 1
	}
	return 0
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

