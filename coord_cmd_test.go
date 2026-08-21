package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// runGitMutation returns 0 on its --dry-run branch without reading the
// Completed's exit code, because in a dry run strictcli records the invocation
// instead of performing it and the carrier it hands back is unsettled -- asking
// it for an exit code panics. The framework exposes no settled-ness (Completed's
// field is unexported, its accessors panic rather than report, and
// Effects.Recorded() claims the would-do render as a side effect), so the guard
// has to key off safegit's own flag.
//
// That is correct only while safegit declares no app-level proc-observe
// allowlist. strictcli's Run takes an observe branch for any argv matching an
// allowlisted prefix, and that branch EXECUTES the child even in dry mode and
// returns a settled Completed. An allowlist admitting a prefix that a
// runGitMutation argv matches would therefore run git for real under --dry-run
// while runGitMutation reported 0 and threw git's own verdict away.
//
// This test pins the premise so the coupling cannot be broken silently: whoever
// declares an allowlist has to come back to runGitMutation's guard first.
func TestSafegitDeclaresNoProcObserveAllowlist(t *testing.T) {
	const option = "WithProcObserveAllowlist"

	var found []string
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

		// The AST is scanned rather than the bytes so that prose naming the
		// option -- runGitMutation's own comment does -- is not a violation.
		// Matching the identifier alone covers both spellings: the selector's
		// own Sel is an Ident, and so is a dot-imported bare call.
		ast.Inspect(f, func(n ast.Node) bool {
			if ident, isIdent := n.(*ast.Ident); isIdent && ident.Name == option {
				found = append(found, fset.Position(ident.Pos()).String())
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

	for _, pos := range found {
		t.Errorf("%s: %s is declared here -- runGitMutation's --dry-run branch (coord_cmd.go) returns 0 "+
			"without reading git's exit code and is only correct while no allowlisted prefix can match a "+
			"runGitMutation argv; rekey that guard or prove the prefixes are read-only before declaring one",
			pos, option)
	}
	t.Logf("scanned %d production files for a proc-observe allowlist declaration", scanned)
}
