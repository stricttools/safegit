package main

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/smm-h/safegit/internal/repo"
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

	for i, policy := range policies {
		if policy.Type != "match" {
			// Only "match" type is supported for now.
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

		var scope *string
		if policy.Scope != "" {
			scope = &policy.Scope
		}

		verifyErr := verifySecretRemovedScoped(ctx, compiled, scope)
		if verifyErr != nil {
			details := []string{verifyErr.Error()}
			results = append(results, ScrubVerifyPolicyResult{
				Pattern: policy.Pattern,
				Scope:   policy.Scope,
				Reason:  policy.Reason,
				Pass:    false,
				Details: details,
			})
			failed++
			if !flags.quiet {
				fmt.Fprintf(os.Stderr, "  FAIL [%d] pattern=%q", i+1, policy.Pattern)
				if policy.Scope != "" {
					fmt.Fprintf(os.Stderr, " scope=%q", policy.Scope)
				}
				fmt.Fprintf(os.Stderr, ": %v\n", verifyErr)
			}
		} else {
			results = append(results, ScrubVerifyPolicyResult{
				Pattern: policy.Pattern,
				Scope:   policy.Scope,
				Reason:  policy.Reason,
				Pass:    true,
			})
			passed++
			infof(flags, "  PASS [%d] pattern=%q", i+1, policy.Pattern)
			if policy.Scope != "" {
				infof(flags, " scope=%q", policy.Scope)
			}
			infof(flags, "\n")
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

