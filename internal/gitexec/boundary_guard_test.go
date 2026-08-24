package gitexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is the structural guard behind the claim that internal/gitexec is
// safegit's single git-execution boundary. Prose cannot hold that line; a
// mechanical scan of the source can.
//
// Four shapes are refused outside the boundary package:
//
//   - a Command / CommandContext call on os/exec naming the git binary, which
//     is a subprocess built without the argv prefix, the environment assembly
//     or the context-carried overrides. The package is identified by its import
//     PATH, so an alias (import osexec "os/exec") does not hide it;
//   - a []interface{}{"git", ...} argv literal, which is the same bypass in the
//     shape the strictcli effects handle takes;
//   - the "--no-optional-locks" string, which is the boundary's own prefix and
//     appears anywhere else only because someone rebuilt the argv by hand;
//   - the 40-zero object name written out as a literal outside internal/git,
//     which is git's "this object must not exist" convention and has exactly
//     one spelling in safegit: git.ZeroSHA. A second spelling is a second
//     definition of the create-only contract.
//
// Two evasions are KNOWN and accepted, because catching either needs full type
// checking (loading and type-checking every package) rather than the per-file
// AST parse this guard does, and that cost buys nothing against an accident --
// only against someone deliberately hiding a git call from a guard they can
// read:
//
//   - a binary name reached through a constant or variable rather than a string
//     literal (exec.Command(gitBin, ...)), because resolving the identifier to
//     its value is constant evaluation across files;
//   - a shell wrapper (exec.Command("sh", "-c", "git ...")), because the git
//     invocation is inside an opaque string the shell parses, not in the argv.
//
// A FIFTH shape is refused for a different reason: a LITERAL authoring verb in
// a git argv construction -- `"commit"`, or `"merge"`/`"cherry-pick"`/`"revert"`
// with none of that verb's suppressing tokens in the same construction. That is
// the source half of the single-authorship boundary, whose enforcing half is the
// runtime check in Validate (see authoring.go). The two halves read the SAME
// declaration -- the verb table's Authors and SuppressedBy fields -- so they
// cannot disagree about what authors a commit.
//
// The division of labour between them is deliberate:
//
//   - the RUNTIME check is the enforcement. It sees every argv safegit builds,
//     including the ones whose verb comes from a variable (the guarded
//     passthroughs), and it needs no list of anything;
//   - this SOURCE scan is the earlier answer, and it is deliberately narrow. It
//     recognizes two construction shapes and refuses a literal inside them; it
//     never guesses. A construction it does not recognize is not a hole, because
//     the runtime check is behind it.
//
// Scope rule: _test.go files anywhere and the whole internal/testutil package
// are exempt. Tests exercise git directly by design -- that is how a test builds
// the fixture the production code is then measured against. Production packages
// are not exempt, and neither is a non-test file that happens to sit in
// internal/test.

// execImportPath is the package whose process construction the boundary owns.
const execImportPath = "os/exec"

// zeroSHALiteral is the all-zero object name, spelled here (in a _test.go file,
// which the guard does not scan) so the guard can recognize it anywhere else.
var zeroSHALiteral = strings.Repeat("0", 40)

// zeroSHAHomeDir is the one package allowed to write that literal: internal/git
// declares git.ZeroSHA, the single spelling everything else uses.
const zeroSHAHomeDir = "internal/git"

// exemptDirs are directories whose Go source the guard does not scan, each with
// the reason.
var exemptDirs = map[string]string{
	"internal/gitexec":  "the boundary itself: this is the one package allowed to build a git subprocess",
	"internal/testutil": "test-only helpers; tests drive git directly to build fixtures",
}

// skipDirs are directories with no safegit source to scan. The keys are paths
// RELATIVE TO THE REPOSITORY ROOT, not bare directory names: skipping by name
// at any depth would silently unscan a Go package that happened to sit under a
// directory sharing one of these names.
var skipDirs = map[string]bool{
	".git": true, "testdata": true, "vendor": true, "node_modules": true,
	"docs": true, "scripts": true, "todo": true,
	// experiments is the declared scratch space for git-behavior probes
	// (throwaway repos included); its contents are gitignored and
	// disposable, and guards deliberately do not scan it. See
	// experiments/README.md.
	"experiments": true,
}

type violation struct {
	pos  string
	what string
}

// staticAuthoringVerbs are the authoring verbs this SOURCE scan refuses when it
// finds one spelled as a literal. It is the verb table's Authors set minus the
// entries in staticAuthoringExcluded, and
// TestStaticAuthoringVerbsCoverTheTableExactly holds the two halves together.
var staticAuthoringVerbs = map[string]bool{
	"commit":      true,
	"merge":       true,
	"cherry-pick": true,
	"revert":      true,
}

// staticAuthoringExcluded names the authoring verbs the source scan deliberately
// does NOT refuse, each with the reason.
var staticAuthoringExcluded = map[string]string{
	"rebase": "the one declared door is a rebase, and whether a call site holds it is a RUN-TIME fact this scan cannot read; refusing the literal would mean a second file-and-line exemption table restating the door table, so the runtime check carries rebase alone",
}

// argvTakingCalls are the functions whose string-literal arguments form a git
// argv. The match is on the FUNCTION NAME (`git.Run`, `runGitMutation`), not on
// a resolved package, for the same reason the rest of this guard parses per file
// rather than type-checking the module.
//
// The list being incomplete is not a hole: it is the source scan's reach, and
// the runtime check behind it needs no list at all. What the list buys is the
// earlier answer on the shapes safegit actually writes.
var argvTakingCalls = map[string]string{
	"Run":                   "internal/git.Run and its kin: the variadic tail IS the git argv",
	"RunWithEnv":            "internal/git.RunWithEnv",
	"RunWithEnvStdin":       "internal/git.RunWithEnvStdin",
	"RunWithGitDir":         "internal/git.RunWithGitDir",
	"RunPassthrough":        "internal/git.RunPassthrough",
	"RunPassthroughWithEnv": "internal/git.RunPassthroughWithEnv",
	"RunPassthroughTo":      "internal/git.RunPassthroughTo",
	"runGit":                "internal/submodule.runGit",
	"runGitMutation":        "main.runGitMutation, the effects-handle route for the guarded commands",
	"runPassthrough":        "main.runPassthrough",
	"runBackupGit":          "main.runBackupGit, the effects-handle route for a restore's fetch and fast-forward",
	"runRepairGit":          "main.runRepairGit, the effects-handle route for the doctor repairs",
	"ArgvAny":               "the boundary's own effects-handle argv builder",
}

// authoringViolation reports the refused authoring shape for one argv
// construction: the verb literal, and every string literal the construction
// carries after it.
//
// suppressors come from the verb's own SuppressedBy list, so this scan and the
// runtime check answer "does this authorize git to commit" from one declaration.
func authoringViolation(verb string, rest []string) (string, bool) {
	if !staticAuthoringVerbs[verb] {
		return "", false
	}
	v, ok := Lookup(verb)
	if !ok {
		return "", false
	}
	for _, tok := range v.SuppressedBy {
		for _, r := range rest {
			if r == tok {
				return "", false
			}
		}
	}
	return "a `git " + verb + "` argv built with no suppressing token on it (" +
		strings.Join(v.SuppressedBy, ", ") + "): git would AUTHOR the commit, and every commit safegit makes is its own pipeline's", true
}

// stringLits returns the values of the string-literal elements of a list, in
// order, skipping everything that is not one.
func stringLits(list []ast.Expr) []string {
	var out []string
	for _, e := range list {
		if s, ok := stringLit(e); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestGitExecutionBoundaryIsTheOnlyOne(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var found []violation

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if skipDirs[filepath.ToSlash(rel)] {
				return filepath.SkipDir
			}
			if _, exempt := exemptDirs[filepath.ToSlash(rel)]; exempt {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		found = append(found, scanFile(fset, file, filepath.ToSlash(rel))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	for _, v := range found {
		t.Errorf("%s: %s -- every git subprocess must be built by internal/gitexec.Command, every git argv by internal/gitexec.ArgvAny, and git's create-only object name spelled once as git.ZeroSHA", v.pos, v.what)
	}
}

// execNames maps every local name bound to os/exec in this file to true, so the
// scan follows the IMPORT PATH rather than the package's default name. It also
// reports a dot-import of os/exec, which binds no name at all and would make
// every call to it unqualified and invisible to the scan.
func execNames(file *ast.File) (names map[string]bool, dotImport token.Pos) {
	names = map[string]bool{}
	dotImport = token.NoPos
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != execImportPath {
			continue
		}
		if imp.Name == nil {
			// No alias: the local name is the package name, which for every
			// standard-library path is the last path element.
			names[path[strings.LastIndex(path, "/")+1:]] = true
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImport = imp.Pos()
		case "_":
			// Imported for side effects only; nothing is bound.
		default:
			names[imp.Name.Name] = true
		}
	}
	return names, dotImport
}

// scanFile reports the four refused shapes in one file.
func scanFile(fset *token.FileSet, file *ast.File, rel string) []violation {
	var out []violation
	at := func(p token.Pos) string {
		pos := fset.Position(p)
		return rel + ":" + strconv.Itoa(pos.Line)
	}

	execPkg, dotImport := execNames(file)
	if dotImport.IsValid() {
		out = append(out, violation{at(dotImport), "a dot-import of " + execImportPath + " hides every process construction from this guard"})
	}
	zeroSHAAllowed := rel == zeroSHAHomeDir || strings.HasPrefix(rel, zeroSHAHomeDir+"/")

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// The single-authorship rule's CALL shape, checked before the
			// os/exec one because it matches on the function name alone and
			// applies to a plain identifier as well as a selector.
			if name := calleeName(node.Fun); argvTakingCalls[name] != "" {
				if lits := stringLits(node.Args); len(lits) > 0 {
					if what, bad := authoringViolation(lits[0], lits[1:]); bad {
						out = append(out, violation{at(node.Pos()), what})
					}
				}
			}
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || !execPkg[pkg.Name] {
				return true
			}
			var binaryArg ast.Expr
			switch sel.Sel.Name {
			case "Command":
				if len(node.Args) > 0 {
					binaryArg = node.Args[0]
				}
			case "CommandContext":
				if len(node.Args) > 1 {
					binaryArg = node.Args[1]
				}
			default:
				return true
			}
			if s, ok := stringLit(binaryArg); ok && s == Binary {
				out = append(out, violation{at(node.Pos()), pkg.Name + "." + sel.Sel.Name + " (" + execImportPath + ") of the git binary outside the execution boundary"})
			}
		case *ast.CompositeLit:
			// The single-authorship rule's SLICE shape. A []string argv carries
			// the verb at element 0: the binary and the global prefix are the
			// boundary's to add. A first element that is not a string literal --
			// the `[]string{gitCmd}` the guarded passthroughs build -- is left
			// to the runtime check, which can see what the variable holds.
			if isStringSlice(node.Type) && len(node.Elts) > 0 {
				if lits := stringLits(node.Elts); len(lits) > 0 {
					if first, ok := stringLit(node.Elts[0]); ok {
						if what, bad := authoringViolation(first, lits[1:]); bad {
							out = append(out, violation{at(node.Pos()), what})
						}
					}
				}
			}
			if !isInterfaceSlice(node.Type) || len(node.Elts) == 0 {
				return true
			}
			if s, ok := stringLit(node.Elts[0]); ok && s == Binary {
				out = append(out, violation{at(node.Pos()), `a []interface{}{"git", ...} argv literal outside the execution boundary`})
			}
		case *ast.BasicLit:
			s, ok := stringLit(node)
			if !ok {
				return true
			}
			if s == "--no-optional-locks" {
				out = append(out, violation{at(node.Pos()), `the "--no-optional-locks" prefix spelled outside the execution boundary`})
			}
			if s == zeroSHALiteral && !zeroSHAAllowed {
				out = append(out, violation{at(node.Pos()), "the all-zero object name spelled as a literal outside " + zeroSHAHomeDir + "; use git.ZeroSHA, the single spelling of git's create-only contract"})
			}
		}
		return true
	})
	return out
}

// stringLit returns the value of an untyped string literal.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// calleeName renders the name a call expression invokes: the bare identifier for
// a package-local function, and the selector's own name for a qualified one
// (`git.Run` reports "Run"). It is a NAME match by design -- resolving it to a
// package would mean type-checking the module, and the shapes this guard is
// written against are the ones safegit itself writes.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// isStringSlice reports whether a composite-literal type is []string, the shape
// every git argv safegit builds for the boundary takes.
func isStringSlice(t ast.Expr) bool {
	arr, ok := t.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	id, ok := arr.Elt.(*ast.Ident)
	return ok && id.Name == "string"
}

// isInterfaceSlice reports whether a composite-literal type is []interface{} or
// its []any spelling.
func isInterfaceSlice(t ast.Expr) bool {
	arr, ok := t.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	switch elt := arr.Elt.(type) {
	case *ast.InterfaceType:
		return elt.Methods == nil || len(elt.Methods.List) == 0
	case *ast.Ident:
		return elt.Name == "any"
	}
	return false
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the package directory")
		}
		dir = parent
	}
}

// TestBoundaryGuardCatchesItsRefusedShapes measures the guard itself. A scan
// that never fires proves nothing about the repository, so each refused shape
// is fed to scanFile as source and must be reported -- including the evasions
// the hardening exists for: an import alias, and a zero-SHA literal outside
// internal/git.
func TestBoundaryGuardCatchesItsRefusedShapes(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		src  string
		want bool
	}{
		{
			name: "plain exec.Command of git",
			rel:  "somepkg/a.go",
			src:  "package p\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"git\", \"status\") }\n",
			want: true,
		},
		{
			name: "aliased os/exec import",
			rel:  "somepkg/a.go",
			src:  "package p\nimport osexec \"os/exec\"\nfunc f() { _ = osexec.CommandContext(nil, \"git\", \"status\") }\n",
			want: true,
		},
		{
			name: "dot-imported os/exec",
			rel:  "somepkg/a.go",
			src:  "package p\nimport . \"os/exec\"\nfunc f() { _ = Command(\"git\") }\n",
			want: true,
		},
		{
			name: "a package merely named exec that is not os/exec",
			rel:  "somepkg/a.go",
			src:  "package p\nimport \"example.com/other/exec\"\nfunc f() { _ = exec.Command(\"git\", \"status\") }\n",
			want: false,
		},
		{
			name: "effects-handle argv literal",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []interface{}{\"git\", \"push\"}\n",
			want: true,
		},
		{
			// The same shape in its []any spelling: isInterfaceSlice accepts
			// both, and the two are one gofmt -s away from each other.
			name: "effects-handle argv literal spelled []any",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []any{\"git\", \"push\"}\n",
			want: true,
		},
		{
			name: "the boundary's own prefix rebuilt by hand",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []string{\"--no-optional-locks\"}\n",
			want: true,
		},
		{
			name: "zero-SHA literal outside internal/git",
			rel:  "somepkg/a.go",
			src:  "package p\nconst nullSHA = \"" + zeroSHALiteral + "\"\n",
			want: true,
		},
		{
			name: "zero-SHA literal in the package that declares ZeroSHA",
			rel:  "internal/git/git.go",
			src:  "package git\nconst ZeroSHA = \"" + zeroSHALiteral + "\"\n",
			want: false,
		},

		// The single-authorship rule. Each planted violation is the shape a
		// production site would really take.
		{
			name: "an argv slice literal that lets git author a merge",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []string{\"merge\", \"topic\"}\n",
			want: true,
		},
		{
			name: "the same construction with the suppressor on it",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []string{\"merge\", \"--no-ff\", \"--no-commit\", \"topic\"}\n",
			want: false,
		},
		{
			name: "an argv slice literal naming git's own commit verb",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _ = append([]string{\"commit\", \"-m\", \"x\"}, nil...) }\n",
			want: true,
		},
		{
			name: "a cherry-pick argv built as a call to an argv-taking runner",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _, _, _ = git.Run(ctx, \"cherry-pick\", \"abc1234\") }\n",
			want: true,
		},
		{
			name: "the same runner call with cherry-pick's own -n",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _, _, _ = git.Run(ctx, \"cherry-pick\", \"-n\", \"abc1234\") }\n",
			want: false,
		},
		{
			// backup restore's real argv: a fast-forward moves a ref onto a
			// commit that already exists.
			name: "a merge argv suppressed by --ff-only",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _, _, _ = git.Run(ctx, \"merge\", \"--ff-only\", \"FETCH_HEAD\") }\n",
			want: false,
		},
		{
			// `-n` is --no-stat on merge, not --no-commit: the suppressor sets
			// are per verb precisely so this stays a violation.
			name: "a merge argv carrying -n, which on merge means --no-stat",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _, _, _ = git.Run(ctx, \"merge\", \"-n\", \"topic\") }\n",
			want: true,
		},
		{
			name: "a forwarded --continue, which authors the commit",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _ = git.RunPassthrough(ctx, \"revert\", \"--continue\") }\n",
			want: true,
		},
		{
			// The effects-handle shape, whose first element is the BINARY. It is
			// already refused by the argv-literal rule above whatever verb
			// follows, and the row is here to state that an authoring one is
			// covered too rather than falling between the two rules.
			name: "an effects-handle git argv literal that authors",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []interface{}{\"git\", \"revert\", \"abc1234\"}\n",
			want: true,
		},
		{
			// autobump's safegit self-spawn: the binary is not the literal
			// "git", so the construction is not a git argv at all.
			name: "a safegit self-spawn through the effects handle",
			rel:  "somepkg/a.go",
			src:  "package p\nvar argv = []interface{}{safegitBin, \"commit\", \"-m\", msg}\n",
			want: false,
		},
		{
			// The verb is a variable, which is the shape the guarded
			// passthroughs take. The runtime check is what covers those; the
			// source scan cannot and must not guess.
			name: "an argv whose verb comes from a variable",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _ = append([]string{gitCmd}, args...) }\n",
			want: false,
		},
		{
			// An operation NAME, not an argv: these are everywhere (oplog op
			// names, lock names, map keys) and none of them is a git argv.
			name: "an operation name passed to a non-argv function",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _, _ = acquireOperationLock(flags, gitDir, \"cherry-pick\") }\n",
			want: false,
		},
		{
			name: "a map keyed by operation name",
			rel:  "somepkg/a.go",
			src:  "package p\nvar undoable = map[string]string{\"commit\": \"parent\", \"revert\": \"revert\"}\n",
			want: false,
		},
		{
			// rebase is deliberately outside the SOURCE rule: see
			// staticAuthoringExcluded. The runtime check covers it.
			name: "the rebase passthrough's own argv construction",
			rel:  "somepkg/a.go",
			src:  "package p\nfunc f() { _ = append([]string{\"rebase\"}, args...) }\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.rel, tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parsing the fixture: %v", err)
			}
			got := scanFile(fset, file, tc.rel)
			if tc.want && len(got) == 0 {
				t.Errorf("the guard did not report %s", tc.name)
			}
			if !tc.want && len(got) != 0 {
				t.Errorf("the guard reported %v for %s, which is not a refused shape", got, tc.name)
			}
		})
	}
}

// TestBoundaryGuardSkipsOnlyRootDirectories: the no-source skip list is
// repo-root-relative, so a Go package nested under a directory that happens to
// share one of those names is still scanned.
func TestBoundaryGuardSkipsOnlyRootDirectories(t *testing.T) {
	for dir := range skipDirs {
		// A key with a separator would never match the single-segment relative
		// path of a top-level directory, so it would skip nothing at all.
		if strings.Contains(dir, "/") || strings.Contains(dir, string(filepath.Separator)) {
			t.Errorf("skip entry %q must name one repository-root directory", dir)
		}
	}
	// A directory of the same name nested inside a package is NOT skipped: the
	// walk compares the repo-root-relative path, not the base name.
	nested := filepath.Join("internal", "somepkg", "scripts")
	if skipDirs[filepath.ToSlash(nested)] {
		t.Errorf("%q must not be skipped: only the repository-root directory is", nested)
	}
}

// TestStaticAuthoringVerbsCoverTheTableExactly binds the source scan's verb set
// to the classification table's own Authors set. A verb that becomes able to
// author must land in one of the two maps deliberately -- refused by the scan,
// or excluded from it with the reason written down -- rather than quietly
// falling outside both.
func TestStaticAuthoringVerbsCoverTheTableExactly(t *testing.T) {
	for _, v := range Verbs() {
		scanned := staticAuthoringVerbs[v.Name]
		reason, excluded := staticAuthoringExcluded[v.Name]
		switch {
		case v.Authors && !scanned && !excluded:
			t.Errorf("verb %q can author a commit but the source scan neither refuses nor excludes it", v.Name)
		case v.Authors && scanned && excluded:
			t.Errorf("verb %q is both refused and excluded by the source scan", v.Name)
		case !v.Authors && (scanned || excluded):
			t.Errorf("verb %q cannot author a commit, so the source scan has nothing to say about it", v.Name)
		}
		if excluded && strings.TrimSpace(reason) == "" {
			t.Errorf("verb %q is excluded from the source scan with no reason", v.Name)
		}
	}
	declared := map[string]bool{}
	for _, v := range Verbs() {
		declared[v.Name] = true
	}
	for name := range staticAuthoringVerbs {
		if !declared[name] {
			t.Errorf("the source scan refuses %q, which the classification table does not declare", name)
		}
	}
	for name := range staticAuthoringExcluded {
		if !declared[name] {
			t.Errorf("the source scan excludes %q, which the classification table does not declare", name)
		}
	}
}

// TestArgvTakingCallsNamesEveryVariadicGitRunner is the source scan's own
// freshness check: a new function that takes a git argv as a variadic string
// tail must be named in argvTakingCalls, or the scan stops seeing the argv built
// through it.
//
// One direction only. The list may legitimately carry MORE than the scan finds
// -- main.runPassthrough takes a []string rather than a variadic tail, and
// gitexec.ArgvAny lives in the package the walk skips -- but it may never carry
// less.
func TestArgvTakingCallsNamesEveryVariadicGitRunner(t *testing.T) {
	root := repoRoot(t)
	// The packages that speak git argv. internal/testutil is not among them: it
	// drives raw git for fixtures and never reaches the boundary.
	dirs := []string{".", "internal/git", "internal/submodule"}
	fset := token.NewFileSet()
	found := 0

	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, filepath.FromSlash(dir), e.Name())
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
					continue
				}
				last := fn.Type.Params.List[len(fn.Type.Params.List)-1]
				ell, ok := last.Type.(*ast.Ellipsis)
				if !ok {
					continue
				}
				if id, ok := ell.Elt.(*ast.Ident); !ok || id.Name != "string" {
					continue
				}
				if len(last.Names) != 1 || last.Names[0].Name != "args" {
					continue
				}
				found++
				if argvTakingCalls[fn.Name.Name] == "" {
					t.Errorf("%s/%s: %s takes a variadic git argv but is not named in argvTakingCalls, so the source scan cannot see argv built through it",
						dir, e.Name(), fn.Name.Name)
				}
			}
		}
	}
	if found == 0 {
		t.Error("no variadic git runner was found at all; this check is measuring nothing")
	}
}

// TestBoundaryGuardExemptionsAreDeclared keeps the guard's own scope rule from
// growing silently: every exempt directory must exist and carry a reason.
func TestBoundaryGuardExemptionsAreDeclared(t *testing.T) {
	root := repoRoot(t)
	want := map[string]bool{"internal/gitexec": true, "internal/testutil": true}
	for dir, reason := range exemptDirs {
		if !want[dir] {
			t.Errorf("undeclared boundary-guard exemption %q; the exempt set is the boundary itself plus the test-only helper package", dir)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("boundary-guard exemption %q carries no reason", dir)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			t.Errorf("boundary-guard exemption %q names a directory that does not exist: %v", dir, err)
		}
	}
	for dir := range want {
		if _, ok := exemptDirs[dir]; !ok {
			t.Errorf("boundary-guard exemption %q is missing from the table", dir)
		}
	}
}
