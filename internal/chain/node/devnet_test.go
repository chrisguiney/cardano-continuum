//go:build devnet

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const (
	devnetPrefix = "continuum-chain-"
	bridgePort   = "3333"
	faucetActor  = 1000
)

// TestDevnetFollow boots the continuum-genesis devnet (three cardano-node
// containers, k = 10, f = 0.25, 200-slot epochs), follows pool 0 over
// node-to-client chain-sync into epoch 1 and checks the view against the
// node's own local-state-query. Pool 0's socket is forwarded to a loopback
// TCP port with socat, since a unix socket cannot cross Docker Desktop's VM.
//
//	go test -tags devnet -run TestDevnetFollow -v -timeout 15m ./internal/chain
//
// CONTINUUM_DEVNET_NODE_IMAGE overrides the node image.
func TestDevnetFollow(t *testing.T) {
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
	seed[31] = 16
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
	node := Node{Network: "tcp", Address: addr, Magic: s.Magic}
	q := waitUp(ctx, t, node)
	checkQuerier(ctx, t, q, s)

	book := chain.NewBook()
	if err := book.AddAddress(b.Faucet, faucetActor); err != nil {
		t.Fatal(err)
	}
	fb, err := b.Faucet.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// Shelley genesis funds sit at (Blake2b-256 of the address bytes, 0).
	gen := utxo(common.Blake2b256Hash(fb), 0, b.Faucet, s.FaucetLovelace)
	slots := testSlotsOf(t, s)
	v, err := chain.New(chain.Config{K: uint64(s.SecurityParam), Slots: slots, Book: book}, chain.State{Utxos: []common.Utxo{gen}})
	if err != nil {
		t.Fatal(err)
	}
	stats := followUntil(ctx, t, node, v, uint64(s.SecurityParam), func(v *chain.View) bool { return v.Epoch() >= 1 && !v.Settled().IsOrigin() })
	t.Logf("followed %d blocks, %d rollbacks, %d settled; tip slot %d, settled slot %d, epoch %d",
		stats.forward, stats.backward, stats.settled, v.Tip().Slot, v.Settled().Slot, v.Epoch())
	if uint64(v.Balance(faucetActor)) != s.FaucetLovelace {
		t.Errorf("faucet balance %d", v.Balance(faucetActor))
	}
	got, err := q.UtxosAt(ctx, []common.Address{b.Faucet})
	if err != nil {
		t.Fatal(err)
	}
	mine := v.UtxosAt(b.Faucet)
	if len(got) != len(mine) {
		t.Fatalf("node has %d faucet outputs, view %d", len(got), len(mine))
	}
	for i := range got {
		if chain.RefOf(got[i].Id) != chain.RefOf(mine[i].Id) || got[i].Output.Amount().Cmp(mine[i].Output.Amount()) != 0 {
			t.Errorf("faucet output %d: node %v, view %v", i, got[i], mine[i])
		}
	}
}

func checkQuerier(ctx context.Context, t *testing.T, q *Querier, s genesis.Spec) {
	t.Helper()
	g, err := q.Genesis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := chain.Genesis{
		Magic: s.Magic, Start: s.SystemStart, EpochLength: uint64(s.EpochLength),
		SecurityParam: uint64(s.SecurityParam), SlotLength: s.SlotLength,
	}
	if err := chain.Guard(want, g); err != nil {
		t.Error(err)
	}
	pp, err := q.Params(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pp == nil {
		t.Fatal("no protocol parameters")
	}
	t.Logf("node genesis %+v; params %T", g, pp)
}

type followStats struct{ forward, backward, settled int }

func followUntil(ctx context.Context, t *testing.T, n Node, v *chain.View, k uint64, done func(*chain.View) bool) followStats {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	out := make(chan chain.Event)
	f := NewFollower(n, FollowerConfig{K: k}, out, nil)
	errc := make(chan error, 1)
	go func() { errc <- f.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-errc; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("follower: %v", err)
		}
	}()
	var st followStats
	for !done(v) {
		var ev chain.Event
		select {
		case ev = <-out:
		case err := <-errc:
			errc <- err
			t.Fatalf("follower stopped: %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out at tip slot %d", v.Tip().Slot)
		}
		eff, err := apply(v, ev)
		if err != nil {
			t.Fatalf("%v at slot %d: %v", ev.Kind, ev.Point.Slot, err)
		}
		switch ev.Kind {
		case chain.Forward:
			st.forward++
		case chain.Backward:
			st.backward++
		}
		for _, e := range eff {
			if e.Kind == chain.Settled {
				st.settled++
			}
		}
	}
	return st
}

func apply(v *chain.View, ev chain.Event) ([]chain.Effect, error) {
	if ev.Kind == chain.Forward {
		return v.Apply(ev.Block)
	}
	return v.Rollback(ev.Point)
}

func testSlotsOf(t *testing.T, s genesis.Spec) sched.Slots {
	t.Helper()
	sl, err := sched.NewSlots(sched.Time(s.SlotLength.Milliseconds()), uint64(s.EpochLength))
	if err != nil {
		t.Fatal(err)
	}
	return sl
}

// waitUp retries until pool 0 answers local-state-query.
func waitUp(ctx context.Context, t *testing.T, n Node) *Querier {
	t.Helper()
	q := NewQuerier(n)
	for {
		_, err := q.Epoch(ctx)
		if err == nil {
			return q
		}
		select {
		case <-ctx.Done():
			t.Fatalf("node never answered: %v", err)
		case <-time.After(3 * time.Second):
		}
	}
}

// startDevnet runs one node per pool and returns pool 0's forwarded
// node-to-client address. Every container is removed at cleanup.
func startDevnet(t *testing.T, s genesis.Spec, b genesis.Bundle, image string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, b); err != nil {
		t.Fatal(err)
	}
	network := devnetPrefix + "net"
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
		run := "exec cardano-node run --config /config/config.json --topology /keys/topology.json" +
			" --database-path /data/db --socket-path /data/node.socket --host-addr 0.0.0.0 --port " +
			strconv.Itoa(s.PoolPort) + " --shelley-kes-key /keys/kes.skey --shelley-vrf-key /keys/vrf.skey" +
			" --shelley-operational-certificate /keys/opcert.cert"
		args := []string{"run", "-d", "--name", name, "--hostname", name, "--network", network,
			"-v", filepath.Join(dir, "config") + ":/config:ro",
			"-v", filepath.Join(dir, "pools", strconv.Itoa(i)) + ":/keys:ro"}
		if i == 0 {
			args = append(args, "-p", "127.0.0.1::"+bridgePort)
			run = "socat TCP-LISTEN:" + bridgePort + ",fork,reuseaddr UNIX-CONNECT:/data/node.socket & " + run
		}
		args = append(args, "--entrypoint", "sh", image, "-c", run)
		docker(t, args...)
	}
	out, err := exec.Command("docker", "port", fmt.Sprintf(s.PoolHost, 0), bridgePort).Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	return strings.TrimSpace(strings.Split(string(out), "\n")[0])
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
}
