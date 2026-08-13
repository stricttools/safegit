package main

import (
	"context"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/strictcli/go/strictcli"
)

// The preview side of the history rewrites, shared by `scrub file`,
// `scrub match`, `scrub run` and `author rewrite`.
//
// A rewrite's dry run used to be hand-rolled: it printed a human summary,
// separately built a per-mode JSON struct out of different numbers, and minted
// no effects at all -- so the framework's would-do log rendered its header over
// an empty body, which reads as "this would change nothing" about the most
// destructive operation the tool has. The rewrite is now minted through the
// effects handle, so the framework renders the log (human mode) and carries the
// same records in the envelope's preview member (machine mode), and each
// command computes ONE result struct that both renderings read.

// rewrittenPlaceholder stands where a rewritten SHA will be. A preview cannot
// know it -- computing it would mean performing the rewrite -- so the log says
// which ref moves and to what kind of thing, and never invents a hash.
const rewrittenPlaceholder = "<rewritten>"

// recordHistoryRewrite mints a history rewrite into the framework's would-do
// log. It is dry-mode-only, exactly like recordCommitRefUpdate: on the execute
// path these commands run through internal/git and the shared post-rewrite
// pipeline, which predate the effects regime and own their own ordering.
//
// The four effects are the rewrite's user-visible mutations, in the order the
// execute path performs them: the ref moves onto the rewritten history, then
// cleanupAfterRewrite expires the reflog, repacks and prunes so the pre-rewrite
// objects (which hold whatever was scrubbed) stop being reachable.
func recordHistoryRewrite(ctx context.Context, flags globalFlags, oldHeadSHA string) {
	if !flags.dryRun {
		return
	}
	ref, err := git.HeadRef(ctx)
	if err != nil || ref == "" {
		ref = "HEAD"
	}
	if oldHeadSHA == "" {
		oldHeadSHA = rewrittenPlaceholder
	}
	e := flags.effects()
	_, _ = e.Run(
		[]interface{}{"git", "update-ref", ref, rewrittenPlaceholder, oldHeadSHA},
		strictcli.Resource("ref:"+ref),
	)
	_, _ = e.Run(
		[]interface{}{"git", "reflog", "expire", "--expire=now", "--all"},
		strictcli.Resource("reflog"),
	)
	_, _ = e.Run(
		[]interface{}{"git", "repack", "-a", "-d", "--unpack-unreachable=now"},
		strictcli.Resource("object-store"),
	)
	_, _ = e.Run(
		[]interface{}{"git", "prune", "--expire=now"},
		strictcli.Resource("object-store"),
	)
}

// scrubRewritesSchema is the declared shape of an old-SHA-to-new-SHA map: a
// dynamic-key object, which the closed subset expresses as a schema-valued
// additionalProperties.
var scrubRewritesSchema = strictcli.SchemaObject(
	nil, nil, strictcli.SchemaType("string"),
)

// scrubTagsSchema is the declared shape of the tag rewrite records (see
// TagRewrite in walker.go).
var scrubTagsSchema = strictcli.SchemaArray(strictcli.SchemaObject(
	map[string]interface{}{
		"refname":   strictcli.SchemaType("string"),
		"old_sha":   strictcli.SchemaType("string"),
		"new_sha":   strictcli.SchemaType("string"),
		"annotated": strictcli.SchemaType("boolean"),
	},
	[]string{"refname", "old_sha", "new_sha", "annotated"},
	false,
))

// intPtr and boolPtr mark the execute-only members of the result structs: a
// preview omits them, an execution states them, and neither publishes a zero
// that a reader would take for a measurement.
func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }
