package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// refMove is one ref update a rewrite implies: move Refname from OldSHA to
// NewSHA, compare-and-swap on the old value.
type refMove struct {
	Refname string
	OldSHA  string
	NewSHA  string
}

// RefUpdatePlan is everything a rewrite will do to refs, computed before any of
// it happens. Splitting the plan from its application is what lets Tier A
// verification run on the finished object graph while the repository still
// points at the old one: every object the plan needs exists once the plan is
// built, and not one ref has moved.
type RefUpdatePlan struct {
	// Moves are the ref updates, in the order they will be applied.
	Moves []refMove

	// TagRewrites records tag objects rewritten because their target commit
	// moved or their tagger identity matched.
	TagRewrites []TagRewrite

	// AnnotationTagRewrites records tag objects rewritten because the
	// annotation transform changed their body. Their OldSHA is the tag object
	// as the retargeting step left it, so the two lists chain.
	AnnotationTagRewrites []TagRewrite

	// TagsRewritten counts the annotation-body rewrites.
	TagsRewritten int

	// NewTips are the objects refs will point at once the plan is applied:
	// every branch and tag head of the rewritten history, plus a detached
	// HEAD's new commit. Scanning them scans the whole new history, including
	// the parts no ref reaches yet.
	NewTips []string
}

// planRefUpdates computes every ref update the SHA map implies and writes the
// tag objects those updates need, without moving a single ref.
//
// An annotated tag can be rewritten twice over: once because its target commit
// moved or its tagger matched an identity rewrite, and again because the
// annotation transform changed its body. Both writes happen here, chained, and
// the ref moves once, from the original object to the final one.
func planRefUpdates(ctx context.Context, shaMap map[string]string, oldName, newName, oldEmail, newEmail string, annotate TagBodyTransformFunc, verbose bool) (*RefUpdatePlan, error) {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/heads/", "refs/tags/", "refs/remotes/")
	if err != nil {
		return nil, fmt.Errorf("listing refs: %w", err)
	}

	plan := &RefUpdatePlan{}
	for _, line := range git.SplitNonEmpty(out) {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		refname, objecttype, objectname := parts[0], parts[1], parts[2]

		// Skip stash refs.
		if strings.HasPrefix(refname, "refs/stash") {
			continue
		}

		// Skip symbolic refs like refs/remotes/origin/HEAD -- updating them
		// with git update-ref would convert them to regular refs.
		if strings.HasPrefix(refname, "refs/remotes/") && strings.HasSuffix(refname, "/HEAD") {
			continue
		}

		switch objecttype {
		case "commit":
			// Branch or lightweight tag pointing directly at a commit.
			newSHA, ok := shaMap[objectname]
			if !ok || newSHA == objectname {
				plan.NewTips = append(plan.NewTips, objectname)
				continue
			}
			plan.Moves = append(plan.Moves, refMove{Refname: refname, OldSHA: objectname, NewSHA: newSHA})
			plan.NewTips = append(plan.NewTips, newSHA)
			if strings.HasPrefix(refname, "refs/tags/") {
				plan.TagRewrites = append(plan.TagRewrites, TagRewrite{Refname: refname, OldSHA: objectname, NewSHA: newSHA, Annotated: false})
			}

		case "tag":
			// Step 1: retarget the tag object at the rewritten commit, and
			// apply an identity rewrite to its tagger line.
			retargeted, err := rewriteAnnotatedTag(ctx, objectname, shaMap, oldName, newName, oldEmail, newEmail)
			if err != nil {
				return nil, fmt.Errorf("rewriting annotated tag %s: %w", refname, err)
			}
			if retargeted != objectname {
				plan.TagRewrites = append(plan.TagRewrites, TagRewrite{Refname: refname, OldSHA: objectname, NewSHA: retargeted, Annotated: true})
			}

			// Step 2: rewrite the annotation body.
			final := retargeted
			if annotate != nil {
				annotated, err := rewriteTagBody(ctx, refname, retargeted, annotate)
				if err != nil {
					return nil, err
				}
				if annotated != retargeted {
					plan.AnnotationTagRewrites = append(plan.AnnotationTagRewrites, TagRewrite{Refname: refname, OldSHA: retargeted, NewSHA: annotated, Annotated: true})
					plan.TagsRewritten++
					if verbose {
						fmt.Fprintf(os.Stderr, "  tag annotation %s: %s -> %s\n", refname, shortSHA(retargeted), shortSHA(annotated))
					}
					final = annotated
				}
			}

			plan.NewTips = append(plan.NewTips, final)
			if final != objectname {
				plan.Moves = append(plan.Moves, refMove{Refname: refname, OldSHA: objectname, NewSHA: final})
			}
		}
	}

	// Detached HEAD: no branch carries it, so it needs its own move.
	if _, _, symErr := git.Run(ctx, "symbolic-ref", "HEAD"); symErr != nil {
		headSHA, err := git.RevParse(ctx, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("reading detached HEAD: %w", err)
		}
		if newSHA, ok := shaMap[headSHA]; ok && newSHA != headSHA {
			plan.Moves = append(plan.Moves, refMove{Refname: "HEAD", OldSHA: headSHA, NewSHA: newSHA})
			plan.NewTips = append(plan.NewTips, newSHA)
		}
	}

	return plan, nil
}

// rewriteTagBody applies the annotation transform to one tag object, writing
// the new tag object when the body changed. A tag object with no body has
// nothing to transform and is returned unchanged.
func rewriteTagBody(ctx context.Context, refname, tagObjectSHA string, annotate TagBodyTransformFunc) (string, error) {
	content, _, err := git.Run(ctx, "cat-file", "-p", tagObjectSHA)
	if err != nil {
		return "", fmt.Errorf("reading tag object %s: %w", tagObjectSHA, err)
	}
	headerEnd := strings.Index(content, "\n\n")
	if headerEnd < 0 {
		return tagObjectSHA, nil
	}
	header := content[:headerEnd]
	body := content[headerEnd+2:]

	newBody, err := annotate(refname, header, body)
	if err != nil {
		return "", fmt.Errorf("transforming tag %s: %w", refname, err)
	}
	if newBody == body {
		return tagObjectSHA, nil
	}

	newSHA, err := git.HashObjectWriteTag(ctx, []byte(header+"\n\n"+newBody))
	if err != nil {
		return "", fmt.Errorf("writing rewritten tag annotation for %s: %w", refname, err)
	}
	return newSHA, nil
}

// applyRefUpdates performs the planned ref moves. This is the irreversible step
// of a rewrite: everything refusable has already run.
func applyRefUpdates(ctx context.Context, plan *RefUpdatePlan, verbose bool) error {
	for _, m := range plan.Moves {
		if err := git.UpdateRef(ctx, m.Refname, m.NewSHA, m.OldSHA); err != nil {
			return fmt.Errorf("updating ref %s: %w", m.Refname, err)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "  %-20s %s -> %s\n", m.Refname, shortSHA(m.OldSHA), shortSHA(m.NewSHA))
		}
	}
	return nil
}
