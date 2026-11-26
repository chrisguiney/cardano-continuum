package lab_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// TestFittedProfilesParse guards the contract between fit.py and
// internal/profile: every YAML file under example/ must parse and validate,
// including the clustered fit with amount distributions, weights and the
// non-wallet populations. Regenerate with
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
			file, err := profile.ParseFile(r)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			if len(file.Profiles) == 0 || file.Profiles[0].Name == "" {
				t.Fatalf("got %d profiles", len(file.Profiles))
			}
			if filepath.Base(f) == "clustered.yaml" && (len(file.Populations) != 3 || len(file.Network.Proposals) == 0) {
				t.Fatalf("clustered fit lost its populations: %+v %+v", file.Populations, file.Network)
			}
		})
	}
}
