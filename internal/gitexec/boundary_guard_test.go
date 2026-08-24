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
