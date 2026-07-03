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

// requireOrchestratedRewrite blocks a destructive history rewrite in
// rlsbl-managed repositories unless it runs under release-tool orchestration.
// History rewrites in release-managed repos must go through the release
// pipeline so changelogs, tags, and remotes stay consistent. Callers invoke
// this only on destructive paths (dry-run/--diff stay usable) and supply the
// command-specific death message.
func requireOrchestratedRewrite(ctx context.Context, flags globalFlags, cmd, message string) {
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
	die(flags, cmd, 1, message)
}

// requireOrchestratedScrub is the scrub-command variant: rlsbl has a
// dedicated scrub flow, so the message points at it directly.
func requireOrchestratedScrub(ctx context.Context, flags globalFlags, cmd string) {
	requireOrchestratedRewrite(ctx, flags, cmd,
		"this repository is managed by rlsbl; history rewrites must run under release orchestration -- run 'rlsbl release scrub' instead of invoking safegit scrub directly (dry-run and --diff previews remain available)")
}

// requireOrchestratedAuthorRewrite is the author-rewrite variant. rlsbl has
// no dedicated author-rewrite flow, so the message points at coordinating
// the rewrite with the release tooling rather than at a specific command.
func requireOrchestratedAuthorRewrite(ctx context.Context, flags globalFlags, cmd string) {
	requireOrchestratedRewrite(ctx, flags, cmd,
		"this repository is managed by rlsbl; history rewrites must be coordinated with the release tooling -- coordinate this author rewrite through rlsbl instead of invoking safegit author rewrite directly (dry-run previews remain available)")
}
