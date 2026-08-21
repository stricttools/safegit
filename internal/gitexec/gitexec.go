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

// Spec describes one git invocation.
type Spec struct {
	// Args is the git argv WITHOUT the binary name and WITHOUT the global
	// prefix; Command adds both.
	Args []string

	// Env holds extra environment entries ("KEY=value"). When non-empty (or
	// when an override adds entries) the subprocess inherits os.Environ() plus
	// these.
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

	// os.Environ() is the base for every override that travels as an
	// environment variable; Phase 3.1's object quarantine composes on top of it.
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd, nil
}

// ArgvAny returns the full git argv -- binary, global prefix, then args -- as
// []interface{}, the shape the strictcli effects handle takes.
//
// The invocation is not built by Command because safegit does not start it: the
// effects handle does, in the process working directory. exempt names the
// declared reason that is acceptable.
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

// Error is the boundary's own error type, so a caller can tell a refusal by the
// boundary from a failure reported by git.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }
