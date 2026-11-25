//go:build smoke

package genesis

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSmokeForges boots one cardano-node per genesis pool in Docker on a
// rendered devnet and requires the chain to reach epoch 2 in Conway with
// blocks from every pool. It needs Docker and takes about eight minutes:
//
//	go test -tags smoke -run TestSmokeForges -v -timeout 15m ./internal/genesis
//
// CONTINUUM_SMOKE_NODE_IMAGE overrides the node image.
func TestSmokeForges(t *testing.T) {
	image := os.Getenv("CONTINUUM_SMOKE_NODE_IMAGE")
	if image == "" {
		image = "ghcr.io/blinklabs-io/cardano-node:11.1.3-1"
	}
	const prefix = "continuum-genesis-"
	s := Default()
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25 // k/f = 40, so 200-slot epochs pass Check
	s.EpochLength = 200
	s.PoolHost = prefix + "pool-%d"
	s.SystemStart = time.Now().Add(30 * time.Second).Truncate(time.Second).UTC()

	b := render(t, s, 1)
	dir := t.TempDir()
	if _, err := WriteDir(dir, b); err != nil {
		t.Fatal(err)
	}
	network := prefix + "net"
	docker(t, "network", "create", network)
	t.Cleanup(func() { _, _ = exec.Command("docker", "network", "rm", network).CombinedOutput() })
	for i := range s.Pools {
		name := fmt.Sprintf(s.PoolHost, i)
		t.Cleanup(func() {
			if t.Failed() {
				out, _ := exec.Command("docker", "logs", "--tail", "40", name).CombinedOutput()
				t.Logf("%s logs:\n%s", name, out)
			}
			_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
		})
		docker(t, "run", "-d", "--name", name, "--hostname", name, "--network", network,
			"-v", filepath.Join(dir, "config")+":/config:ro",
			"-v", filepath.Join(dir, "pools", strconv.Itoa(i))+":/keys:ro",
			"--entrypoint", "cardano-node", image, "run",
			"--config", "/config/config.json", "--topology", "/keys/topology.json",
			"--database-path", "/data/db", "--socket-path", "/data/node.socket",
			"--host-addr", "0.0.0.0", "--port", strconv.Itoa(s.PoolPort),
			"--shelley-kes-key", "/keys/kes.skey", "--shelley-vrf-key", "/keys/vrf.skey",
			"--shelley-operational-certificate", "/keys/opcert.cert")
	}

	twoEpochs := time.Duration(2*s.EpochLength) * s.SlotLength
	deadline := s.SystemStart.Add(twoEpochs + 3*time.Minute)
	var tips []tip
	for {
		tips = queryTips(t, s)
		if len(tips) == s.Pools && allPast(tips, 2) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no node reached epoch 2 by %s: %+v", deadline.Format(time.TimeOnly), tips)
		}
		time.Sleep(10 * time.Second)
	}
	for i, tp := range tips {
		t.Logf("pool %d tip: epoch %d slot %d block %d era %s", i, tp.Epoch, tp.Slot, tp.Block, tp.Era)
		if tp.Era != "Conway" {
			t.Errorf("pool %d era = %s, want Conway", i, tp.Era)
		}
	}
	for i := range s.Pools {
		name := fmt.Sprintf(s.PoolHost, i)
		out, err := exec.Command("docker", "logs", name).CombinedOutput()
		if err != nil {
			t.Fatalf("logs %s: %v", name, err)
		}
		n := strings.Count(string(out), "ForgedBlock")
		t.Logf("pool %d forged-block log lines: %d", i, n)
		if n == 0 {
			t.Errorf("pool %d forged nothing", i)
		}
	}
}

type tip struct {
	Epoch int    `json:"epoch"`
	Slot  int    `json:"slot"`
	Block int    `json:"block"`
	Era   string `json:"era"`
}

func allPast(tips []tip, epoch int) bool {
	for _, tp := range tips {
		if tp.Epoch < epoch {
			return false
		}
	}
	return true
}

// queryTips returns the tip of every node that answers; a node still
// starting up is left out.
func queryTips(t *testing.T, s Spec) []tip {
	t.Helper()
	var out []tip
	for i := range s.Pools {
		raw, err := exec.Command("docker", "exec", fmt.Sprintf(s.PoolHost, i),
			"cardano-cli", "query", "tip", "--testnet-magic", strconv.Itoa(int(s.Magic)),
			"--socket-path", "/data/node.socket").Output()
		if err != nil {
			continue
		}
		var tp tip
		if err := json.Unmarshal(raw, &tp); err != nil {
			t.Fatalf("parse tip %q: %v", raw, err)
		}
		out = append(out, tp)
	}
	return out
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
}
