package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// DecisionKinds must list every Kind* constant in kinds.go.
func TestDecisionKindsListsEveryConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "kinds.go", nil, 0)
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
			if strings.HasPrefix(v.Names[0].Name, "Kind") {
				lit := v.Values[0].(*ast.BasicLit).Value
				want = append(want, lit[1:len(lit)-1])
			}
		}
	}
	got := slices.Clone(DecisionKinds())
	slices.Sort(want)
	slices.Sort(got)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("DecisionKinds() = %v, constants = %v", got, want)
	}
}
