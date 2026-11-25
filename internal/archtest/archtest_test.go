package archtest

import (
	"strings"
	"testing"
)

func TestViolations(t *testing.T) {
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
	g, err := load("../..")
	if err != nil {
		t.Fatal(err)
	}
	if v := violations(g); len(v) > 0 {
		t.Fatalf("boundary violations:\n%s", strings.Join(v, "\n"))
	}
}
