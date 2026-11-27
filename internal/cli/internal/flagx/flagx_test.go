package flagx

import "testing"

func TestRepoOf(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"localhost:5001/continuum/dingo:dev-d1": "localhost:5001/continuum/dingo",
		"localhost:5001/continuum/dingo":        "localhost:5001/continuum/dingo",
		"ghcr.io/x/dingo:main":                  "ghcr.io/x/dingo",
	} {
		if got := RepoOf(in); got != want {
			t.Errorf("RepoOf(%q) = %q, want %q", in, got, want)
		}
	}
}
