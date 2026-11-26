//go:build devnet

package chain

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// TestDevnetQueries boots the same devnet as TestDevnetFollow and asks pool
// 0 for the chain data the engine needs beyond blocks: stake snapshots,
// pool registrations and governance state, in the first epoch after genesis
// and again once epoch 1 has begun. It reports which queries the node
// refuses.
//
//	go test -tags devnet -run TestDevnetQueries -v -timeout 15m ./internal/chain
func TestDevnetQueries(t *testing.T) {
	image := os.Getenv("CONTINUUM_DEVNET_NODE_IMAGE")
	if image == "" {
		image = "ghcr.io/blinklabs-io/cardano-node:11.1.3-1"
	}
	s := genesis.Default()
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25
	s.EpochLength = 200
	s.PoolHost = devnetPrefix + "pool-%d"
	s.SystemStart = time.Now().Add(30 * time.Second).Truncate(time.Second).UTC()
	var seed rng.Seed
	seed[31] = 17
	ks, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := genesis.Render(s, ks)
	if err != nil {
		t.Fatal(err)
	}
	addr := startDevnet(t, s, b, image)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	q := waitUp(ctx, t, Node{Network: "tcp", Address: addr, Magic: s.Magic})

	check := func(label string) {
		snaps, err := q.StakeSnapshots(ctx)
		if err != nil {
			t.Errorf("%s: stake snapshots: %v", label, err)
		} else {
			t.Logf("%s: snapshots: %d pools; totals mark %d set %d go %d", label, len(snaps.Pools), snaps.Mark, snaps.Set, snaps.Go)
		}
		regs, err := q.PoolRegistrations(ctx)
		switch {
		case err != nil:
			t.Errorf("%s: pool state: %v", label, err)
		case len(regs.Registered) != s.Pools:
			t.Errorf("%s: %d pools registered, genesis has %d", label, len(regs.Registered), s.Pools)
		default:
			t.Logf("%s: %d pools registered, %d retiring", label, len(regs.Registered), len(regs.Retiring))
		}
		gov, err := q.Governance(ctx)
		if err != nil {
			t.Errorf("%s: governance: %v", label, err)
		} else {
			t.Logf("%s: governance: %d proposals, %d dreps, %d vote delegations",
				label, len(gov.Proposals), len(gov.DReps), len(gov.VoteDelegations))
		}
		comm, err := q.Committee(ctx)
		if err != nil {
			t.Logf("%s: committee (known gouroboros gap): %v", label, err)
		} else {
			t.Logf("%s: committee: %d members", label, len(comm))
		}
	}
	check("start")
	for {
		ep, err := q.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ep >= 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("epoch 1 never began")
		case <-time.After(5 * time.Second):
		}
	}
	check("epoch 1")
}
