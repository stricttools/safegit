// Package gitexec is safegit's single git-execution boundary.
//
// Three things live here and nowhere else:
//
//  1. Process construction. Every git subprocess safegit starts is built by
//     Command: the argv prefix, the environment assembly and the
//     context-carried execution overrides are applied in exactly one place.
//  2. The git argv vocabulary. The classification table in classify.go is the
//     single authority over which git subcommands safegit may invoke and what
//     each of them can do to a repository.
//  3. The declared directory-pin exemption table (exemptions.go): the closed
//     list of sites that are NOT subject to the repo-root working-directory
//     pin, each with the reason it cannot be.
//
// The package is a leaf -- it imports only the standard library -- so both
// internal/git (safegit's git plumbing interface) and internal/submodule
// (which cannot import internal/git without a cycle) route through it.
package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Binary is the git executable safegit invokes.
const Binary = "git"

// globalPrefix is prepended to every git argv safegit builds.
//
// --no-optional-locks keeps git from taking .git/index.lock for the optional
// index refresh that read-only commands would otherwise perform, which is what
// lets many safegit processes share one repository without contending on the
// shared index lock.
var globalPrefix = []string{"--no-optional-locks"}

// GlobalPrefix returns the argv prefix safegit puts on every git invocation.
// It returns a copy: the prefix is the boundary's, not a caller's, to change.
func GlobalPrefix() []string {
	return append([]string(nil), globalPrefix...)
}

// dirKey is the context key for an explicit git-directory override.
type dirKey struct{}

// dirVal holds the git directory and work tree a context-scoped override
// targets.
type dirVal struct{ GitDir, WorkTree string }

// rootKey is the context key for the repository-root working-directory pin.
type rootKey struct{}

// noRootPinKey is the context key that records a declared exemption from the
// repository-root pin.
type noRootPinKey struct{}

// previewKey is the context key that marks a run as a preview -- a --dry-run
// dispatch, which promises to change nothing on disk.
type previewKey struct{}

// quarantineKey is the context key for the object quarantine a preview writes
// through.
type quarantineKey struct{}

// quarantine is where a preview's object writes go, and which object stores
// must stay readable while they do.
type quarantine struct {
	// Dir becomes GIT_OBJECT_DIRECTORY: the throwaway store every object a
	// preview creates is written into, and which is deleted with the preview.
	Dir string
	// Alternates are the object stores the preview must still READ: the
	// repository's own, first of all, since the preview stages the parent tree
	// and builds a commit on it. They become GIT_ALTERNATE_OBJECT_DIRECTORIES,
	// merged with whatever the environment already carried.
	Alternates []string
}

// WithDir returns a context that targets a specific repository. Every git
// subprocess built from it sets GIT_DIR, GIT_WORK_TREE and its working
// directory accordingly, regardless of the process's own directory.
func WithDir(ctx context.Context, gitDir, workTree string) context.Context {
	return context.WithValue(ctx, dirKey{}, dirVal{gitDir, workTree})
}

// DirOverride reports the context-carried repository override, if any.
func DirOverride(ctx context.Context) (gitDir, workTree string, ok bool) {
	v, ok := ctx.Value(dirKey{}).(dirVal)
	if !ok {
		return "", "", false
	}
	return v.GitDir, v.WorkTree, true
}

// WithRoot returns a context carrying the repository-root pin: every git
// subprocess safegit itself constructs from it runs with its working directory
// set to root.
//
// The pin exists because a large part of git's plumbing vocabulary is scoped to
// the process working directory -- `ls-files` defaults to the pathspec ".",
// `ls-tree` prefixes the cwd path onto the tree it reads, `apply` resolves the
// paths inside a patch against the cwd -- so an operator invoking safegit from
// a subdirectory would otherwise silently narrow what safegit sees and what it
// rewrites. safegit's own argv is built from repo-relative or absolute paths,
// so pinning it to the root is what makes it mean the same thing from
// everywhere.
//
// An empty root returns ctx unchanged: a repository with no work tree has no
// root to pin to.
func WithRoot(ctx context.Context, root string) context.Context {
	if root == "" {
		return ctx
	}
	return context.WithValue(ctx, rootKey{}, root)
}

// Root reports the context-carried repository-root pin, if any.
func Root(ctx context.Context) (string, bool) {
	root, ok := ctx.Value(rootKey{}).(string)
	return root, ok
}

// WithoutRootPin returns a context whose repository-root pin does not apply,
// recording which declared exemption suspends it. It panics when id is not
// declared in the exemption table with kind KindOperatorCwd: the table is the
// only way a site can escape the pin.
func WithoutRootPin(ctx context.Context, id ExemptionID) context.Context {
	MustBeExempt(id, KindOperatorCwd)
	return context.WithValue(ctx, noRootPinKey{}, id)
}

// rootPinSuspended reports whether a declared exemption suspends the pin.
func rootPinSuspended(ctx context.Context) bool {
	_, ok := ctx.Value(noRootPinKey{}).(ExemptionID)
	return ok
}

// WithPreview marks a context as a preview: the --dry-run dispatch, which
// promises to change nothing on disk. It is what makes the quarantine
// ENFORCEABLE rather than merely available -- see Command, which refuses an
// object-writing invocation on a previewing context that carries no quarantine.
//
// The mark is separate from the quarantine itself precisely so the refusal has
// something to fire on: a previewing command that forgot to install a
// quarantine is exactly the case worth catching.
func WithPreview(ctx context.Context) context.Context {
	if InPreview(ctx) {
		return ctx
	}
	return context.WithValue(ctx, previewKey{}, true)
}

// InPreview reports whether this context belongs to a preview.
func InPreview(ctx context.Context) bool {
	v, _ := ctx.Value(previewKey{}).(bool)
	return v
}

// WithObjectQuarantine returns a previewing context whose git subprocesses
// write every object they create into dir instead of into the repository, while
// still reading the object stores named by alternates.
//
// A preview that promises to change nothing cannot make that promise while
// `git add`, `git write-tree` and `git commit-tree` deposit blobs, trees and
// commits in the repository's own store, where they stay as unreferenced loose
// objects. Pointing GIT_OBJECT_DIRECTORY at a throwaway directory keeps the
// preview's arithmetic exact -- the tree SHA it computes is the tree SHA the
// real run would compute -- and leaves nothing behind when the directory goes.
//
// It marks the context as a preview too: a quarantine exists for no other
// reason.
func WithObjectQuarantine(ctx context.Context, dir string, alternates ...string) context.Context {
	q := quarantine{Dir: dir, Alternates: append([]string(nil), alternates...)}
	return context.WithValue(WithPreview(ctx), quarantineKey{}, q)
}

// ObjectQuarantine reports the context-carried object quarantine, if any.
func ObjectQuarantine(ctx context.Context) (dir string, alternates []string, ok bool) {
	q, ok := ctx.Value(quarantineKey{}).(quarantine)
	if !ok {
		return "", nil, false
	}
	return q.Dir, append([]string(nil), q.Alternates...), true
}

// Spec describes one git invocation.
type Spec struct {
	// Args is the git argv WITHOUT the binary name and WITHOUT the global
	// prefix; Command adds both.
	Args []string

	// Env holds extra environment entries ("KEY=value"). When non-empty (or
	// when an override adds entries) the subprocess inherits os.Environ() plus
	// these.
	//
	// It may NOT carry a directory override: GIT_DIR, GIT_WORK_TREE and
	// GIT_COMMON_DIR are refused here (see validateEnv). Setting one through the
	// environment targets another repository without declaring it, which is
	// exactly what the exemption table exists to make impossible; the GitDir and
	// WorkTree fields are the declared way to say it.
	Env []string

	// Exempt names this site's entry in the declared directory-pin exemption
	// table. It is REQUIRED whenever the spec sets GitDir, WorkTree or Dir, and
	// must be empty otherwise -- a site cannot pick its own directory without
	// declaring why.
	Exempt ExemptionID

	// GitDir, WorkTree and Dir target a specific repository from the site's own
	// arguments. Setting any of them supersedes the context-carried directory
	// override and the repository-root pin (see Exempt).
	GitDir   string
	WorkTree string
	Dir      string
}

// explicitDir reports whether the spec targets a directory from its own
// arguments.
func (s Spec) explicitDir() bool {
	return s.GitDir != "" || s.WorkTree != "" || s.Dir != ""
}

// Command builds the git subprocess for one spec.
//
// It errors when the argv names a subcommand the classification table does not
// declare, and when the spec's directory targeting is not backed by a declared
// exemption. It never starts the process; the caller owns that.
func Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	if err := Validate(s.Args); err != nil {
		return nil, err
	}
	if err := s.validateExemption(); err != nil {
		return nil, err
	}
	if err := s.validateEnv(); err != nil {
		return nil, err
	}

	full := append(GlobalPrefix(), s.Args...)
	cmd := exec.CommandContext(ctx, Binary, full...)

	env := append([]string(nil), s.Env...)

	// Directory targeting. This is the ONE override the explicit-directory
	// sites supersede: they were handed a repository and there is no operator
	// working directory left for the pin to correct.
	switch {
	case s.explicitDir():
		if s.GitDir != "" {
			env = append(env, "GIT_DIR="+s.GitDir)
		}
		if s.WorkTree != "" {
			env = append(env, "GIT_WORK_TREE="+s.WorkTree)
		}
		cmd.Dir = s.Dir
		if cmd.Dir == "" {
			cmd.Dir = s.WorkTree
		}
		if cmd.Dir == "" {
			cmd.Dir = s.GitDir
		}
	default:
		if gitDir, workTree, ok := DirOverride(ctx); ok {
			cmd.Dir = workTree
			env = append(env, "GIT_DIR="+gitDir, "GIT_WORK_TREE="+workTree)
		} else if root, ok := Root(ctx); ok && !rootPinSuspended(ctx) {
			cmd.Dir = root
		}
	}

	// Every OTHER context-carried override belongs below this line and applies
	// to EVERY spec, including the explicit-directory ones. Nothing here may be
	// placed inside the switch above: exemption from the directory pin is not
	// exemption from anything else.
	//
	// The object quarantine is the case that makes the rule concrete: the
	// explicit-directory sites are the submodule scan and cat-file paths, and a
	// submodule preview that skipped the quarantine would write into the
	// submodule's object store while the parent's was protected.
	quarantineEnv, err := s.quarantine(ctx)
	if err != nil {
		return nil, err
	}
	env = append(env, quarantineEnv...)

	// os.Environ() is the base for every override that travels as an
	// environment variable; the quarantine composes on top of it.
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd, nil
}

// objectEnvVars are the environment variables that retarget git's object store.
var (
	objectDirVar     = "GIT_OBJECT_DIRECTORY"
	altObjectDirsVar = "GIT_ALTERNATE_OBJECT_DIRECTORIES"
)

// quarantine renders the object-store environment for one spec, and enforces
// the rule that gives the quarantine its worth: in a preview, an invocation the
// classification table says can WRITE objects may not run without one.
//
// The refusal is a hard error rather than a warning because the failure it
// guards is invisible -- a preview that writes objects leaves them in the
// repository as unreferenced loose objects, and nothing downstream ever
// reports it.
func (s Spec) quarantine(ctx context.Context) ([]string, error) {
	dir, alternates, ok := ObjectQuarantine(ctx)
	if !ok {
		if InPreview(ctx) && WritesObjects(s.Args) {
			return nil, &Error{Msg: "gitexec: refusing to run `git " + strings.Join(s.Args, " ") +
				"` in a preview with no object quarantine installed: the classification table says this invocation can write to the object store, and a preview must leave it untouched (install one with gitexec.WithObjectQuarantine before the command's first object-writing call)"}
		}
		return nil, nil
	}

	// The alternates are assembled rather than assigned: GIT_OBJECT_DIRECTORY
	// REPLACES the object store git would have used, so every store the
	// invocation still has to read has to be listed here. Go's exec dedups the
	// environment keeping the LAST occurrence of a name, so writing a bare value
	// would silently drop an inherited one.
	merged := append([]string(nil), splitObjectDirs(os.Getenv(altObjectDirsVar))...)
	for _, e := range s.Env {
		if name, value, found := strings.Cut(e, "="); found && name == altObjectDirsVar {
			merged = append(merged, splitObjectDirs(value)...)
		}
	}
	merged = append(merged, alternates...)
	if s.GitDir != "" {
		// A spec that names its own repository reads that repository's objects,
		// and the quarantine has just displaced them.
		merged = append(merged, filepath.Join(s.GitDir, "objects"))
	}

	return []string{
		objectDirVar + "=" + dir,
		altObjectDirsVar + "=" + strings.Join(dedupePaths(merged), string(os.PathListSeparator)),
	}, nil
}

// splitObjectDirs splits a GIT_ALTERNATE_OBJECT_DIRECTORIES value into its
// entries, dropping empty ones.
func splitObjectDirs(value string) []string {
	if value == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(value, string(os.PathListSeparator)) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dedupePaths keeps the first occurrence of each path, so a store listed twice
// is searched once.
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ArgvAny returns the full git argv -- binary, global prefix, then args -- as
// []interface{}, the shape the strictcli effects handle takes.
//
// The invocation is not built by Command because safegit does not start it: the
// effects handle does, in the process working directory. exempt names the
// declared reason that is acceptable.
//
// It takes no context, so the context-carried overrides Command applies -- the
// object quarantine among them -- cannot reach these invocations. That is not a
// hole, because no argv built here can write objects during a preview:
//
//   - An argv matching an allowlisted observe prefix EXECUTES even in dry mode,
//     but ObservePrefixes admits only verbs the classification table declares
//     observe-only unconditionally, which by definition write nothing.
//   - Every other argv is RECORDED in dry mode and never started, whatever it
//     names -- which is how `repack` and `prune` appear in a rewrite preview's
//     would-do log without ever running.
//
// Outside a preview there is no quarantine to reach in the first place: an
// executing run writes its objects into the repository on purpose.
func ArgvAny(exempt ExemptionID, args ...string) ([]interface{}, error) {
	if err := Validate(args); err != nil {
		return nil, err
	}
	if _, err := lookupExemption(exempt); err != nil {
		return nil, err
	}
	full := append([]string{Binary}, GlobalPrefix()...)
	full = append(full, args...)
	argv := make([]interface{}, 0, len(full))
	for _, a := range full {
		argv = append(argv, a)
	}
	return argv, nil
}

// validateExemption enforces the pairing between directory targeting and the
// declared exemption table.
func (s Spec) validateExemption() error {
	if s.Exempt == "" {
		if s.explicitDir() {
			return &Error{Msg: "gitexec: a spec that sets its own git directory must name its entry in the directory-pin exemption table"}
		}
		return nil
	}
	e, err := lookupExemption(s.Exempt)
	if err != nil {
		return err
	}
	if s.explicitDir() && e.Kind != KindExplicitDir {
		return &Error{Msg: "gitexec: exemption " + string(s.Exempt) + " is declared " + string(e.Kind) + ", which does not permit setting a git directory"}
	}
	if !s.explicitDir() && e.Kind == KindExplicitDir {
		return &Error{Msg: "gitexec: exemption " + string(s.Exempt) + " is declared " + string(KindExplicitDir) + " but the spec sets no git directory"}
	}
	return nil
}

// dirEnvVars are the environment variables that retarget git's directories.
// The boundary sets them itself, from Spec.GitDir/WorkTree or from the
// context-carried override; a spec may not smuggle one in through Spec.Env.
//
// GIT_OBJECT_DIRECTORY is banned for the same reason: it retargets the object
// store, and the ONE thing that may do that is the context-carried quarantine,
// whose presence or absence the preview refusal reads.
var dirEnvVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"}

// validateEnv refuses a directory override spelled as an environment entry.
//
// Without this, the exemption pairing enforced by validateExemption inspects
// only the GitDir, WorkTree and Dir fields, and a site could reach another
// repository -- escaping the repository-root pin and the declared table both --
// simply by writing "GIT_DIR=..." into Spec.Env.
func (s Spec) validateEnv() error {
	for _, e := range s.Env {
		name := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			name = e[:i]
		}
		for _, banned := range dirEnvVars {
			if name == banned {
				return &Error{Msg: "gitexec: " + banned + " in Spec.Env is an undeclared directory override; set Spec.GitDir/Spec.WorkTree with the matching entry from the directory-pin exemption table instead"}
			}
		}
	}
	return nil
}

// Error is the boundary's own error type, so a caller can tell a refusal by the
// boundary from a failure reported by git.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }
