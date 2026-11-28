package contract

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The datum and redeemer types are generated from the committed
// blueprint's definitions; rendering them again must give the committed
// file byte for byte.
func TestTypesMatchBlueprint(t *testing.T) {
	t.Parallel()
	got, err := RenderTypes(readBlueprint(t), "contracts/templates/plutus.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("types_gen.go drifts from contracts/templates/plutus.json: run make contracts")
	}
}

func TestRenderTypesRefusesUnknownShapes(t *testing.T) {
	t.Parallel()
	prim := map[string]Definition{"Int": {DataType: "integer"}, "Data": {Title: "Data"}}
	tests := []struct {
		name string
		defs map[string]Definition
		want string
	}{
		{"sum with fields", map[string]Definition{"continuum/types/S": {Title: "S", AnyOf: []Constructor{
			{Title: "A", Index: 0, Fields: []Field{{Title: "x", Ref: "#/definitions/Int"}}},
			{Title: "B", Index: 1},
		}}}, "neither a record nor an enum"},
		{"field of opaque data", map[string]Definition{"continuum/types/R": {Title: "R", AnyOf: []Constructor{
			{Title: "R", Index: 0, Fields: []Field{{Title: "x", Ref: "#/definitions/Data"}}},
		}}}, "not an integer"},
		{"constructor name clash", map[string]Definition{
			"continuum/types/E": {Title: "E", AnyOf: []Constructor{{Title: "On", Index: 0}, {Title: "Off", Index: 1}}},
			"continuum/types/F": {Title: "F", AnyOf: []Constructor{{Title: "On", Index: 0}, {Title: "Up", Index: 1}}},
		}, "is also one of"},
	}
	for _, tt := range tests {
		defs := map[string]Definition{}
		for k, v := range prim {
			defs[k] = v
		}
		for k, v := range tt.defs {
			defs[k] = v
		}
		_, err := RenderTypes(Blueprint{Definitions: defs}, "x")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"lock_until": "LockUntil", "depositor": "Depositor", "EscrowDatum": "EscrowDatum"} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"AddLiquidity": "add_liquidity", "Release": "release"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}
