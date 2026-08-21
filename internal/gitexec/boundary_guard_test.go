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
// Three shapes are refused outside the boundary package:
//
//   - exec.Command / exec.CommandContext naming the git binary, which is a
//     subprocess built without the argv prefix, the environment assembly or the
//     context-carried overrides;
//   - a []interface{}{"git", ...} argv literal, which is the same bypass in the
//     shape the strictcli effects handle takes;
//   - the "--no-optional-locks" string, which is the boundary's own prefix and
//     appears anywhere else only because someone rebuilt the argv by hand.
//
// Scope rule: _test.go files anywhere and the whole internal/testutil package
// are exempt. Tests exercise git directly by design -- that is how a test builds
// the fixture the production code is then measured against. Production packages
// are not exempt, and neither is a non-test file that happens to sit in
// internal/test.

// exemptDirs are directories whose Go source the guard does not scan, each with
// the reason.
var exemptDirs = map[string]string{
	"internal/gitexec":  "the boundary itself: this is the one package allowed to build a git subprocess",
	"internal/testutil": "test-only helpers; tests drive git directly to build fixtures",
}

// skipDirs are directories with no safegit source to scan.
var skipDirs = map[string]bool{
	".git": true, "testdata": true, "vendor": true, "node_modules": true,
	"docs": true, "scripts": true, "todo": true,
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
			if skipDirs[d.Name()] {
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
		t.Errorf("%s: %s -- every git subprocess must be built by internal/gitexec.Command, and every git argv by internal/gitexec.ArgvAny", v.pos, v.what)
	}
}

// scanFile reports the three refused shapes in one file.
func scanFile(fset *token.FileSet, file *ast.File, rel string) []violation {
	var out []violation
	at := func(p token.Pos) string {
		pos := fset.Position(p)
		return rel + ":" + strconv.Itoa(pos.Line)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
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
				out = append(out, violation{at(node.Pos()), "exec." + sel.Sel.Name + " of the git binary outside the execution boundary"})
			}
		case *ast.CompositeLit:
			if !isInterfaceSlice(node.Type) || len(node.Elts) == 0 {
				return true
			}
			if s, ok := stringLit(node.Elts[0]); ok && s == Binary {
				out = append(out, violation{at(node.Pos()), `a []interface{}{"git", ...} argv literal outside the execution boundary`})
			}
		case *ast.BasicLit:
			if s, ok := stringLit(node); ok && s == "--no-optional-locks" {
				out = append(out, violation{at(node.Pos()), `the "--no-optional-locks" prefix spelled outside the execution boundary`})
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
