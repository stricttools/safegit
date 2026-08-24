package main

import (
	"fmt"

	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// The three conclusion commands' surface: their handlers, their payloads and
// the human text they print. The engine they share is in
// sequencer_continue.go; what lives here is what each one SAYS, which is where
// they legitimately differ.

// continueResolution is one declared resolution as the payload reports it.
type continueResolution struct {
	Path   string `json:"path"`
	Choice string `json:"choice"`
	// Source is "flag" or "file": both inputs may be combined, so a consumer
	// that wants to reproduce the invocation needs to know which one carried
	// each path.
	Source string `json:"source"`
}

// continuePayload is what a conclusion puts in the envelope's payload.
//
// Nothing here is counted from the arguments. `files` is the changed-path list
// the pipeline read off the objects, and `parents` is the commit's real parent
// list -- which for a merge conclusion is HEAD plus every MERGE_HEAD line, an
// octopus included.
type continuePayload struct {
	// Operation is the git operation that was concluded: "merge",
	// "cherry-pick" or "revert".
	Operation string `json:"operation"`
	Ref       string `json:"ref"`
	// SHA is the commit that was created, and null under --dry-run: the preview
	// builds an object to compute the tree honestly, but no commit exists at
	// that name for anyone to fetch.
	SHA         *string              `json:"sha"`
	Parents     []string             `json:"parents"`
	Tree        string               `json:"tree"`
	Files       []string             `json:"files"`
	Resolutions []continueResolution `json:"resolutions"`
	// StateCleared reports that the operation's whole state-file set was
	// removed, so git no longer considers the repository mid-operation. False
	// under --dry-run, which removes nothing.
	StateCleared bool `json:"state_cleared"`
	Attempts     int  `json:"attempts"`
	// DeclinedChecks are the checks this conclusion did NOT make, never nil. A
	// silent skip reads as a clean verdict over content nothing looked at.
	DeclinedChecks []declinedCheck `json:"declined_checks"`
	DryRun         bool            `json:"dry_run"`
}

// continueAuthor is the identity the concluding commit RECORDS as its author.
//
// Where it comes from differs per operation, which is a fact about the
// operation rather than about the member: a cherry-pick preserves the picked
// commit's author, and a revert records the OPERATOR, because a revert is the
// reverter's own new change (git's own revert semantics). A merge conclusion
// records no author of its own, which is why its schema has no such member
// rather than a null one.
type continueAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// pickRevertPayload is continuePayload plus the one member only a cherry-pick
// or revert conclusion has: the identity the commit records.
//
// It carried two more, `head` and `commits_created`, and both are gone. They
// were QUEUE members: they existed to describe a delegated sequence, where the
// conclusion could make several commits and the branch tip was therefore not
// the commit this document named. A queued sequence is no longer safegit's to
// conclude, so one shape remains -- one conclusion, one commit -- and in that
// shape `head` was a duplicate of `sha` and `commits_created` was a constant.
type pickRevertPayload struct {
	continuePayload
	Author continueAuthor `json:"author"`
}

// continuePayloadSchema builds a conclusion's payload schema. The three
// commands declare their own, from this one construction, so a member can never
// be present in one command's document and absent from its schema.
//
// withAuthor selects the two commands whose commit records an identity of its
// own (cherry-pick and revert). merge-continue has none -- a merge conclusion
// preserves no author -- which is why its schema has no such member rather than
// a null one.
func continuePayloadSchema(withAuthor bool) map[string]interface{} {
	members := map[string]interface{}{
		"operation": strictcli.SchemaType("string"),
		"ref":       strictcli.SchemaType("string"),
		"sha":       strictcli.SchemaType("string", "null"),
		"parents":   strictcli.SchemaArray(strictcli.SchemaType("string")),
		"tree":      strictcli.SchemaType("string"),
		"files":     strictcli.SchemaArray(strictcli.SchemaType("string")),
		"resolutions": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"path":   strictcli.SchemaType("string"),
				"choice": strictcli.SchemaType("string"),
				"source": strictcli.SchemaType("string"),
			},
			[]string{"path", "choice", "source"},
			false,
		)),
		"state_cleared": strictcli.SchemaType("boolean"),
		"attempts":      strictcli.SchemaType("integer"),
		"declined_checks": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"check":  strictcli.SchemaType("string"),
				"path":   strictcli.SchemaType("string"),
				"reason": strictcli.SchemaType("string"),
			},
			[]string{"check", "path", "reason"},
			false,
		)),
		"dry_run": strictcli.SchemaType("boolean"),
	}
	required := []string{"operation", "ref", "sha", "parents", "tree", "files", "resolutions", "state_cleared", "attempts", "declined_checks", "dry_run"}

	if withAuthor {
		members["author"] = strictcli.SchemaObject(
			map[string]interface{}{
				"name":  strictcli.SchemaType("string"),
				"email": strictcli.SchemaType("string"),
			},
			[]string{"name", "email"},
			false,
		)
		required = append(required, "author")
	}
	return strictcli.SchemaObject(members, required, false)
}

var (
	mergeContinuePayloadSchema      = continuePayloadSchema(false)
	cherryPickContinuePayloadSchema = continuePayloadSchema(true)
	revertContinuePayloadSchema     = continuePayloadSchema(true)
)

// report emits the payload and the human rendering of a finished (or
// previewed) conclusion.
//
// It is the whole answer for the three conclusion commands. The restructured
// `safegit merge`, `safegit cherry-pick` and `safegit revert` reach the same
// engine through a different door and call renderHuman alone: each declares a
// payload schema of ITS OWN and supplies the document itself, because the
// members differ: a command that started the operation has no declared
// resolutions to report.
func (op continueOp) report(flags globalFlags, out conclusionResult) {
	op.reportPayload(flags, out)
	op.renderHuman(flags, out, "concluded the "+op.kind.String())
}

// reportPayload builds and supplies the machine document.
func (op continueOp) reportPayload(flags globalFlags, out conclusionResult) {
	base := continuePayload{
		Operation:      op.kind.String(),
		Ref:            out.commit.Ref,
		SHA:            realSHA(flags, out.commit.SHA),
		Parents:        orEmpty(out.commit.Parents),
		Tree:           out.commit.Tree,
		Files:          orEmpty(out.commit.Files),
		Resolutions:    reportedResolutions(out.declared),
		StateCleared:   out.cleared,
		Attempts:       out.commit.Attempts,
		DeclinedChecks: orEmptyDeclines(out.declines),
		DryRun:         flags.dryRun,
	}
	// The shape is chosen by the COMMAND, not by whether an author was
	// resolved: the two commands that record an identity declare the wider
	// schema, and a document missing its declared members would be refused at
	// emission.
	if !op.reportsAuthor() {
		flags.payload(base)
		return
	}
	var author continueAuthor
	if out.author != nil {
		author = continueAuthor{Name: out.author.Name, Email: out.author.Email}
	}
	flags.payload(pickRevertPayload{
		continuePayload: base,
		Author:          author,
	})
}

// renderHuman prints what a conclusion did (or would do), headed by the caller's
// own one-line summary of the operation: the three conclusion commands say they
// concluded something, the restructured revert says it reverted a commit.
func (op continueOp) renderHuman(flags globalFlags, out conclusionResult, headline string) {
	if flags.silent() {
		return
	}

	if flags.dryRun {
		fmt.Println(wouldWriteHeader("conclude the "+op.kind.String(), out.commit.Ref, out.commit.Tree, firstLine(messageSubject(out))))
		fmt.Printf(" %d file(s) would be committed, %d parent(s), %d declared resolution(s)\n",
			len(out.commit.Files), len(out.commit.Parents), len(out.declared))
		fmt.Printf(" the %s state files would then be removed\n", op.kind)
		// Stated because it is a working-tree write the preview is not making:
		// an executing run puts the operator's autostashed work back, and a
		// preview that said nothing about it would be describing a smaller
		// operation than the one it is previewing.
		if out.state.Autostash != "" {
			fmt.Printf(" the autostash %s would then be applied to the working tree and %s removed\n",
				shortSHA(out.state.Autostash), sequencer.FileMergeAutostash)
		}
		if written, removed := worktreeEffects(out.declared); len(written)+len(removed) > 0 {
			if len(written) > 0 {
				fmt.Printf(" %d working-tree file(s) would be overwritten with the resolved content: %s\n", len(written), joinPaths(written))
			}
			if len(removed) > 0 {
				fmt.Printf(" %d working-tree file(s) would be deleted: %s\n", len(removed), joinPaths(removed))
			}
		}
		renderDeclinedChecks(out.declines)
		return
	}

	fmt.Printf("[%s %s] %s\n", refShortName(out.commit.Ref), shortSHA(out.commit.SHA), headline)
	fmt.Printf(" %d file(s) committed, %d parent(s)\n", len(out.commit.Files), len(out.commit.Parents))
	if out.author != nil {
		if op.preservesSourceAuthor() {
			fmt.Printf(" author preserved: %s <%s>\n", out.author.Name, out.author.Email)
		} else {
			fmt.Printf(" author: %s <%s> (a revert is your own change, so it is NOT authored by the commit it undoes)\n",
				out.author.Name, out.author.Email)
		}
	}
	// What the conclusion did to the working tree, stated because it wrote
	// there: `ours` and `theirs` replace the file on disk with the content that
	// was committed (git's own `checkout --ours`), and `delete` removes it (git's
	// own `rm`). A `worktree` resolution needs no line -- the file on disk was
	// the source.
	written, removed := worktreeEffects(out.declared)
	if len(written) > 0 {
		fmt.Printf(" %d working-tree file(s) written with the resolved content: %s\n", len(written), joinPaths(written))
	}
	if len(removed) > 0 {
		fmt.Printf(" %d working-tree file(s) deleted: %s\n", len(removed), joinPaths(removed))
	}
	renderDeclinedChecks(out.declines)
}

// worktreeEffects splits the declared resolutions into the paths whose
// working-tree file the conclusion overwrites and the ones whose file it
// deletes. A `worktree` resolution appears in neither: its file on disk is
// where the committed content came from.
//
// The split is by KEYWORD, not by what the stages hold, so it is the same
// answer before the commit (a preview) and after it (the report). A stage the
// conflict does not have turns an overwrite into a deletion on disk, which the
// listing already says and which this line does not try to predict.
func worktreeEffects(declared []resolution) (written, removed []string) {
	for _, r := range declared {
		switch r.Choice {
		case resolveOurs, resolveTheirs:
			written = append(written, r.Path)
		case resolveDelete:
			removed = append(removed, r.Path)
		}
	}
	return written, removed
}

// messageSubject renders something to head a preview line with. A preview has
// the commit object, so its subject is read off it rather than reconstructed.
func messageSubject(out conclusionResult) string {
	if out.state.Kind == sequencer.KindNone {
		return ""
	}
	return "concluding " + out.state.String()
}

// orEmptyDeclines renders the declined-check list for a payload, never nil: a
// declared member that arrives as null invites a consumer to treat the absence
// of declined checks as a missing answer rather than as an empty one.
func orEmptyDeclines(declines []declinedCheck) []declinedCheck {
	if declines == nil {
		return []declinedCheck{}
	}
	return declines
}

// renderDeclinedChecks prints the checks a conclusion did NOT make.
//
// It is printed on every path that reports a conclusion -- the preview and the
// executed run alike, because the preview ran the same verification and declined
// the same checks. Each line names the check, the path and what declined it, so
// an operator who did not know the exemption was there can find the declaration.
func renderDeclinedChecks(declines []declinedCheck) {
	for _, d := range declines {
		fmt.Printf(" check declined: %s on %s -- %s\n", d.Check, d.Path, d.Reason)
	}
}

// reportedResolutions renders the declared set for the payload, never nil.
func reportedResolutions(declared []resolution) []continueResolution {
	out := make([]continueResolution, 0, len(declared))
	for _, r := range declared {
		out = append(out, continueResolution{Path: r.Path, Choice: string(r.Choice), Source: string(r.Source)})
	}
	return out
}

// joinPaths renders a short path list for a one-line notice.
func joinPaths(paths []string) string {
	const shown = 3
	if len(paths) <= shown {
		return fmt.Sprint(paths)
	}
	return fmt.Sprintf("%v and %d more", paths[:shown], len(paths)-shown)
}

// The flags the three commands share. They are built per command rather than
// shared as one slice value because each command's help text names its own
// operation, and because a flag value is a registration, not a constant.
func continueFlags(op continueOp, theirsHelp string) []strictcli.Flag {
	return []strictcli.Flag{
		strictcli.StringFlag("resolve",
			"resolve one conflicted path, as 'path=ours|theirs|worktree|delete' (repeatable, once per path; the split is on the last '=', so a path containing one stays intact). "+
				"The keywords are defined by INDEX STAGE: ours is the stage-2 blob -- "+op.oursText+"; theirs is the stage-3 blob -- "+theirsHelp+"; "+
				"worktree is the file's current content on disk; delete leaves the path out of the commit. "+
				"ours and theirs also WRITE the chosen content into the working tree (git's own checkout --ours), and delete removes the file from disk (git's own rm). "+
				"Paths are repository-relative. Omitted, every conflicted path is unresolved, which is a refusal listing them",
			strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional(), strictcli.ValidateFn(validateResolveSelection)),
		strictcli.StringFlag("resolve-file",
			"read resolutions from a TOML file of [[resolutions]] tables, each with a path and a choice key; combinable with --resolve, and a path named by both is a hard error; omitted means the --resolve flags are the whole declaration",
			strictcli.Optional()),
		strictcli.StringFlag("m",
			"commit message paragraph, replacing git's own draft; repeating it joins the values with a blank line between them; omitted means git's draft for this operation with its comment block stripped",
			strictcli.Short("m"), strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional()),
		strictcli.StringFlag("trailer",
			"add a key-value trailer line to the commit message (repeatable)",
			strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional()),
	}
}

// registerContinue registers one conclusion command.
//
// theirsHelp is this operation's own answer to "what does theirs resolve to",
// which the help text repeats and the per-path listing prints at the moment the
// operator has to choose.
func registerContinue(app *strictcli.App, op continueOp, schema map[string]interface{}, theirsHelp, help string) {
	app.Command(op.command, help, continueHandler(op),
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithTags("json"),
		strictcli.PayloadSchema(schema),
		strictcli.WithGrants(strictcli.Grant{
			Name:   "parent-bump",
			Reason: "concluding an operation in a submodule moves the parent's gitlink, so safegit commits the parent too when commit.autoBumpParent is on",
			Kind:   strictcli.ProcMutate,
		}),
		strictcli.WithFlags(continueFlags(op, theirsHelp)...),
	)
}

// continueHandler builds one command's handler over the shared engine.
func continueHandler(op continueOp) func(*strictcli.Context, map[string]interface{}) strictcli.Outcome {
	return func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		gf := globalsToFlags(ctx, kwargs)
		return strictcli.Exit(runContinue(gf,
			op,
			kwargsStrSlice(kwargs["m"]),
			kwargsStrSlice(kwargs["trailer"]),
			kwargsStrSlice(kwargs["resolve"]),
			optStr(kwargs["resolve_file"], ""),
		))
	}
}
