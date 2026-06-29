package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// identityKey uniquely identifies an author/committer by name and email.
type identityKey struct {
	Name  string
	Email string
}

// identityEntry holds aggregated info about one (name, email) identity.
type identityEntry struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Count int    `json:"count"`
}

func runAuthorList(flags globalFlags) int {
	const cmd = "author list"
	ctx := context.Background()

	// Get all author and committer identities in one pass.
	// Format: author_name\x01author_email\x01committer_name\x01committer_email
	out, _, err := git.Run(ctx, "log", "--all", "--format=%an\x01%ae\x01%cn\x01%ce")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("reading git log: %v", err))
	}

	lines := git.SplitNonEmpty(out)

	// Aggregate into distinct (name, email) tuples with counts and roles.
	type counts struct {
		authorCount    int
		committerCount int
	}
	seen := make(map[identityKey]*counts)

	for _, line := range lines {
		parts := strings.SplitN(line, "\x01", 4)
		if len(parts) != 4 {
			continue
		}
		authorName, authorEmail := parts[0], parts[1]
		committerName, committerEmail := parts[2], parts[3]

		ak := identityKey{Name: authorName, Email: authorEmail}
		if seen[ak] == nil {
			seen[ak] = &counts{}
		}
		seen[ak].authorCount++

		ck := identityKey{Name: committerName, Email: committerEmail}
		if seen[ck] == nil {
			seen[ck] = &counts{}
		}
		seen[ck].committerCount++
	}

	// Build entries with role and total count.
	entries := make([]identityEntry, 0, len(seen))
	for key, c := range seen {
		var role string
		switch {
		case c.authorCount > 0 && c.committerCount > 0:
			role = "both"
		case c.authorCount > 0:
			role = "author"
		default:
			role = "committer"
		}
		total := c.authorCount + c.committerCount
		entries = append(entries, identityEntry{
			Name:  key.Name,
			Email: key.Email,
			Role:  role,
			Count: total,
		})
	}

	// Sort by count descending, then by name for stability.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Email < entries[j].Email
	})

	if flags.json {
		emitJSON(entries)
		return 0
	}

	// Human-readable table output.
	if len(entries) == 0 {
		infof(flags, "No commits found.\n")
		return 0
	}

	// Calculate column widths.
	nameW, emailW, roleW := len("Name"), len("Email"), len("Role")
	for _, e := range entries {
		if len(e.Name) > nameW {
			nameW = len(e.Name)
		}
		if len(e.Email) > emailW {
			emailW = len(e.Email)
		}
		if len(e.Role) > roleW {
			roleW = len(e.Role)
		}
	}

	fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, emailW, roleW)

	fmt.Printf(fmtStr, "Name", "Email", "Role", "Count")
	fmt.Printf("%s  %s  %s  %s\n",
		strings.Repeat("-", nameW),
		strings.Repeat("-", emailW),
		strings.Repeat("-", roleW),
		strings.Repeat("-", 5))

	for _, e := range entries {
		fmt.Printf(fmtStr, e.Name, e.Email, e.Role, fmt.Sprintf("%d", e.Count))
	}

	return 0
}
