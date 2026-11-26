package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// TestFakeRunMistakesFlag: mistakes are on by default for the shipped
// profiles, whose competence falls below 1; the summary reports them, and
// the replay command line must agree with the run's.
func TestFakeRunMistakesFlag(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "run")
	flags := []string{"--seed", "4", "--wallets", "30", "--epoch-slots", "120"}
	var out bytes.Buffer
	if err := run(ctx, append([]string{"run", "--fake", "--epochs", "2", "--out", dir}, flags...), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "mistakes true: rejections ") || !strings.Contains(out.String(), "findings 0 (fatal 0)") {
		t.Fatalf("summary: %s", out.String())
	}
	log := filepath.Join(dir, "events.log")
	out.Reset()
	if err := run(ctx, append(append([]string{"replay"}, flags...), log), &out); err != nil || !strings.Contains(out.String(), ": 0 mismatches") {
		t.Fatalf("replay: %v\n%s", err, out.String())
	}
	if err := run(ctx, append(append([]string{"replay", "--mistakes", "off"}, flags...), log), &out); err == nil {
		t.Fatal("replay with mistakes off of a run with them on succeeded")
	}
	if err := run(ctx, []string{"run", "--fake", "--out", t.TempDir(), "--mistakes", "maybe"}, &out); err == nil {
		t.Fatal("--mistakes maybe accepted")
	}
}

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
	sf := simFlags{seed: 9, wallets: 8, profiles: "", scale: 1, slotMs: 1000, epochSlots: 60, k: 5, blockEvery: 10, ttl: 120, floor: 50}
	cfg, err := sf.config()
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
	if err := writeLog(ctx, path, mem); err != nil {
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
