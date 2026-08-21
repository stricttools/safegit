package exitcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// declaredConstants parses this package's own source and returns every
// top-level untyped int constant it declares, by name. Reading the source
// rather than a hand-kept list is what makes the completeness check real: a
// constant added without a row in All() has nowhere to hide.
func declaredConstants(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exitcode.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing exitcode.go: %v", err)
	}
	out := map[string]int{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				t.Fatalf("const spec is not one name = one value: %v", vs.Names)
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				t.Fatalf("const %s is not an integer literal", vs.Names[0].Name)
			}
			v, err := strconv.Atoi(lit.Value)
			if err != nil {
				t.Fatalf("const %s: %v", vs.Names[0].Name, err)
			}
			out[vs.Names[0].Name] = v
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no constants from exitcode.go")
	}
	return out
}

// TestAllCoversEveryDeclaredConstant is the standing rule's enforcement: a code
// added to the registry without a row in All() (or a row naming a constant that
// does not exist) fails here, which in turn keeps the generated documentation
// table complete.
func TestAllCoversEveryDeclaredConstant(t *testing.T) {
	declared := declaredConstants(t)
	registered := map[string]int{}
	for _, e := range All() {
		if _, dup := registered[e.Const]; dup {
			t.Errorf("All() lists %s twice", e.Const)
		}
		registered[e.Const] = e.Code
	}

	for name, value := range declared {
		regValue, ok := registered[name]
		if !ok {
			t.Errorf("constant %s (= %d) is declared but has no row in All(); every exit code must be registered", name, value)
			continue
		}
		if regValue != value {
			t.Errorf("All() row %s carries code %d, but the constant is %d", name, regValue, value)
		}
	}
	for name := range registered {
		if _, ok := declared[name]; !ok {
			t.Errorf("All() lists %s, which is not a constant declared in exitcode.go", name)
		}
	}
}

func TestCodesAreUniqueAndAscending(t *testing.T) {
	seen := map[int]string{}
	prev := -1
	for _, e := range All() {
		if other, dup := seen[e.Code]; dup {
			t.Errorf("code %d is registered twice: %s and %s", e.Code, other, e.Const)
		}
		seen[e.Code] = e.Const
		if e.Code <= prev {
			t.Errorf("All() is not ascending: %s (%d) follows %d", e.Const, e.Code, prev)
		}
		prev = e.Code
	}
}

func TestEveryEntryHasMeaning(t *testing.T) {
	for _, e := range All() {
		if e.Meaning == "" {
			t.Errorf("%s has no meaning text; the documentation table is generated from it", e.Const)
		}
	}
}

func TestDefined(t *testing.T) {
	if !Defined(LockTimeout) {
		t.Error("Defined(LockTimeout) = false")
	}
	// 99 is deliberately unregistered: Defined must not answer yes to a code
	// nothing produces.
	if Defined(99) {
		t.Error("Defined(99) = true, but 99 is not registered")
	}
}

func TestMarkdownTableRendersEveryEntry(t *testing.T) {
	table := MarkdownTable()
	for _, e := range All() {
		row := "| " + strconv.Itoa(e.Code) + " | " + e.Meaning + " |"
		if !strings.Contains(table, row) {
			t.Errorf("MarkdownTable() is missing the row for %s:\n%s", e.Const, row)
		}
	}
}
