package main

import (
	"context"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/git"
)

// orchestrationEnvVar must be set to exactly "1" by the release tooling when
// it spawns a scrub subprocess. rlsbl sets it on orchestrated scrubs.
const orchestrationEnvVar = "RLSBL_SCRUB_ORCHESTRATED"

// requireOrchestratedScrub blocks destructive scrub operations in
// rlsbl-managed repositories unless the scrub runs under release-tool
// orchestration. History rewrites in release-managed repos must go through
// the release pipeline so changelogs, tags, and remotes stay consistent.
// Callers invoke this only on destructive paths (dry-run/--diff stay usable).
func requireOrchestratedScrub(ctx context.Context, flags globalFlags, cmd string) {
	root, err := git.RepoRoot(ctx)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving repository root: %v", err))
	}
	if !isRlsblManaged(root) {
		return
	}
	if os.Getenv(orchestrationEnvVar) == "1" {
		return
	}
	die(flags, cmd, 1, "this repository is managed by rlsbl; history rewrites must run under release orchestration -- run 'rlsbl release scrub' instead of invoking safegit scrub directly (dry-run and --diff previews remain available)")
}
