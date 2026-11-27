package engine

import (
	"context"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// epochSlots is honestConfig's epoch length.
const epochSlots = 120

// floorConfig is three genesis pools of 2000 ada each, operated by actors
// 1-3, and four stakers of 1000 ada each (actors 4-7), with no simulated
// pods: only PoolStatus inputs move a pod.
func floorConfig(t *testing.T) Config {
	cfg := honestConfig(t, 5)
	cfg.Cohorts = []Cohort{{"operator", 3}, {"staker", 4}}
	cfg.GenesisPools, cfg.GenesisPoolStake, cfg.SimulatedPods = 3, 2_000_000_000, false
	return cfg
}

// stakeOnto delegates every staker to pool actor 3 on the fake chain and
// feeds the engine the blocks up to slot, which must leave the delegations
// K deep.
func stakeOnto(t *testing.T, e *Engine, fc *fakechain.Chain, slot uint64) {
	t.Helper()
	pool, _ := e.co.ring.poolOf(3)
	for id := population.ActorID(4); id <= 7; id++ {
		w := e.co.rec(id).wallet
		got, err := e.builder.Stake().Delegate(context.Background(), w, build.Delegation{
			Spend: build.Spend{Inputs: fc.UtxosAt(w.Address()), TTL: 100}, Pool: pool, Register: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := fc.Submit(context.Background(), fakechain.EraConway, got.Tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := fc.AdvanceTo(slot); err != nil {
		t.Fatal(err)
	}
	feedBlocks(t, e, fc, 0)
}

func podInput(t *testing.T, pool population.ActorID, running bool, at sched.Time) eventlog.Record {
	t.Helper()
	body, err := eventlog.Encode(PoolStatus{Pool: uint64(pool), Running: running})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindPoolStatus, At: at, Body: body}
}

// recovery sorts a Handle's decisions into the recovery's parts.
type recovery struct {
	live, repair []population.ActorID
	moves        map[population.ActorID]uint64 // wallet to pool
	last         sched.Time
}

func readRecovery(t *testing.T, out ...[]eventlog.Record) recovery {
	t.Helper()
	r := recovery{moves: map[population.ActorID]uint64{}}
	for _, recs := range out {
		for _, rec := range recs {
			r.last = max(r.last, rec.At)
			switch rec.Kind {
			case KindPoolTier:
				var b PoolTier
				if err := eventlog.Decode(rec.Body, &b); err != nil {
					t.Fatal(err)
				}
				if !b.Dark {
					r.live = append(r.live, population.ActorID(rec.Actor))
				}
			case KindRepair:
				r.repair = append(r.repair, population.ActorID(rec.Actor))
			case KindSubmitted:
				var b Submitted
				if err := eventlog.Decode(rec.Body, &b); err != nil {
					t.Fatal(err)
				}
				if b.Kind == string(profile.IntentDelegate) {
					r.moves[population.ActorID(rec.Actor)] = b.To
				}
			}
		}
	}
	return r
}

// forgingShare is the percentage of s's stake on pools other than dead.
func forgingShare(s policy.Schedule, dead ...population.ActorID) uint64 {
	var tot, live uint64
	for _, d := range s {
		if d.Pool == 0 {
			continue
		}
		tot += d.Stake
		ok := true
		for _, x := range dead {
			ok = ok && d.Pool != x
		}
		if ok {
			live += d.Stake
		}
	}
	return 100 * live / max(tot, 1)
}

// TestRecoveryStartsOnProjectedBreach: pool 3's pod dies after epoch 1's
// last block. The schedule in use (set) keeps the floor without pool 3;
// the next one (mark, which holds the stakers' delegations) does not. The
// engine recovers at once, in epoch 1: a repair and a move for every
// staker (FloorRecoverLateBug).
func TestRecoveryStartsOnProjectedBreach(t *testing.T) {
	t.Parallel()
	cfg := floorConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fc, err := NewFakeChain(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	stakeOnto(t, e, fc, 2*epochSlots-cfg.BlockEvery)
	at := cfg.Slots.SlotStart(2*epochSlots - 2)
	s := e.floorState(at)
	if s.Label != 1 || s.Epoch != 1 || forgingShare(s.Set, 3) < 50 || forgingShare(s.Mark, 3) >= 50 {
		t.Fatalf("precondition: label %d epoch %d, set share %d%%, mark share %d%%",
			s.Label, s.Epoch, forgingShare(s.Set, 3), forgingShare(s.Mark, 3))
	}
	r := readRecovery(t, mustHandle(t, e, podInput(t, 3, false, at)))
	if len(r.repair) != 1 || r.repair[0] != 3 {
		t.Fatalf("repairs %v, want pool 3", r.repair)
	}
	for id := population.ActorID(4); id <= 7; id++ {
		if to, ok := r.moves[id]; !ok || to == 3 {
			t.Errorf("staker %d: move %v (ok %v)", id, to, ok)
		}
	}
}

// TestRecoveryIntentsInOneEpoch: with pool 1 planned dark and the pods of
// pools 2 and 3 dead, beyond the margin of one, the engine brings pool 1
// live, repairs both pods and moves every staker off pool 3, all before
// the epoch boundary (FloorRecoverNoRedelegateBug).
func TestRecoveryIntentsInOneEpoch(t *testing.T) {
	t.Parallel()
	cfg := floorConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fc, err := NewFakeChain(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	stakeOnto(t, e, fc, epochSlots+4*cfg.BlockEvery)
	at := cfg.Slots.SlotStart(epochSlots + 4*cfg.BlockEvery + 1)
	e.dark[1] = true
	r := readRecovery(t,
		mustHandle(t, e, podInput(t, 2, false, at)),
		mustHandle(t, e, podInput(t, 3, false, at+1000)))
	if len(r.live) != 1 || r.live[0] != 1 {
		t.Errorf("go-live %v, want pool 1", r.live)
	}
	if len(r.repair) != 2 || r.repair[0] != 2 || r.repair[1] != 3 {
		t.Errorf("repairs %v, want pools 2 and 3", r.repair)
	}
	for id := population.ActorID(4); id <= 7; id++ {
		if to, ok := r.moves[id]; !ok || to != 1 {
			t.Errorf("staker %d: move to %d (ok %v), want pool 1", id, to, ok)
		}
	}
	if r.last >= cfg.Slots.EpochStart(2) {
		t.Errorf("recovery ran until %d, past the boundary %d", r.last, cfg.Slots.EpochStart(2))
	}
}

// TestEmptyEpochIsFatal: a block in epoch 2 after none in epoch 1 is a
// stall incident, and the engine is stalled (NoFreeze).
func TestEmptyEpochIsFatal(t *testing.T) {
	t.Parallel()
	cfg := floorConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n <= 11; n++ {
		mustHandle(t, e, blockInput(t, n))
	}
	if e.Stalled() {
		t.Fatal("stalled within epoch 0")
	}
	var inc []Incident
	for _, rec := range mustHandle(t, e, blockInput(t, 25)) {
		if rec.Kind == KindIncident {
			var b Incident
			if err := eventlog.Decode(rec.Body, &b); err != nil {
				t.Fatal(err)
			}
			inc = append(inc, b)
		}
	}
	if len(inc) != 1 || inc[0].Kind != "stall" || inc[0].Epoch != 2 || inc[0].Label != 0 {
		t.Fatalf("incidents %+v, want one stall in epoch 2 after label 0", inc)
	}
	if !e.Stalled() {
		t.Fatal("engine not stalled")
	}
	r, err := Restore(cfg, snap(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Stalled() {
		t.Fatal("stall lost across a snapshot")
	}
}

// killPods reports the listed pods dead once the clock reaches at.
type killPods struct {
	at   sched.Time
	dead []population.ActorID
	done bool
}

func (k *killPods) Changes(_ context.Context, now sched.Time) ([]PoolStatus, error) {
	if k.done || now < k.at {
		return nil, nil
	}
	k.done = true
	var out []PoolStatus
	for _, id := range k.dead {
		out = append(out, PoolStatus{Pool: uint64(id)})
	}
	return out, nil
}

// boundConfig is four genesis pools of 5000 ada and eight stakers of
// 1000 ada, who all delegate to pool 1 (every return is zero, so the
// lowest ID wins), which the floor allows: losing pool 1 still leaves
// 15000 of 28000 forging.
func boundConfig(t *testing.T, simulated bool) Config {
	cfg := honestConfig(t, 11)
	cfg.Cohorts = []Cohort{{"operator", 4}, {"staker", 8}}
	cfg.GenesisPools, cfg.GenesisPoolStake, cfg.SimulatedPods = 4, 5_000_000_000, simulated
	return cfg
}

// endangeredWithout reports whether the floor is endangered at at once the
// pods of dead stop.
func endangeredWithout(e *Engine, at sched.Time, dead []population.ActorID) bool {
	s := e.floorState(at)
	for i := range s.Pools {
		for _, d := range dead {
			if s.Pools[i].ID == d {
				s.Pools[i].Running = false
			}
		}
	}
	return e.guard.Endangered(s)
}

// TestRecoveryBound: the pods of pools 1 and 2 die at the start of epoch
// 3, leaving 10000 of 28000 forging. With repair the floor is back within
// the epoch of the deaths; without, the stakers' moves off pool 1 bring it
// back within 3 epochs (FloorRecoverRepair, FloorRecover).
func TestRecoveryBound(t *testing.T) {
	t.Parallel()
	dead := []population.ActorID{1, 2}
	for _, tt := range []struct {
		name      string
		simulated bool
		epochs    uint64
	}{{"repair", true, 1}, {"moves", false, 3}} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := boundConfig(t, tt.simulated)
			w := newWorld(t, cfg)
			death := cfg.Slots.EpochStart(3)
			e := cleanRun(t, w, death-1).r.Engine()
			for id := population.ActorID(5); id <= 12; id++ {
				if pool, ok := e.win.View().DelegatedTo(id); !ok || pool != 1 {
					t.Fatalf("precondition: staker %d on pool %d (%v), want pool 1", id, pool, ok)
				}
			}
			if !endangeredWithout(e, death, dead) {
				t.Fatal("precondition: losing pools 1 and 2 does not endanger the floor")
			}
			w.pods = &killPods{at: death, dead: dead}
			p, err := w.start(nil, 50)
			if err != nil {
				t.Fatal(err)
			}
			end := cfg.Slots.EpochStart(3+tt.epochs) - 1
			if err := p.r.Run(context.Background(), end); err != nil {
				t.Fatal(err)
			}
			if e := p.r.Engine(); e.guard.Endangered(e.floorState(end)) {
				t.Fatalf("floor still endangered at the end of epoch %d: %+v", 2+tt.epochs, e.floorState(end))
			}
		})
	}
}
