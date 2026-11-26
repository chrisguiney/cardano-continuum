package lab_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// TestFittedProfilesParse guards the contract between fit.py and
// internal/profile: every YAML file under example/ must parse and validate,
// including the clustered fit with amount distributions and weights. Regenerate with
// `UPDATE_EXAMPLE=1 make lab-test` when it fails.
func TestFittedProfilesParse(t *testing.T) {
	files, err := filepath.Glob("example/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example profiles: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			r, err := os.Open(f)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			ps, err := profile.ParseNamed(f, r)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(ps) == 0 || ps[0].Name == "" {
				t.Fatalf("got %d profiles", len(ps))
			}
		})
	}
}
