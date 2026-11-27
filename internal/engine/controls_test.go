package engine

import (
	"bytes"
	"context"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func controlInput(t *testing.T, at sched.Time, c control) eventlog.Record {
	t.Helper()
	body, err := eventlog.Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindControl, At: at, Body: body}
}

func kinds(out []eventlog.Record) map[string]int {
	k := map[string]int{}
	for _, r := range out {
		k[r.Kind]++
	}
	return k
}

func controlled(t *testing.T, out []eventlog.Record) Controlled {
	t.Helper()
	for _, r := range out {
		if r.Kind == KindControlled {
			var c Controlled
			if err := eventlog.Decode(r.Body, &c); err != nil {
				t.Fatal(err)
			}
			return c
		}
	}
	t.Fatal("no Controlled decision")
	return Controlled{}
}

// tickSpan ticks e every second over (from, to] and returns the decisions.
func tickSpan(t *testing.T, e *Engine, from, to sched.Time) []eventlog.Record {
	t.Helper()
	var out []eventlog.Record
	for at := from + 1000; at <= to; at += 1000 {
		out = append(out, mustHandle(t, e, tickInput(at))...)
	}
	return out
}

// TestControlsSteerAndReplay: pause stops the agendas and resume restarts
// them; a weights override reaches the decisions; the controls survive a
// snapshot, and a restored engine decides as the original.
func TestControlsSteerAndReplay(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 4, 6)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(tickSpan(t, e, 0, 20_000)); k[KindIntentChosen] == 0 {
		t.Fatalf("no decisions before the pause: %v", k)
	}
	if c := controlled(t, mustHandle(t, e, controlInput(t, 20_000, control{RequestID: "p", Op: "pause"}))); c.Why != "" {
		t.Fatalf("pause: %+v", c)
	}
	if k := kinds(tickSpan(t, e, 20_000, 40_000)); k[KindIntentChosen] != 0 {
		t.Fatalf("%d decisions while paused", k[KindIntentChosen])
	}
	mustHandle(t, e, controlInput(t, 40_000, control{RequestID: "r", Op: "resume"}))
	mustHandle(t, e, controlInput(t, 40_000, control{RequestID: "w", Op: "set_weights", Weights: map[string]float64{"send": 0}}))
	r, err := Restore(cfg, snap(t, e))
	if err != nil {
		t.Fatal(err)
	}
	a, b := tickSpan(t, e, 40_000, 80_000), tickSpan(t, r, 40_000, 80_000)
	if len(a) != len(b) {
		t.Fatalf("restored engine made %d decisions, original %d", len(b), len(a))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || !bytes.Equal(a[i].Body, b[i].Body) {
			t.Fatalf("decision %d differs after restore: %s / %s", i, a[i].Kind, b[i].Kind)
		}
	}
	if k := kinds(a); k[KindIntentChosen] == 0 || k[KindSubmitted] != 0 {
		t.Fatalf("after resume with send weight 0: %v", k)
	}
}

// TestInjectedGoDarkIsGuarded: with three equal genesis pools and a margin
// of one, no pool may go dark; with five, one may.
func TestInjectedGoDarkIsGuarded(t *testing.T) {
	t.Parallel()
	cfg := floorConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := mustHandle(t, e, controlInput(t, 1000, control{RequestID: "d", Op: "inject", Scenario: "go_dark"}))
	if k := kinds(out); k[KindGuarded] != 1 || k[KindPoolTier] != 0 || controlled(t, out).Why == "" {
		t.Fatalf("unsafe go-dark: %v, %+v", k, controlled(t, out))
	}
	cfg.Cohorts[0].Count, cfg.GenesisPools = 5, 5
	e, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out = mustHandle(t, e, controlInput(t, 1000, control{RequestID: "d", Op: "inject", Scenario: "go_dark:2"}))
	if k := kinds(out); k[KindGuarded] != 0 || k[KindPoolTier] != 1 {
		t.Fatalf("safe go-dark: %v, %+v", k, controlled(t, out))
	}
	if p := e.st.Pools(); !p[1].Dark || p[1].Genesis != 1 {
		t.Fatalf("plan %+v, want pool actor 2 (genesis pool 1) dark", p)
	}
}

// TestUpgradeWave: an upgrade control plans every pool's adoption within
// the wave's epoch; each adoption is an ImageChange and lands in the pool
// plan, and a wave interrupted by a snapshot finishes the same way.
func TestUpgradeWave(t *testing.T) {
	t.Parallel()
	cfg := floorConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := controlled(t, mustHandle(t, e, controlInput(t, 1000, control{RequestID: "u", Op: "inject", Scenario: "upgrade:img@sha256:ab"}))); c.Why != "" {
		t.Fatalf("upgrade: %+v", c)
	}
	half := cfg.Slots.EpochStart(1) / 2
	first := tickSpan(t, e, 1000, half)
	r, err := Restore(cfg, snap(t, e))
	if err != nil {
		t.Fatal(err)
	}
	end := cfg.Slots.EpochStart(1) + 2000
	rest, again := tickSpan(t, e, half, end), tickSpan(t, r, half, end)
	n := 0
	for _, rec := range append(first, rest...) {
		if rec.Kind == KindImageChange {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d image changes, want one per pool", n)
	}
	if kinds(rest)[KindImageChange] != kinds(again)[KindImageChange] {
		t.Fatal("the restored engine's wave differs")
	}
	for _, p := range e.st.Pools() {
		if p.Image != "img@sha256:ab" {
			t.Fatalf("plan %+v", p)
		}
	}
}

// scriptControls releases each control once the clock reaches its time.
type scriptControls struct {
	clock *sched.Virtual
	at    []sched.Time
	cs    []control
}

func (s *scriptControls) Controls(context.Context) ([][]byte, error) {
	var out [][]byte
	for len(s.at) > 0 && s.clock.Now() >= s.at[0] {
		b, err := eventlog.Encode(s.cs[0])
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		s.at, s.cs = s.at[1:], s.cs[1:]
	}
	return out, nil
}
