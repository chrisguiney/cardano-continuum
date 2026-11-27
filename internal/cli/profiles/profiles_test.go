package profiles

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// The index maps each shipped fitted profile's hash to its target, so
// grade.py can pick the target from a run's header. Regenerate with
// `go run ./cmd/continuum profiles index --out profiles/index.json`.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestProfileIndexIsCurrent(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"index"}, &out); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../profiles/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(out.Bytes()), bytes.TrimSpace(want)) {
		t.Fatalf("profiles/index.json is stale; regenerate it\nwant:\n%s", out.String())
	}
	var idx map[string]struct{ Profile, Target string }
	if err := json.Unmarshal(want, &idx); err != nil || len(idx) < 2 {
		t.Fatalf("index: %v (%d entries)", err, len(idx))
	}
	for h, e := range idx {
		if b, err := hex.DecodeString(h); err != nil || len(b) != 32 {
			t.Errorf("bad hash %q", h)
		}
		if _, err := os.Stat(filepath.Join("../../../profiles", e.Target)); err != nil {
			t.Errorf("%s: %v", e.Profile, err)
		}
	}
}
