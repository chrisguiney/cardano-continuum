//go:build smoke

package rogue

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

const (
	smokePrefix  = "adv2-"
	smokeNetwork = smokePrefix + "net"
)

// smokeNode is one honest devnet node a rogue peer attacks.
type smokeNode struct {
	Name, Impl, Image string
	// hostPort is the host address the container's 3001 is published to.
	hostPort string
}

// TestSmokeRoguePeers runs one cardano-node pool and one dingo pool on a plain
// Docker network, then drives every rogue behaviour against each node and
// records its reaction. It fails if a node crashes, accepts an invalid message,
// stalls an honest peer, or leaks, and reports the per-node reaction table. It
// needs Docker and the two node images, uses only containers and a network
// prefixed adv2-, and removes them at the end. It takes a few minutes:
//
//	CONTINUUM_SMOKE_OUT=<dir> go test -tags smoke -run TestSmokeRoguePeers -v -timeout 15m ./internal/rogue
func TestSmokeRoguePeers(t *testing.T) {
	out := os.Getenv("CONTINUUM_SMOKE_OUT")
	if out == "" {
		out = t.TempDir()
	}
	nodes := []*smokeNode{
		{Name: smokePrefix + "pool-0", Impl: "cardano-node", Image: envOr("CONTINUUM_SMOKE_NODE_IMAGE", "ghcr.io/blinklabs-io/cardano-node:11.1.3-2")},
		{Name: smokePrefix + "pool-1", Impl: "dingo", Image: envOr("CONTINUUM_SMOKE_DINGO_IMAGE", "ghcr.io/blinklabs-io/dingo:main")},
	}
	s := genesis.Default()
	s.Pools = len(nodes)
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25
	s.EpochLength = 200
	s.PoolHost = smokePrefix + "pool-%d"
	s.SystemStart = time.Now().Add(20 * time.Second).Truncate(time.Second).UTC()
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
	startNodes(t, dir, out, s.Magic, nodes)

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	for _, n := range nodes {
		n.hostPort = publishedPort(t, n.Name)
		waitReady(t, ctx, n.hostPort, s.Magic)
	}

	// behaviour -> node -> reaction, for the report table.
	table := map[Behaviour]map[string]string{}
	for _, n := range nodes {
		// Record once whether the node accepts our honest handshake, so a
		// reaction reached through a completed handshake is known to be
		// meaningful.
		if err := honestProbe(n.hostPort, s.Magic); err != nil {
			t.Logf("%s (%s) did not accept the honest handshake: %v", n.Name, n.Impl, err)
		} else {
			t.Logf("%s (%s) accepts the honest handshake", n.Name, n.Impl)
		}
		p, err := New(Config{Addr: n.hostPort, Magic: s.Magic, Clearance: cleared(t), Settle: 8 * time.Second}, discard())
		if err != nil {
			t.Fatalf("new rogue for %s: %v", n.Name, err)
		}
		for _, b := range Behaviours() {
			out, err := p.Run(ctx, b)
			if err != nil {
				t.Fatalf("%s against %s: %v", b, n.Name, err)
			}
			if table[b] == nil {
				table[b] = map[string]string{}
			}
			table[b][n.Impl] = string(out.Reaction)
			if out.Reaction.Fatal() {
				t.Errorf("FATAL %s against %s (%s): %s", b, n.Name, n.Impl, out.Detail)
			}
			// Liveness and non-interference: the node must still accept a fresh
			// connection and stay running after every rogue run. A node that
			// refused to accept a new connection, or whose container exited, is
			// stalled or crashed.
			if !tcpReachable(n.hostPort) {
				t.Errorf("FATAL %s left %s (%s) unable to accept a fresh peer", b, n.Name, n.Impl)
			}
			if st := containerState(n.Name); !strings.HasPrefix(st, "running 0") {
				t.Errorf("FATAL %s left %s (%s) in state %q", b, n.Name, n.Impl, st)
			}
		}
	}
	reportTable(t, nodes, table)
}

// honestProbe completes a node-to-node handshake, so a node that crashed or
// stalled is caught.
func honestProbe(addr string, magic uint32) error {
	p, err := New(Config{Addr: addr, Magic: magic, Clearance: clearedGenesis(magic), Settle: 6 * time.Second}, discard())
	if err != nil {
		return err
	}
	// A completed handshake followed by an immediate, well-formed Done is the
	// honest shape; it is not a behaviour, so reuse the raw helpers.
	conn, err := dialConn(nil, context.Background(), addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	r, detail, ok := handshakeThen(context.Background(), p, conn)
	if !ok {
		return fmt.Errorf("honest handshake failed: %s (%s)", r, detail)
	}
	return nil
}

func reportTable(t *testing.T, nodes []*smokeNode, table map[Behaviour]map[string]string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-32s", "behaviour")
	for _, n := range nodes {
		fmt.Fprintf(&b, " %-14s", n.Impl)
	}
	b.WriteByte('\n')
	for _, beh := range Behaviours() {
		fmt.Fprintf(&b, "%-32s", string(beh))
		for _, n := range nodes {
			fmt.Fprintf(&b, " %-14s", table[beh][n.Impl])
		}
		b.WriteByte('\n')
	}
	t.Logf("rogue-peer reactions (expected: %s)\n%s", "disconnect or limit, never fatal", b.String())
}
