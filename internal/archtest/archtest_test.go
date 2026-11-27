package archtest

import (
	"slices"
	"strings"
	"testing"
)

func TestViolations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		g     graph
		wantN int
	}{
		{"leaf imports an internal package", graph{"internal/ident": {"internal/check"}}, 1},
		{"leaf imports only the standard library", graph{"internal/check": nil}, 0},
		{"internal package imports cmd", graph{"internal/engine": {"cmd/continuum"}}, 1},
		{"cmd package imports internal", graph{"cmd/continuum": {"internal/engine"}}, 0},
		{"api package imports internal", graph{"api/v1alpha1": {"internal/ident"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := violations(tt.g)
			if len(got) != tt.wantN {
				t.Fatalf("violations = %q, want %d", got, tt.wantN)
			}
		})
	}
}

// TestRepositoryBoundaries applies the rules to the real tree. A failure here
// names the package and the import that breaks a boundary.
func TestRepositoryBoundaries(t *testing.T) {
	t.Parallel()
	g, err := load("../..", true)
	if err != nil {
		t.Fatal(err)
	}
	if v := violations(g); len(v) > 0 {
		t.Fatalf("boundary violations:\n%s", strings.Join(v, "\n"))
	}
}

func TestSeparations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		g    graph
		want []string
	}{
		{"direct import", graph{"cmd/continuum": {"web"}}, []string{"cmd/continuum reaches web: cmd/continuum -> web"}},
		{"through a chain", graph{"cmd/continuum": {"internal/cli/serve"}, "internal/cli/serve": {"internal/httpapi", "web"}},
			[]string{"cmd/continuum reaches web: cmd/continuum -> internal/cli/serve -> web"}},
		{"another binary may", graph{"cmd/continuum-ui": {"web"}, "cmd/continuum": {"internal/httpapi"}}, nil},
		{"import cycle", graph{"cmd/continuum": {"a"}, "a": {"b"}, "b": {"a"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := separations(tt.g); !slices.Equal(got, tt.want) {
				t.Fatalf("separations = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBinarySeparations applies the separations to the tree's non-test
// imports, which are what a binary links. It is what keeps the engine
// image's digest independent of the web UI.
func TestBinarySeparations(t *testing.T) {
	t.Parallel()
	g, err := load("../..", false)
	if err != nil {
		t.Fatal(err)
	}
	if v := separations(g); len(v) > 0 {
		t.Fatalf("separation violations:\n%s", strings.Join(v, "\n"))
	}
}
