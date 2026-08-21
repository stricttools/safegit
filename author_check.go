package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/strictcli/go/strictcli"
)

// authorCheckDeviation records a commit whose author or committer
// doesn't match the expected identity.
type authorCheckDeviation struct {
	SHA            string `json:"sha"`
	AuthorName     string `json:"author_name"`
	AuthorEmail    string `json:"author_email"`
	CommitterName  string `json:"committer_name"`
	CommitterEmail string `json:"committer_email"`
}

// authorCheckResult is the JSON envelope for `author check`.
type authorCheckResult struct {
	Expected   authorCheckExpected    `json:"expected"`
	Deviations []authorCheckDeviation `json:"deviations"`
}

type authorCheckExpected struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// authorCheckPayloadSchema declares what `author check` puts in the envelope's
// payload. The framework validates the value against it at emission, so the
// declaration and the struct above cannot drift: name and email are omitempty
// (an unset expectation is absent, not empty), which is why they are declared
// but not required.
var authorCheckPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"expected": strictcli.SchemaObject(
			map[string]interface{}{
				"name":  strictcli.SchemaType("string"),
				"email": strictcli.SchemaType("string"),
			},
			nil, false,
		),
		"deviations": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"sha":             strictcli.SchemaType("string"),
				"author_name":     strictcli.SchemaType("string"),
				"author_email":    strictcli.SchemaType("string"),
				"committer_name":  strictcli.SchemaType("string"),
				"committer_email": strictcli.SchemaType("string"),
			},
			[]string{"sha", "author_name", "author_email", "committer_name", "committer_email"},
			false,
		)),
	},
	[]string{"expected", "deviations"},
	false,
)

func runAuthorCheck(flags globalFlags, kwargs map[string]interface{}) int {
	var expectName, expectEmail string
	if v := kwargs["name"]; v != nil {
		expectName = v.(string)
	}
	if v := kwargs["email"]; v != nil {
		expectEmail = v.(string)
	}

	if expectName == "" && expectEmail == "" {
		fmt.Fprintf(os.Stderr, "error: at least one of --name or --email is required\n")
		fmt.Fprintf(os.Stderr, "Run 'safegit author check --help' for usage.\n")
		return exitcode.Usage
	}

	ctx := flags.ctx()

	// Get all commits with SHA + author/committer identity in one pass.
	// Format: sha\x01author_name\x01author_email\x01committer_name\x01committer_email
	out, _, err := git.Run(ctx, "log", "--all", "--format=%H\x01%an\x01%ae\x01%cn\x01%ce")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("reading git log: %v", err))
	}

	lines := git.SplitNonEmpty(out)

	var deviations []authorCheckDeviation
	for _, line := range lines {
		parts := strings.SplitN(line, "\x01", 5)
		if len(parts) != 5 {
			continue
		}
		sha := parts[0]
		authorName, authorEmail := parts[1], parts[2]
		committerName, committerEmail := parts[3], parts[4]

		deviated := false
		if expectName != "" {
			if authorName != expectName || committerName != expectName {
				deviated = true
			}
		}
		if expectEmail != "" {
			if authorEmail != expectEmail || committerEmail != expectEmail {
				deviated = true
			}
		}

		if deviated {
			deviations = append(deviations, authorCheckDeviation{
				SHA:            sha,
				AuthorName:     authorName,
				AuthorEmail:    authorEmail,
				CommitterName:  committerName,
				CommitterEmail: committerEmail,
			})
		}
	}

	// One computation, two renderings: the payload is built unconditionally and
	// the human text below reports the same deviations list.
	result := authorCheckResult{
		Expected: authorCheckExpected{
			Name:  expectName,
			Email: expectEmail,
		},
		Deviations: deviations,
	}
	if result.Deviations == nil {
		result.Deviations = []authorCheckDeviation{}
	}
	flags.payload(result)

	// In machine mode the envelope is stdout's only document, so the human
	// rendering below is skipped -- it prints straight to stdout and would land
	// beside the envelope.
	if flags.json {
		if len(deviations) > 0 {
			return exitcode.General
		}
		return 0
	}

	if len(deviations) == 0 {
		infof(flags, "All commits match expected identity.\n")
		return 0
	}

	fmt.Printf("Found %d commits with deviating identity:\n\n", len(deviations))
	for _, d := range deviations {
		fmt.Printf("  %s  author: %s <%s>  committer: %s <%s>\n",
			d.SHA[:12], d.AuthorName, d.AuthorEmail, d.CommitterName, d.CommitterEmail)
	}

	// Suggest a rewrite command.
	fmt.Printf("\nSuggested fix:\n")
	var rewriteArgs []string
	if expectName != "" {
		// Find the most common deviating name to suggest as --old-name.
		nameFreq := make(map[string]int)
		for _, d := range deviations {
			if d.AuthorName != expectName {
				nameFreq[d.AuthorName]++
			}
			if d.CommitterName != expectName {
				nameFreq[d.CommitterName]++
			}
		}
		if oldName := mostFrequent(nameFreq); oldName != "" {
			rewriteArgs = append(rewriteArgs, fmt.Sprintf("--old-name %q --new-name %q", oldName, expectName))
		}
	}
	if expectEmail != "" {
		emailFreq := make(map[string]int)
		for _, d := range deviations {
			if d.AuthorEmail != expectEmail {
				emailFreq[d.AuthorEmail]++
			}
			if d.CommitterEmail != expectEmail {
				emailFreq[d.CommitterEmail]++
			}
		}
		if oldEmail := mostFrequent(emailFreq); oldEmail != "" {
			rewriteArgs = append(rewriteArgs, fmt.Sprintf("--old-email %q --new-email %q", oldEmail, expectEmail))
		}
	}
	if len(rewriteArgs) > 0 {
		fmt.Printf("  safegit author rewrite %s\n", strings.Join(rewriteArgs, " "))
	}

	return exitcode.General
}

// mostFrequent returns the key with the highest count in the frequency map,
// or empty string if the map is empty.
func mostFrequent(freq map[string]int) string {
	var best string
	var bestCount int
	for k, v := range freq {
		if v > bestCount {
			best = k
			bestCount = v
		}
	}
	return best
}
