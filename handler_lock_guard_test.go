package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestEveryHandlerReleasesLocksWhenItEnds: a command ends early through
// strictcli.ExitNow, which unwinds the handler's stack, so a lock is released
// by the deferred Release of the function that took it. The backstop for a lock
// no deferred Release covers is releasingLocks, which every registered handler
// is wrapped in: it releases whatever the dispatch still holds once the
// handler's own deferred cleanup has run.
//
// The scan is syntactic: every Command registration in the package's
// production files must pass its handler as a direct call to releasingLocks,
// and every Passthrough registration as a direct call to
// releasingPassthroughLocks.
func TestEveryHandlerReleasesLocksWhenItEnds(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	registrations := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "Passthrough") || len(call.Args) < 3 {
				return true
			}
			// gitexec.Command builds a git subprocess, not a registration.
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "gitexec" {
				return true
			}
			registrations++
			want := "releasingLocks"
			if sel.Sel.Name == "Passthrough" {
				want = "releasingPassthroughLocks"
			}
			inner, ok := call.Args[2].(*ast.CallExpr)
			if ok {
				if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == want {
					return true
				}
			}
			t.Errorf("%s: the handler of this %s registration is not wrapped in %s",
				fset.Position(call.Lparen), sel.Sel.Name, want)
			return true
		})
	}
	if registrations == 0 {
		t.Fatal("found no command registrations, so the guard proved nothing")
	}
}
