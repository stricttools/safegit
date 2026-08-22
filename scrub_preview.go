package main

import (
	"context"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
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
// log. It is dry-mode-only -- it returns immediately on the execute path, where
// these commands run through internal/git and the shared post-rewrite pipeline,
// which predate the effects regime and own their own ordering.
//
// That makes it the exception rather than the pattern: the commit family's ref
// update is minted through the handle in BOTH modes, one call site that
// performs the update in an executing run and records it in a preview. A
// dry-mode-only recorder like this one is a description of what the execute
// path would do, written beside it rather than by it, so the two can drift.
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
	record := func(resource string, args ...string) {
		argv, err := gitexec.ArgvAny(gitexec.ExemptHistoryRewriteRecord, args...)
		if err != nil {
			return
		}
		_, _ = e.Run(argv, strictcli.Resource(resource))
	}
	record("ref:"+ref, "update-ref", ref, rewrittenPlaceholder, oldHeadSHA)
	record("reflog", "reflog", "expire", "--expire=now", "--all")
	record("object-store", "repack", "-a", "-d", "--unpack-unreachable=now")
	record("object-store", "prune", "--expire=now")
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
