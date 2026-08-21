//go:build ignore

// exit-inventory enumerates every process-exit site in safegit's production Go
// sources so the exit-code registry (internal/exitcode) can be derived from
// what the code actually does rather than from memory.
//
// Run it through the wrapper:
//
//	scripts/exit-inventory            # human table, grouped by code
//	scripts/exit-inventory --tsv      # one tab-separated row per site
//
// Five site kinds are recognized, by AST shape rather than by regex:
//
//	die(N, ...)              safegit's own fatal helper (main.go)
//	os.Exit(N)               a direct process exit
//	strictcli.Exit(N)        a handler's outcome, which the framework exits with
//	return N                 a nonzero return from an int-returning function in
//	                         package main -- safegit's handlers and their run*
//	                         helpers propagate exit codes this way
//	err.Code                 a Code: field in a composite literal, which is how
//	                         internal/commit's CommitError carries an exit code
//	                         out to the caller that os.Exit()s it
//
// A site whose code is not an integer literal (a variable, a call, a named
// constant that is not resolved here) is reported with code -1 and the
// expression text, so the review pass sees it instead of losing it. Each row
// names its enclosing function, because the `return` kind necessarily
// over-reports: an int-returning helper in package main that computes a count
// rather than an exit code looks identical to a handler from the AST. Reading
// the enclosing function name is how the review pass separates them.
//
// Test files are excluded: they assert exit codes, they do not define them.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type site struct {
	File string
	Line int
	Kind string
	Func string // enclosing function, so helpers are distinguishable from handlers
	Code int    // -1 when the code is not an integer literal
	Expr string // the raw code expression, for non-literal sites
	Text string // the source line, trimmed
}

func main() {
	tsv := flag.Bool("tsv", false, "emit one tab-separated row per exit site instead of the grouped table")
	root := flag.String("root", ".", "repository root to scan")
	flag.Parse()

	var sites []site
	err := filepath.WalkDir(*root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "testdata" || name == "scripts" || name == "docs" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, err := scanFile(path)
		if err != nil {
			return err
		}
		sites = append(sites, found...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Code != sites[j].Code {
			return sites[i].Code < sites[j].Code
		}
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})

	if *tsv {
		for _, s := range sites {
			fmt.Printf("%d\t%s\t%s:%d\t%s\t%s\n", s.Code, s.Kind, s.File, s.Line, s.Func, s.Text)
		}
		return
	}

	byCode := map[int][]site{}
	for _, s := range sites {
		byCode[s.Code] = append(byCode[s.Code], s)
	}
	codes := make([]int, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	total := 0
	for _, c := range codes {
		group := byCode[c]
		label := strconv.Itoa(c)
		if c < 0 {
			label = "non-literal"
		}
		fmt.Printf("== exit %s (%d sites)\n", label, len(group))
		for _, s := range group {
			if s.Code < 0 {
				fmt.Printf("   %s:%d  %s in %s  code=%s  | %s\n", s.File, s.Line, s.Kind, s.Func, s.Expr, s.Text)
			} else {
				fmt.Printf("   %s:%d  %s in %s  | %s\n", s.File, s.Line, s.Kind, s.Func, s.Text)
			}
		}
		total += len(group)
	}
	fmt.Printf("\ntotal exit sites: %d across %d distinct codes\n", total, len(codes))
}

func scanFile(path string) ([]site, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	lineText := func(n int) string {
		if n-1 < len(lines) {
			return strings.TrimSpace(lines[n-1])
		}
		return ""
	}

	var out []site
	walk(f, fset, filepath.ToSlash(path), src, lineText, &out)
	return out, nil
}

// walk performs the attribution walk with an explicitly maintained stack of
// enclosing function result types.
func walk(f *ast.File, fset *token.FileSet, path string, src []byte, lineText func(int) string, out *[]site) {
	// Only package main returns exit codes as ints; the internal packages
	// return errors, and their int-returning helpers are counts, not codes.
	mainPkg := f.Name.Name == "main"

	type frame struct {
		intReturning bool
		name         string
	}
	var stack []frame
	enclosing := func() string {
		if len(stack) == 0 {
			return "(file scope)"
		}
		return stack[len(stack)-1].name
	}

	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		if n == nil {
			return
		}
		pushed := false
		switch node := n.(type) {
		case *ast.FuncDecl:
			stack = append(stack, frame{singleIntResult(node.Type), node.Name.Name})
			pushed = true
		case *ast.FuncLit:
			name := "func literal"
			if len(stack) > 0 {
				name = stack[len(stack)-1].name + ".func"
			}
			stack = append(stack, frame{singleIntResult(node.Type), name})
			pushed = true
		case *ast.CallExpr:
			if kind := callKind(node.Fun); kind != "" && len(node.Args) > 0 {
				code, expr, literal := intArg(node.Args[0], src, fset)
				if !(kind == "strictcli.Exit" && literal && code == 0) {
					pos := fset.Position(node.Lparen)
					s := site{File: path, Line: pos.Line, Kind: kind, Func: enclosing(), Text: lineText(pos.Line)}
					if literal {
						s.Code = code
					} else {
						s.Code = -1
						s.Expr = expr
					}
					*out = append(*out, s)
				}
			}
		case *ast.ReturnStmt:
			if mainPkg && len(stack) > 0 && stack[len(stack)-1].intReturning && len(node.Results) == 1 {
				code, expr, literal := intArg(node.Results[0], src, fset)
				if !literal || code != 0 {
					pos := fset.Position(node.Return)
					s := site{File: path, Line: pos.Line, Kind: "return", Func: enclosing(), Code: code, Text: lineText(pos.Line)}
					if !literal {
						s.Expr = expr
					}
					*out = append(*out, s)
				}
			}
		case *ast.KeyValueExpr:
			// A structured error carrying an exit code (commit.CommitError's
			// Code field) is an exit site too: the value reaches os.Exit
			// unchanged through the commit pipeline's error handling.
			if id, ok := node.Key.(*ast.Ident); ok && id.Name == "Code" {
				code, expr, literal := intArg(node.Value, src, fset)
				pos := fset.Position(node.Colon)
				s := site{File: path, Line: pos.Line, Kind: "err.Code", Func: enclosing(), Code: code, Text: lineText(pos.Line)}
				if !literal {
					s.Expr = expr
				}
				*out = append(*out, s)
			}
		}
		ast.Inspect(n, func(c ast.Node) bool {
			if c == n {
				return true
			}
			visit(c)
			return false
		})
		if pushed {
			stack = stack[:len(stack)-1]
		}
	}
	visit(f)
}

func singleIntResult(t *ast.FuncType) bool {
	if t.Results == nil || len(t.Results.List) != 1 {
		return false
	}
	if len(t.Results.List[0].Names) > 1 {
		return false
	}
	id, ok := t.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "int"
}

// callKind names the exit-site call shapes; "" for everything else.
func callKind(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		if e.Name == "die" {
			return "die"
		}
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return ""
		}
		switch {
		case pkg.Name == "os" && e.Sel.Name == "Exit":
			return "os.Exit"
		case pkg.Name == "strictcli" && e.Sel.Name == "Exit":
			return "strictcli.Exit"
		}
	}
	return ""
}

// intArg reports the integer value of an expression when it is an integer
// literal; otherwise it returns the expression's source text.
func intArg(e ast.Expr, src []byte, fset *token.FileSet) (code int, expr string, literal bool) {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.INT {
		v, err := strconv.Atoi(lit.Value)
		if err == nil {
			return v, lit.Value, true
		}
	}
	start := fset.Position(e.Pos()).Offset
	end := fset.Position(e.End()).Offset
	if start >= 0 && end <= len(src) && start < end {
		return -1, string(src[start:end]), false
	}
	return -1, "?", false
}
