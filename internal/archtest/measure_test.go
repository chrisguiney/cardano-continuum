package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write lays out a small module tree under a temp dir.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func has(fs []finding, key string) (finding, bool) {
	for _, f := range fs {
		if f.key == key {
			return f, true
		}
	}
	return finding{}, false
}

func TestFunctionLimits(t *testing.T) {
	long := "package p\n\nfunc long() {\n" + strings.Repeat("\t_ = 1\n", 80) + "}\n"
	deep := `package p

func deep(x int) {
	if x > 0 {
		for range 3 {
			switch x {
			case 1:
				_ = func() {}
			}
		}
	}
}

func flat(x int) {
	if x > 0 {
		_ = x
	}
}
`
	root := write(t, map[string]string{"p/long.go": long, "p/deep.go": deep})
	m, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := limits(m)
	if f, ok := has(got, "func-length p long"); !ok || f.n != 82 {
		t.Fatalf("func-length p long = %+v %v, want 82", f, ok)
	}
	if f, ok := has(got, "nesting p deep"); !ok || f.n != 4 {
		t.Fatalf("nesting p deep = %+v %v, want 4", f, ok)
	}
	if _, ok := has(got, "nesting p flat"); ok {
		t.Fatal("flat must not be flagged")
	}
}

func TestTypeLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\ntype Big struct {\n")
	for i := range 15 {
		b.WriteString("\tf" + strings.Repeat("x", i+1) + " int\n")
	}
	b.WriteString("}\n")
	for i := range 6 {
		b.WriteString("\nfunc (Big) M" + strings.Repeat("x", i+1) + "() {}\n")
	}
	root := write(t, map[string]string{
		"p/big.go":                   b.String(),
		"p/other.go":                 "package p\n\nfunc (*Big) Away() {}\n",
		"p/zz_generated.deepcopy.go": "package p\n\nfunc (*Big) DeepCopy() {}\n",
	})
	m, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := limits(m)
	if f, ok := has(got, "members p Big"); !ok || f.n != 23 {
		t.Fatalf("members p Big = %+v %v, want 23", f, ok)
	}
	if f, ok := has(got, "stray-methods p Big"); !ok || f.n != 1 {
		t.Fatalf("stray-methods p Big = %+v %v, want 1 (generated exempt)", f, ok)
	}
}

func TestFileLimit(t *testing.T) {
	files := map[string]string{}
	for i := range 13 {
		files["p/f"+strings.Repeat("a", i+1)+".go"] = "package p\n"
		files["p/f"+strings.Repeat("a", i+1)+"_test.go"] = "package p\n"
	}
	root := write(t, files)
	m, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := has(limits(m), "files p"); !ok || f.n != 26 {
		t.Fatalf("files p = %+v %v, want 26 counting tests", f, ok)
	}
}

func TestCompare(t *testing.T) {
	found := []finding{{key: "func-length p a", n: 90}, {key: "nesting p b", n: 4}}
	allowed := map[string]int{"func-length p a": 85, "members p gone": 30, "nesting p b": 5}
	fresh, stale := compare(found, allowed)
	if len(fresh) != 1 || !strings.Contains(fresh[0], "func-length p a 90") {
		t.Fatalf("fresh = %q, want the grown function", fresh)
	}
	if len(stale) != 1 || !strings.Contains(stale[0], "members p gone") {
		t.Fatalf("stale = %q, want the vanished type", stale)
	}
}

func TestParseAllowance(t *testing.T) {
	got, err := parseAllowance("# note\n\nfunc-length internal/a Restore 90\nfiles internal/b 48\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["func-length internal/a Restore"] != 90 || got["files internal/b"] != 48 || len(got) != 2 {
		t.Fatalf("parsed = %v", got)
	}
	if _, err := parseAllowance("nonsense\n"); err == nil {
		t.Fatal("a line without a number must be rejected")
	}
}

func TestComponentImports(t *testing.T) {
	const c = "internal/engine/internal/"
	tests := []struct {
		name  string
		g     graph
		wantN int
	}{
		{"component imports a sibling", graph{c + "pay": {c + "clock"}}, 1},
		{"component imports wire", graph{c + "pay": {c + "wire"}}, 0},
		{"component imports its assembler", graph{c + "pay": {"internal/engine"}}, 1},
		{"component imports an ordinary package", graph{c + "pay": {"internal/plan"}}, 0},
		{"assembler imports a component", graph{"internal/engine": {c + "pay", c + "clock"}}, 0},
		{"component imports itself", graph{c + "pay": {c + "pay"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := violations(tt.g); len(got) != tt.wantN {
				t.Fatalf("violations = %q, want %d", got, tt.wantN)
			}
		})
	}
}

// TestRepositoryLimits fails on a limit break the allowance does not cover,
// and on an allowance entry whose violation is gone. The allowance only
// shrinks: delete a line when this reports it stale.
func TestRepositoryLimits(t *testing.T) {
	m, err := scan("../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("allowance.txt")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := parseAllowance(string(src))
	if err != nil {
		t.Fatal(err)
	}
	fresh, stale := compare(limits(m), allowed)
	if len(fresh) > 0 {
		t.Errorf("new limit violations (fix them; the allowance takes no additions):\n%s", strings.Join(fresh, "\n"))
	}
	if len(stale) > 0 {
		t.Errorf("allowance entries no longer needed (delete them):\n%s", strings.Join(stale, "\n"))
	}
}
