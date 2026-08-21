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
// Two call shapes are refused in production source: die(N, ...) and
// os.Exit(N), where N is an integer literal. Both arguments are exit codes and
// nothing else, so a literal there is always a code that some future reader
// has to look up in the source instead of in the registry.
//
// What this guard deliberately does NOT cover, and why: a handler's
// `return N`. In package main an int return is usually an exit code, but not
// always -- a helper that counts stale locks returns an int too, and the AST
// cannot tell the two apart without knowing what the function means. Refusing
// every literal int return would either misfire on counting helpers or need a
// hand-kept exemption list, which is a second registry by another name. The
// full sweep across all four site shapes is `scripts/exit-inventory`, run by a
// human who can read the enclosing function name.
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
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "die" {
					name = "die"
				}
			case *ast.SelectorExpr:
				pkg, isIdent := fun.X.(*ast.Ident)
				if isIdent && pkg.Name == "os" && fun.Sel.Name == "Exit" {
					name = "os.Exit"
				}
			}
			if name == "" {
				return true
			}
			lit, isLit := call.Args[0].(*ast.BasicLit)
			if !isLit || lit.Kind != token.INT {
				return true
			}
			code, convErr := strconv.Atoi(lit.Value)
			if convErr != nil {
				return true
			}
			found = append(found, violation{
				pos:  fset.Position(call.Lparen).String(),
				call: name,
				code: code,
			})
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
		t.Errorf("%s: %s(%d, ...) uses a bare exit-code literal -- name it in internal/exitcode and use the constant",
			v.pos, v.call, v.code)
	}
	t.Logf("scanned %d production files for bare exit-code literals", scanned)
}
