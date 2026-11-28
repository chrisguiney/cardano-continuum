package serve

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blinklabs-io/bursa"

	"flag"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
)

// The goldens pin a run with no hard fork: its hot-key authorisations and
// proposal would move every log position the goldens name.
var attackFlags = []string{"--seed", "7", "--wallets", "20", "--epoch-slots", "60", "--profiles", "m3", "--hard-fork", "none"}

// attackedRun makes a control run and an attack run (every tier-1 attack)
// on one seed and returns both output directories.
func attackedRun(t *testing.T) (control, attack string) {
	t.Helper()
	dir := t.TempDir()
	all := "dust-flood,utxo-contention,malformed-cbor,front-run,double-spend"
	for name, extra := range map[string][]string{"control": nil, "attack": {"--attack", all}} {
		var out bytes.Buffer
		args := append(append([]string{"run", "--fake", "--epochs", "2", "--out", filepath.Join(dir, name)}, attackFlags...), extra...)
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out.String())
		}
	}
	return filepath.Join(dir, "control"), filepath.Join(dir, "attack")
}

func rawGet(t *testing.T, s served, path string) []byte {
	t.Helper()
	resp, err := s.c.Get(s.base + path) //nolint:noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s: %d %v %s", path, resp.StatusCode, err, b)
	}
	return b
}

func goldenFile(t *testing.T, name string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(body), "", "  "); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("%s differs from golden (rerun with -update to inspect):\n%s", name, buf.String())
	}
}

// secrets are the private bytes of actor ids 1..n and the root: none may
// appear in any response.
func secrets(n int) [][]byte {
	ent := engine.SeedFromInt(7).Derive("keys").Derive("keys/v1", "root")
	root := bursa.GetRootKey(ent[:], nil)
	out := [][]byte{ent[:], root}
	for id := 1; id <= n; id++ {
		acct, err := bursa.GetAccountKey(root, uint32(id))
		if err != nil {
			panic(err)
		}
		pay, _ := bursa.GetPaymentKey(acct, 0)
		stake, _ := bursa.GetStakeKey(acct, 0)
		out = append(out, pay, pay[:32], stake, stake[:32])
	}
	return out
}

// Not parallel: rewrites its golden files under -update.
func TestServeAdversariesAndActors(t *testing.T) {
	control, attack := attackedRun(t)
	srv, stop := startServe(t, "--log", filepath.Join(attack, "events.log"), "--store", filepath.Join(attack, "store.db"),
		"--control", filepath.Join(control, "events.log"), "--epoch-slots", "60")
	defer stop()

	adv := rawGet(t, srv, "/api/adversaries")
	goldenFile(t, "adversaries.golden.json", adv)
	var v struct {
		HasControl  bool `json:"has_control"`
		Adversaries []struct {
			Attack    string `json:"attack"`
			Attempted int    `json:"attempted"`
		} `json:"adversaries"`
	}
	if err := json.Unmarshal(adv, &v); err != nil || !v.HasControl || len(v.Adversaries) != 5 {
		t.Fatalf("adversaries: %v %s", err, adv)
	}
	seen := map[string]bool{}
	for _, a := range v.Adversaries {
		seen[a.Attack] = true
	}
	for _, n := range []string{"dust-flood", "utxo-contention", "malformed-cbor", "front-run", "double-spend"} {
		if !seen[n] {
			t.Errorf("no adversary for %s: %v", n, seen)
		}
	}

	list := rawGet(t, srv, "/api/actors?limit=500&sort=balance&dir=desc")
	var pg struct {
		Rows []struct {
			ID      uint64 `json:"id"`
			Address string `json:"address"`
			Balance uint64 `json:"balance"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(list, &pg); err != nil || len(pg.Rows) == 0 || pg.Rows[0].Address == "" || pg.Rows[0].Balance == 0 {
		t.Fatalf("list: %v %s", err, list)
	}
	bodies := [][]byte{adv, list}
	for _, r := range pg.Rows {
		bodies = append(bodies, rawGet(t, srv, "/api/actors/"+itoa(r.ID)))
	}
	d := bodies[2]
	goldenFile(t, "actor.golden.json", d)
	for _, b := range bodies {
		low := strings.ToLower(string(b))
		for _, s := range secrets(len(pg.Rows) + 10) {
			if strings.Contains(low, hex.EncodeToString(s)) {
				t.Fatalf("response holds secret bytes: %.200s", b)
			}
		}
	}
	resp, err := srv.c.Get(srv.base + "/api/actors?sort=password") //nolint:noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown sort: %d", resp.StatusCode)
	}
}

func itoa(n uint64) string { return jsonNum(n) }

func jsonNum(n uint64) string { b, _ := json.Marshal(n); return string(b) }

var update = flag.Bool("update", false, "rewrite testdata/*.golden")
