package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/cli/sim"
)

// TestServeCommand: continuum-ui serve is continuum serve's API over a log
// with the web UI beside it, for viewing a recorded run.
func TestServeCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out strings.Builder
	if err := sim.Run(t.Context(), []string{"--fake", "--epochs", "1", "--out", dir, "--wallets", "20", "--epoch-slots", "60"}, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}

	s, stop := start(t, Serve, "--log", filepath.Join(dir, "events.log"), "--epoch-slots", "60")
	defer stop()

	requireSPA(t, s)
	if r, body := s.fetch(t, "/api/capabilities"); r.StatusCode != 200 || !strings.Contains(body, `"controls":false`) {
		t.Errorf("capabilities: %d %q", r.StatusCode, body)
	}
}
