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

// TestSessionHandshakeIsDeclaredOnce: the Claude Code session variable is a
// handshake the framework declares (WithHandshakeEnv), and its value reaches
// every package that records it -- the oplog, the commit trailer, the lock
// recovery record -- through the dispatch context. So its name is spelled in
// exactly one production string literal: the declaration every reader goes
// through. A second spelling is a raw environment read waiting to happen.
func TestSessionHandshakeIsDeclaredOnce(t *testing.T) {
	var sites []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "docs", "scripts", "experiments", "screenshots":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(v, "CLAUDE_CODE_SESSION_ID") {
				sites = append(sites, fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 {
		t.Errorf("CLAUDE_CODE_SESSION_ID is spelled in %d production string literals, want the one declaration: %v", len(sites), sites)
	}
}
