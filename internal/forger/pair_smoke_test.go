//go:build smoke

package forger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// pairEpochs is how long the pair runs. The divergence below appeared in
// the first epoch of every run, so three leave room for the fork to pass k.
const pairEpochs = 3

// TestSmokeHonestPair runs one cardano-node pool and one dingo pool, half
// the stake each and peered directly, with no forger, for three epochs, and
// fails if their chains fork deeper than k.
//
// Known divergence: with dingo main at 700f386a (image digest
// sha256:0acb90df9d64b125a722a1f7c9f3fe21375b4411935e44c986aeaf673ffe7afb)
// this fails. After cardano-node switches to a fork, dingo applies the
// chain-sync roll-backward from its active peer to its own selected chain,
// dropping blocks it forged that are on cardano-node's new, longer chain; it
// then forges on the older tip and does not adopt cardano-node's chain again.
// In one run the fork was at slot 188 and 58 and 53 blocks deep after three
// epochs.
//
//	CONTINUUM_SMOKE_OUT=<dir> go test -tags smoke -run TestSmokeHonestPair -v -timeout 20m ./internal/forger
func TestSmokeHonestPair(t *testing.T) {
	out := os.Getenv("CONTINUUM_SMOKE_OUT")
	if out == "" {
		out = t.TempDir()
	}
	nodes := []smokeNode{
		{Name: smokePrefix + "pool-0", Impl: "cardano-node", Image: envOr("CONTINUUM_SMOKE_NODE_IMAGE", "ghcr.io/blinklabs-io/cardano-node:11.1.3-2")},
		{Name: smokePrefix + "pool-1", Impl: "dingo", Image: envOr("CONTINUUM_SMOKE_DINGO_IMAGE", "ghcr.io/blinklabs-io/dingo:main")},
	}
	s := genesis.Default()
	s.Pools = len(nodes)
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25
	s.EpochLength = 200
	s.PoolHost = smokePrefix + "pool-%d"
	s.SystemStart = time.Now().Add(60 * time.Second).Truncate(time.Second).UTC()
	store, err := keys.New(rng.Seed{0x49}, 0)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := genesis.Render(s, store)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, bundle); err != nil {
		t.Fatal(err)
	}
	startDevnet(t, dir, out, filepath.Join(out, "evidence"), s.Magic, nodes)

	deadline := s.SystemStart.Add(time.Duration(pairEpochs*s.EpochLength) * s.SlotLength)
	for time.Now().Before(deadline) {
		time.Sleep(min(15*time.Second, time.Until(deadline)+time.Second))
		for _, n := range nodes {
			if st := containerState(n.Name); !strings.HasPrefix(st, "running 0") {
				t.Fatalf("%s (%s) is %q: it stopped or restarted during the run", n.Name, n.Impl, st)
			}
		}
	}

	snap := oracle.Snapshot{}
	for _, n := range nodes {
		port := strings.TrimSpace(dockerOut(t, "port", n.Name, "3001/tcp"))
		pts, by, err := fetchChain(port, s.Magic, bundle)
		if err != nil {
			t.Fatalf("read %s's chain: %v", n.Name, err)
		}
		t.Logf("%s (%s): %d blocks, tip slot %d, by pool %v", n.Name, n.Impl, len(pts), tipSlot(pts), by)
		snap.Tips = append(snap.Tips, oracle.Branch{
			Node:   oracle.Node{Impl: n.Impl, Pool: n.Name, Digest: n.Impl + " " + imageID(t, n.Image)},
			Points: pts,
		})
	}
	for _, f := range oracle.Check(snap, oracle.Monitors(uint64(s.SecurityParam), policy.Guard{})) {
		t.Errorf("FINDING %s %s: %s; nodes %+v", f.Severity, f.Kind, f.Summary, f.Nodes)
	}
}
