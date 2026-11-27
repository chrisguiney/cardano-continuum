package serve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/cli/sim"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// liar adds lovelace to the first output chain-sync delivers.
type liar struct {
	*engine.FakeNode
	extra uint64
}

func (l *liar) Intersect(points []eventlog.ChainPoint) (engine.Follower, eventlog.ChainPoint, error) {
	f, at, err := l.FakeNode.Intersect(points)
	return liarFollower{f: f, l: l}, at, err
}

type liarFollower struct {
	f engine.Follower
	l *liar
}

func (lf liarFollower) Next() (engine.ChainEvent, bool, error) {
	ev, ok, err := lf.f.Next()
	if !ok || err != nil || ev.Rollback || lf.l.extra == 0 {
		return ev, ok, err
	}
	var c engine.BlockContent
	if err := eventlog.Decode(ev.Content, &c); err != nil || len(c.Txs) == 0 || len(c.Txs[0].Outputs) == 0 {
		return ev, ok, err
	}
	c.Txs[0].Outputs[0].Lovelace += lf.l.extra
	lf.l.extra = 0
	ev.Content, err = eventlog.Encode(c)
	return ev, ok, err
}

// TestServeShowsFatalFinding: a node reporting a block that creates value
// gives a fatal finding, which `continuum serve` lists at /api/findings.
func TestServeShowsFatalFinding(t *testing.T) {
	ctx := context.Background()
	sf := simflags.Flags{
		Run:        simflags.Run{Seed: 9},
		Population: simflags.Population{Wallets: 8, Scale: 1},
		Chain:      simflags.Chain{SlotMs: 1000, EpochSlots: 60, K: 5, BlockEvery: 10, TTL: 120, Floor: 50},
	}
	cfg, err := sf.Config()
	if err != nil {
		t.Fatal(err)
	}
	c, err := engine.NewFakeChain(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	node := &engine.FakeNode{Chain: c, Slots: cfg.Slots, BlockEvery: cfg.BlockEvery}
	mem := &eventlog.MemLog{}
	r, err := engine.Start(ctx, cfg, engine.Deps{Clock: sched.NewVirtual(0), Log: mem, Node: &liar{FakeNode: node, extra: 7}, Pacer: node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, cfg.Slots.EpochStart(1)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "events.log")
	if err := sim.WriteLog(ctx, path, mem); err != nil {
		t.Fatal(err)
	}
	srv, stop := startServe(t, "--log", path, "--epoch-slots", "60")
	defer stop()
	var page struct {
		Total int
		Rows  []struct {
			Summary       string
			ReplayCommand string `json:"replay_command"`
		}
	}
	if code := srv.getJSON(t, "/api/findings", &page); code != 200 || page.Total != 1 ||
		!strings.HasPrefix(page.Rows[0].Summary, "fatal value_not_conserved:") {
		t.Fatalf("findings %d %+v", code, page)
	}
	if !strings.HasPrefix(page.Rows[0].ReplayCommand, "continuum replay --seed 9 ") {
		t.Fatalf("replay command %q", page.Rows[0].ReplayCommand)
	}
}
