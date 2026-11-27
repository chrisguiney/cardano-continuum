package oracle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// Kinds must list every Kind constant in finding.go, so a new kind cannot
// be left out of what the UI offers.
func TestKindsListsEveryConstant(t *testing.T) {
	t.Parallel()
	f, err := parser.ParseFile(token.NewFileSet(), "finding.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			v := s.(*ast.ValueSpec)
			if id, ok := v.Type.(*ast.Ident); ok && id.Name == "Kind" {
				for _, val := range v.Values {
					want = append(want, val.(*ast.BasicLit).Value[1:len(val.(*ast.BasicLit).Value)-1])
				}
			}
		}
	}
	var got []string
	for _, k := range Kinds() {
		got = append(got, string(k))
	}
	slices.Sort(want)
	slices.Sort(got)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("Kinds() = %v, constants = %v", got, want)
	}
}

func TestSeveritiesLeastFirst(t *testing.T) {
	t.Parallel()
	got := Severities()
	if len(got) != 3 || got[0].String() != "warn" || got[2].String() != "fatal" {
		t.Fatalf("Severities() = %v", got)
	}
}
