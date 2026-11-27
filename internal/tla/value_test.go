package tla

import (
	"math/big"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want Value
	}{
		{"string", `"Client"`, "Client"},
		{"int", `42`, int64(42)},
		{"negative", `-1`, int64(-1)},
		{"bool", `true`, true},
		{"record", `{"a":1,"b":"x"}`, map[string]Value{"a": int64(1), "b": "x"}},
		{"sequence", `[1,2,3]`, []Value{int64(1), int64(2), int64(3)}},
		{"set of strings", `["a","b"]`, []Value{"a", "b"}},
		{"nested", `{"inbox":{"Client":[],"Server":[{"cookie":0}]}}`, map[string]Value{
			"inbox": map[string]Value{
				"Client": []Value{},
				"Server": []Value{map[string]Value{"cookie": int64(0)}},
			},
		}},
		{"empty record", `{}`, map[string]Value{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeValue(strings.NewReader(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDecodeValueBigInt(t *testing.T) {
	t.Parallel()
	got, err := DecodeValue(strings.NewReader(`123456789012345678901234567890`))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	b, ok := got.(*big.Int)
	if !ok || b.Cmp(want) != 0 {
		t.Fatalf("got %#v, want big int %s", got, want)
	}
}

func TestDecodeValueRejects(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat("[", maxDepth+1) + strings.Repeat("]", maxDepth+1)
	tests := map[string]string{
		"empty":     ``,
		"truncated": `{"a":`,
		"trailing":  `1 2`,
		"float":     `1.5`,
		"null":      `null`,
		"too deep":  deep,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeValue(strings.NewReader(in)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func FuzzDecodeValue(f *testing.F) {
	for _, s := range []string{`"x"`, `1`, `true`, `[1,"a",{"k":[]}]`, `{"a":{"b":[1,2]}}`, `12345678901234567890123`, `[`, `{"a"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := DecodeValue(strings.NewReader(string(b)))
		if err != nil {
			return
		}
		if v == nil {
			t.Fatal("nil value without error")
		}
	})
}
