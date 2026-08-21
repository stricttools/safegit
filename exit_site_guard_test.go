package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is the structural guard behind the claim that internal/exitcode is
// safegit's single exit-code registry. Prose cannot hold that line; a
// mechanical scan of the source can.
//
// Four shapes are refused in production source, all of them positions where an
// integer can only ever be an exit code:
//
//   - die(N, ...)             -- safegit's own fatal exit
//   - os.Exit(N)              -- the runtime's
//   - strictcli.Exit(N)       -- a handler's outcome, which the framework exits with
//   - Code: N                 -- a composite literal field, which is how
//     commit.CommitError carries a code up to main
//
// A literal in any of them is a code that some future reader has to look up in
// the source instead of in the registry.
//
// What this guard deliberately does NOT cover, and why: a handler's
// `return N`. In package main an int return is usually an exit code, but not
// always -- a helper that counts stale locks returns an int too, and the AST
// cannot tell the two apart without knowing what the function means. Refusing
// every literal int return would either misfire on counting helpers or need a
// hand-kept exemption list, which is a second registry by another name. The
// full sweep across all site shapes is `scripts/exit-inventory`, run by a
// human who can read the enclosing function name.
//
// The `Code:` field name is matched syntactically, so a future unrelated struct
// with a `Code int` field would be policed too. That is the intended direction
// of error: the guard would demand a named constant for something that is not
// an exit code, which a reader notices immediately, rather than silently
// letting an exit code through.
//
// Scope rule: _test.go files are exempt. A test asserts codes rather than
// producing them, and the separate registry sweep in exit_table_test.go is
// what polices those assertions.

func TestNoProductionExitSiteUsesABareLiteral(t *testing.T) {
	type violation struct {
		pos  string
		call string
		code int
	}
	var found []violation
	scanned := 0

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "docs", "scripts":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		scanned++
		// intLiteral reports the value of an integer-literal expression.
		intLiteral := func(e ast.Expr) (int, bool) {
			lit, isLit := e.(*ast.BasicLit)
			if !isLit || lit.Kind != token.INT {
				return 0, false
			}
			v, convErr := strconv.Atoi(lit.Value)
			if convErr != nil {
				return 0, false
			}
			return v, true
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if len(node.Args) == 0 {
					return true
				}
				name := ""
				switch fun := node.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "die" {
						name = "die"
					}
				case *ast.SelectorExpr:
					pkg, isIdent := fun.X.(*ast.Ident)
					if !isIdent {
						break
					}
					switch {
					case pkg.Name == "os" && fun.Sel.Name == "Exit":
						name = "os.Exit"
					case pkg.Name == "strictcli" && fun.Sel.Name == "Exit":
						name = "strictcli.Exit"
					}
				}
				if name == "" {
					return true
				}
				code, isInt := intLiteral(node.Args[0])
				if !isInt {
					return true
				}
				found = append(found, violation{
					pos:  fset.Position(node.Lparen).String(),
					call: name,
					code: code,
				})
			case *ast.KeyValueExpr:
				// A `Code: N` field in a composite literal. commit.CommitError
				// is the one type that carries an exit code this way.
				key, isIdent := node.Key.(*ast.Ident)
				if !isIdent || key.Name != "Code" {
					return true
				}
				code, isInt := intLiteral(node.Value)
				if !isInt {
					return true
				}
				found = append(found, violation{
					pos:  fset.Position(node.Colon).String(),
					call: "Code:",
					code: code,
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if scanned == 0 {
		t.Fatal("the guard scanned no production files, so it proved nothing")
	}

	for _, v := range found {
		site := v.call + "(" + strconv.Itoa(v.code) + ", ...)"
		if v.call == "Code:" {
			site = "Code: " + strconv.Itoa(v.code)
		}
		t.Errorf("%s: %s uses a bare exit-code literal -- name it in internal/exitcode and use the constant",
			v.pos, site)
	}
	t.Logf("scanned %d production files for bare exit-code literals", scanned)
}
