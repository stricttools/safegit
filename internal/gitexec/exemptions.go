package gitexec

import "sort"

// The declared directory-pin exemption table.
//
// WithRoot pins every git subprocess safegit constructs to the repository root,
// because safegit's own argv is built from repo-relative and absolute paths and
// must mean the same thing from every working directory. A handful of sites
// cannot take that pin. They are enumerated here, in code, with the reason --
// not described in prose somewhere and then forgotten. Nothing outside this
// table can escape the pin: WithoutRootPin and Spec.Exempt both refuse an
// identifier the table does not declare.

// ExemptionKind says WHY a site is not subject to the repository-root pin.
type ExemptionKind string

const (
	// KindExplicitDir: the site receives a git directory and work tree as its
	// own arguments and targets that repository, so there is no operator
	// working directory left for a pin to correct.
	KindExplicitDir ExemptionKind = "explicit-dir"

	// KindOperatorCwd: the argv is the operator's own, forwarded to git
	// verbatim. git must read it in the directory the operator typed it in;
	// re-rooting it would silently change what the operator's pathspecs mean.
	KindOperatorCwd ExemptionKind = "operator-cwd"

	// KindEffectsHandle: safegit does not construct this subprocess -- the
	// strictcli effects handle does, so that --dry-run records the invocation
	// instead of performing it. The pin cannot be applied to a process safegit
	// does not start, so each such site is directory-independent (an argv naming
	// refs and remotes, never paths) or DECLARES the working directory to the
	// effects handle itself, which is the pin's own value stated at the site.
	KindEffectsHandle ExemptionKind = "effects-handle"
)

// ExemptionID identifies one declared exemption. The value is the code site it
// covers, so a reader of the table can go straight there.
type ExemptionID string

const (
	// ExemptRunWithGitDir covers git.RunWithGitDir.
	ExemptRunWithGitDir ExemptionID = "internal/git.RunWithGitDir"
	// ExemptCatFileBatchAllWithDir covers git.CatFileBatchAllWithDir.
	ExemptCatFileBatchAllWithDir ExemptionID = "internal/git.CatFileBatchAllWithDir"
	// ExemptCatFileBatchSHAsWithDir covers git.CatFileBatchSHAsWithDir.
	ExemptCatFileBatchSHAsWithDir ExemptionID = "internal/git.CatFileBatchSHAsWithDir"
	// ExemptSubmoduleRunGit covers internal/submodule's own git runner.
	ExemptSubmoduleRunGit ExemptionID = "internal/submodule.runGit"
	// ExemptAutoBumpParentPointer covers main.autoBumpParent's pointer read.
	ExemptAutoBumpParentPointer ExemptionID = "main.autoBumpParent"
	// ExemptGuardedPassthrough covers git.RunPassthrough.
	ExemptGuardedPassthrough ExemptionID = "internal/git.RunPassthrough"
	// ExemptGitMutation covers main.runGitMutation.
	ExemptGitMutation ExemptionID = "main.runGitMutation"
	// ExemptGitPush covers main.execGitPush.
	ExemptGitPush ExemptionID = "main.execGitPush"
	// ExemptCommitRefUpdate covers main.effectsRefUpdate, the commit
	// pipeline's ref update.
	ExemptCommitRefUpdate ExemptionID = "main.effectsRefUpdate"
	// ExemptHistoryRewriteRecord covers main.recordHistoryRewrite.
	ExemptHistoryRewriteRecord ExemptionID = "main.recordHistoryRewrite"
	// ExemptUndoRefUpdate covers main.effectsUndoRefUpdate, undo's own ref
	// move. It is a row of its own rather than the commit pipeline's: the two
	// argv shapes differ (undo also deletes a ref), and reusing the commit row
	// would make its identifier name a site it does not cover.
	ExemptUndoRefUpdate ExemptionID = "main.effectsUndoRefUpdate"
	// ExemptBackupFetch covers main.fetchSlotObjects' fetch invocation.
	ExemptBackupFetch ExemptionID = "main.fetchSlotObjects"
	// ExemptDoctorRepair covers main.runRepairGit, the git invocations
	// `doctor --action fix` makes to repair git's own leftovers.
	ExemptDoctorRepair ExemptionID = "main.runRepairGit"
)

// DirPinExemption is one row of the table.
type DirPinExemption struct {
	ID     ExemptionID
	Kind   ExemptionKind
	Reason string
}

var dirPinExemptions = []DirPinExemption{
	{
		ID:     ExemptRunWithGitDir,
		Kind:   KindExplicitDir,
		Reason: "runs against a git directory and work tree given as arguments (submodule and cross-repo scans), setting GIT_DIR, GIT_WORK_TREE and the working directory from them",
	},
	{
		ID:     ExemptCatFileBatchAllWithDir,
		Kind:   KindExplicitDir,
		Reason: "streams every object of a repository named by argument; GIT_DIR comes from that argument",
	},
	{
		ID:     ExemptCatFileBatchSHAsWithDir,
		Kind:   KindExplicitDir,
		Reason: "streams named objects of a repository named by argument; GIT_DIR comes from that argument",
	},
	{
		ID:     ExemptSubmoduleRunGit,
		Kind:   KindExplicitDir,
		Reason: "enumerates submodules and resolves parent repositories, each call naming the work tree it must run in; an empty directory means the discovery call that must start from the operator's own directory",
	},
	{
		ID:     ExemptAutoBumpParentPointer,
		Kind:   KindExplicitDir,
		Reason: "reads the gitlink a PARENT repository records for the submodule safegit just committed in, so it must run in the parent's work tree and not in the submodule the pin points at",
	},
	{
		ID:     ExemptGuardedPassthrough,
		Kind:   KindOperatorCwd,
		Reason: "cherry-pick and revert forward the operator's argv to git unchanged, so git must resolve it in the operator's working directory",
	},
	{
		ID:     ExemptGitMutation,
		Kind:   KindOperatorCwd,
		Reason: "switch, merge, rebase, reset, bisect and pull forward the operator's argv (including any pathspec) to git unchanged, as do the DRY RUNS of cherry-pick and revert, which record through this same site rather than through the guarded passthrough that executes them",
	},
	{
		ID:     ExemptGitPush,
		Kind:   KindEffectsHandle,
		Reason: "the push argv names a remote, refspecs, --atomic and per-ref --force-with-lease expectations -- every one of them a ref name or an object name, none of them resolved against a directory -- and the effects handle starts the process so --dry-run can record it instead",
	},
	{
		ID:     ExemptCommitRefUpdate,
		Kind:   KindEffectsHandle,
		Reason: "the commit pipeline's compare-and-swap ref update, minted through the effects handle in both modes -- performed by it in an executing run, recorded instead of performed in a preview; the argv names a ref and two SHAs and resolves against no directory",
	},
	{
		ID:     ExemptUndoRefUpdate,
		Kind:   KindEffectsHandle,
		Reason: "undo's compare-and-swap ref move -- and, for a root undo, the ref DELETION -- minted through the effects handle in both modes; the argv names a ref and object names out of the operation log and resolves against no directory",
	},
	{
		ID:     ExemptBackupFetch,
		Kind:   KindEffectsHandle,
		Reason: "the fetch that downloads a backup slot's objects before a restore fast-forwards onto it; the argv names a remote and a ref, never a path, and the effects handle starts the process so --dry-run can record it instead",
	},
	{
		ID:     ExemptDoctorRepair,
		Kind:   KindEffectsHandle,
		Reason: "the doctor repairs' own git invocations -- storing an orphaned autostash as a stash entry, writing a working-tree blob, re-staging or dropping an index entry -- minted through the effects handle so a preview records them; the argv names working-tree paths, so the site hands the effects handle the repository root as the child's working directory, which is the pin's own value",
	},
	{
		ID:     ExemptHistoryRewriteRecord,
		Kind:   KindEffectsHandle,
		Reason: "records what a history rewrite would do in a dry run -- the ref move plus the reflog expire, repack and prune that follow it; the argv names refs and object-store options and is never executed",
	},
}

// byID indexes the exemption table.
var byID = func() map[ExemptionID]DirPinExemption {
	m := make(map[ExemptionID]DirPinExemption, len(dirPinExemptions))
	for _, e := range dirPinExemptions {
		m[e.ID] = e
	}
	return m
}()

// DirPinExemptions returns the declared table, sorted by ID. It returns a copy.
func DirPinExemptions() []DirPinExemption {
	out := append([]DirPinExemption(nil), dirPinExemptions...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// lookupExemption returns the declared row, or an error naming the undeclared
// identifier.
func lookupExemption(id ExemptionID) (DirPinExemption, error) {
	e, ok := byID[id]
	if !ok {
		return DirPinExemption{}, &Error{Msg: "gitexec: undeclared directory-pin exemption " + string(id) + "; declare it in internal/gitexec/exemptions.go"}
	}
	return e, nil
}

// MustBeExempt asserts that id is declared with the given kind. It panics
// otherwise: an undeclared exemption is a programming error in safegit, not a
// runtime condition, and the identifiers are compile-time constants.
func MustBeExempt(id ExemptionID, kind ExemptionKind) {
	e, err := lookupExemption(id)
	if err != nil {
		panic(err.Error())
	}
	if e.Kind != kind {
		panic("gitexec: exemption " + string(id) + " is declared " + string(e.Kind) + ", not " + string(kind))
	}
}
