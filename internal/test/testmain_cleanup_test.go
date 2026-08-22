package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain builds the safegit binary into a temporary directory and has to
// remove that directory before the process exits. It used to say so with
//
//	defer os.RemoveAll(tmpDir)
//	...
//	os.Exit(m.Run())
//
// which never removed anything: os.Exit runs no deferred function. Every single
// run of this package therefore leaked its built binary into $TMPDIR and left it
// there forever. On the machine where this was found, 1234 leaked
// safegit-test-bin-* directories held 7.5 GB against a 9.4 GB tmpfs quota on
// /tmp -- and once that quota is gone, git and safegit writes inside any test's
// temporary repository fail with EDQUOT at whatever point they happen to reach.
// That is the whole of the suite's two "spurious failures under load": tests
// that write into a filesystem their own harness had been filling up for months.
//
// The shape that fixes it is the shape this guard pins: a function that exits
// the process does nothing else, and every cleanup lives in a function that
// RETURNS, where a defer is honoured. The guard is written against the bug
// class rather than against TestMain by name, so the next function in this
// package that pairs a defer with a process exit is refused too.
func TestNoDeferredCleanupIsStrandedByAProcessExit(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this file")
	}
	dir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}

	exiting := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, isFn := decl.(*ast.FuncDecl)
				if !isFn || fn.Body == nil {
					continue
				}
				if !bodyCallsOsExit(fn.Body) {
					continue
				}
				exiting++
				inspectOwnBody(fn.Body, func(n ast.Node) {
					d, isDefer := n.(*ast.DeferStmt)
					if !isDefer {
						return
					}
					t.Errorf("%s: %s calls os.Exit and defers at %s -- os.Exit runs no deferred "+
						"function, so that cleanup never happens. Move the body into a function "+
						"that returns and exit on its result.",
						filepath.Base(path), fn.Name.Name, fset.Position(d.Pos()))
				})
			}
		}
	}

	if exiting == 0 {
		t.Fatal("no function calling os.Exit was found in this package, so this guard is " +
			"checking nothing; TestMain is expected to be one")
	}
}

// bodyCallsOsExit reports whether the body itself calls os.Exit. A call inside a
// nested function literal does not count: that literal is a different function,
// and its own defers run when it returns.
func bodyCallsOsExit(body *ast.BlockStmt) bool {
	found := false
	inspectOwnBody(body, func(n ast.Node) {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "Exit" {
			return
		}
		if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "os" {
			found = true
		}
	})
	return found
}

// inspectOwnBody visits every node of one function's own body, without
// descending into nested function literals.
func inspectOwnBody(body *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		visit(n)
		return true
	})
}
