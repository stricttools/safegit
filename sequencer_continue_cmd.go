package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/git"
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
	DryRun       bool `json:"dry_run"`
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

// pickRevertPayload is continuePayload plus the members only the two queueable
// commands have: the preserved author, and the three facts that say whether
// safegit or git wrote the commits.
type pickRevertPayload struct {
	continuePayload
	Author continueAuthor `json:"author"`
	// QueueDelegated is false here by construction: this shape is what the
	// pipeline path reports. The delegated path reports delegatedPayload.
	QueueDelegated bool `json:"queue_delegated"`
	// Head is the branch tip after the conclusion, null under --dry-run. It
	// equals SHA on this path and is the only commit name the delegated path
	// can give, so a consumer reads one member either way.
	Head *string `json:"head"`
	// CommitsCreated is 1 here, 0 under --dry-run, and however many git made on
	// the delegated path.
	CommitsCreated int `json:"commits_created"`
	// StoppedAgain is false here by construction: this shape concludes ONE
	// operation, so there is no queue left to stop. See delegatedPayload.
	StoppedAgain bool `json:"stopped_again"`
}

// delegatedPayload is what a QUEUED cherry-pick or revert conclusion reports.
//
// The pipeline members are ABSENT rather than null: safegit created no commit,
// so there is no sha, no tree, no parent list, no changed-path list, no
// compare-and-swap attempt count and no author it chose. Reporting nulls for
// them would invite a consumer to treat them as answers; leaving them out says
// the operation had none. The schema below therefore requires only the members
// both shapes carry.
type delegatedPayload struct {
	Operation      string               `json:"operation"`
	Ref            string               `json:"ref"`
	QueueDelegated bool                 `json:"queue_delegated"`
	Head           *string              `json:"head"`
	CommitsCreated int                  `json:"commits_created"`
	Resolutions    []continueResolution `json:"resolutions"`
	StateCleared   bool                 `json:"state_cleared"`
	// StoppedAgain says git's own `--continue` ended NONZERO because the queue
	// stopped on a further conflict. It is not a restatement of
	// `state_cleared`, which is read off the git directory and answers a
	// different question: what git left behind. This one answers how the
	// delegation ended, and it is the member that makes a payload emitted
	// alongside a nonzero exit readable -- head and commits_created then
	// describe the commits git DID make before stopping.
	StoppedAgain bool `json:"stopped_again"`
	DryRun       bool `json:"dry_run"`
}

// continuePayloadSchema builds a conclusion's payload schema. The three
// commands declare their own, from this one construction, so a member can never
// be present in one command's document and absent from its schema.
//
// withAuthor selects the two commands that can also DELEGATE (cherry-pick and
// revert): the preserved author and the delegation members ride together
// because they belong to the same pair. merge-continue has neither -- a merge
// preserves no author and can never be queued.
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
		"dry_run":       strictcli.SchemaType("boolean"),
	}
	required := []string{"operation", "ref", "sha", "parents", "tree", "files", "resolutions", "state_cleared", "attempts", "dry_run"}

	if withAuthor {
		members["author"] = strictcli.SchemaObject(
			map[string]interface{}{
				"name":  strictcli.SchemaType("string"),
				"email": strictcli.SchemaType("string"),
			},
			[]string{"name", "email"},
			false,
		)
		members["queue_delegated"] = strictcli.SchemaType("boolean")
		members["head"] = strictcli.SchemaType("string", "null")
		members["commits_created"] = strictcli.SchemaType("integer")
		members["stopped_again"] = strictcli.SchemaType("boolean")

		// The delegated document carries none of the pipeline's members, so
		// they leave the required set for these two commands: the three
		// delegation members plus the ones both shapes carry are what a
		// consumer may always read. `queue_delegated` is the discriminator
		// that says which of the two shapes arrived.
		required = []string{"operation", "ref", "resolutions", "state_cleared", "dry_run",
			"queue_delegated", "head", "commits_created", "stopped_again"}
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
// `safegit revert` reaches the same engine through a different door and calls
// renderHuman alone: `revert` is a passthrough registration with no declared
// payload schema, so there is no machine document for it to emit.
func (op continueOp) report(flags globalFlags, out conclusionResult) {
	op.reportPayload(flags, out)
	op.renderHuman(flags, out, "concluded the "+op.kind.String())
}

// reportPayload builds and supplies the machine document.
func (op continueOp) reportPayload(flags globalFlags, out conclusionResult) {
	base := continuePayload{
		Operation:    op.kind.String(),
		Ref:          out.commit.Ref,
		SHA:          realSHA(flags, out.commit.SHA),
		Parents:      orEmpty(out.commit.Parents),
		Tree:         out.commit.Tree,
		Files:        orEmpty(out.commit.Files),
		Resolutions:  reportedResolutions(out.declared),
		StateCleared: out.cleared,
		Attempts:     out.commit.Attempts,
		DryRun:       flags.dryRun,
	}
	// The shape is chosen by the COMMAND, not by whether an author was
	// resolved: the two queueable commands declare the wider schema, and a
	// document missing its declared members would be refused at emission.
	if !op.queueable() {
		flags.payload(base)
		return
	}
	created := 1
	if flags.dryRun {
		created = 0
	}
	var author continueAuthor
	if out.author != nil {
		author = continueAuthor{Name: out.author.Name, Email: out.author.Email}
	}
	flags.payload(pickRevertPayload{
		continuePayload: base,
		Author:          author,
		QueueDelegated:  false,
		Head:            base.SHA,
		CommitsCreated:  created,
		StoppedAgain:    false,
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
}

// reportDelegated emits the payload and the human rendering of a conclusion
// GIT performed.
//
// What the delegation COST is stated first and unconditionally, because it
// changes who the commits belong to: they carry none of safegit's trailers,
// safegit's own commit-msg handling never ran, and `safegit undo` will not
// reverse them. That line is written to stderr on every run -- see the note
// below -- so an operator learns it whatever mode they asked for.
func reportDelegated(flags globalFlags, op continueOp, out delegatedOutcome) {
	head := out.head
	flags.payload(delegatedPayload{
		Operation:      op.kind.String(),
		Ref:            currentRefName(flags),
		QueueDelegated: true,
		Head:           &head,
		CommitsCreated: out.created,
		Resolutions:    reportedResolutions(out.declared),
		StateCleared:   out.stateCleared,
		StoppedAgain:   out.stoppedAgain,
		DryRun:         false,
	})

	// What the delegation COST, on stderr and unconditionally -- the same class
	// of fact as undo's "the merge state is NOT restored" note, and written the
	// same way for the same reason: an operator who does not hear it will look
	// for safegit's trailers on these commits, or try to reverse them with
	// `safegit undo`. --quiet is a request for less chatter, not for less of
	// this; machine mode's envelope owns stdout, and stderr is where a fact
	// that must survive both belongs.
	if out.created != 0 {
		fmt.Fprintf(os.Stderr, "note: these commits are git's: no safegit trailers, safegit's commit-msg handling did not run, and 'safegit undo' does not reverse them\n")
	}

	if flags.silent() {
		return
	}

	verb := strings.TrimSuffix(op.command, "-continue")
	switch {
	case out.stoppedAgain && out.created == 0:
		fmt.Printf("[%s] git stopped the queued %s without committing anything: the branch has not moved\n",
			shortSHA(out.head), op.kind)
	case out.stoppedAgain:
		fmt.Printf("[%s] git stopped the queued %s again: 'git %s --continue' authored the commit(s) it made first\n",
			shortSHA(out.head), op.kind, verb)
	default:
		fmt.Printf("[%s] git concluded the queued %s: 'git %s --continue' authored the commits\n",
			shortSHA(out.head), op.kind, verb)
	}
	fmt.Printf(" %s, %d declared resolution(s) staged into safegit's index copy\n",
		commitCountText(out.created), len(out.declared))
	if out.stateCleared {
		fmt.Printf(" the queue is finished and git removed its own state files\n")
	} else {
		fmt.Printf(" the queue is NOT finished: git stopped again and its state files are still in place\n")
	}

	written, removed := worktreeEffects(out.declared)
	if len(written) > 0 {
		fmt.Printf(" %d working-tree file(s) written with the resolved content before the delegation: %s\n", len(written), joinPaths(written))
	}
	if len(removed) > 0 {
		fmt.Printf(" %d working-tree file(s) deleted before the delegation: %s\n", len(removed), joinPaths(removed))
	}
}

// commitCountText renders the commit count, including the one case where it
// could not be taken -- which is stated rather than rounded to a number.
func commitCountText(created int) string {
	if created < 0 {
		return "the number of commits created could not be counted"
	}
	return fmt.Sprintf("%d commit(s) created", created)
}

// currentRefName resolves the branch the conclusion committed onto, for the
// payload's ref member. The delegated path never had a CommitResult to read it
// off, and an unreadable ref is reported empty rather than guessed.
func currentRefName(flags globalFlags) string {
	ref, err := git.HeadRef(flags.ctx())
	if err != nil {
		return ""
	}
	return ref
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
