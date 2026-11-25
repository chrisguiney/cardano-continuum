package policy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Pools A, B and C; the genesis delegation gives A 3 units (w11), B 2
// (w13, w14) and C 1 (w12), so at a 70% floor C may go dark (5 of 6) but
// B may not (4 of 6).
const (
	pA population.ActorID = 1
	pB population.ActorID = 2
	pC population.ActorID = 3
)

func d(w, p population.ActorID, stake uint64) Delegation {
	return Delegation{Wallet: w, Pool: p, Stake: stake}
}

func genesis() Schedule {
	return Schedule{d(11, pA, 3), d(12, pC, 1), d(13, pB, 1), d(14, pB, 1)}
}

func live(ids ...population.ActorID) []Pool {
	out := make([]Pool, 0, len(ids))
	for _, id := range ids {
		out = append(out, Pool{ID: id, Reg: Registered, Live: true, Running: true})
	}
	return out
}

// base is epoch 0 with its transition run, every snapshot the genesis
// delegation, and every pool registered, live and running.
func base() State {
	return State{Set: genesis(), Mark: genesis(), Deleg: genesis(), Pools: live(pA, pB, pC)}
}

func guard(floor uint64, margin int) Guard { return Guard{cfg: Config{Floor: floor, Margin: margin}} }

func moved(s Schedule, w, to population.ActorID) Schedule {
	out := append(Schedule(nil), s...)
	for i := range out {
		if out[i].Wallet == w {
			out[i].Pool = to
		}
	}
	return out
}

func pool(s *State, id population.ActorID) *Pool {
	for i := range s.Pools {
		if s.Pools[i].ID == id {
			return &s.Pools[i]
		}
	}
	panic("no pool")
}

func dark(id population.ActorID) Intent { return Intent{Op: GoDark, Pool: id} }

func TestNewValidates(t *testing.T) {
	for _, c := range []Config{{Floor: 0, Margin: 1}, {Floor: 101, Margin: 1}, {Floor: 70, Margin: 0}, {Floor: 70, Margin: -1}} {
		if _, err := New(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := New(Config{Floor: 70, Margin: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleSelection(t *testing.T) {
	s := State{
		Set:   Schedule{d(11, pA, 1)},
		Mark:  Schedule{d(11, pA, 2)},
		Deleg: Schedule{d(11, pA, 3)},
	}
	proj := Schedule{d(11, pA, 4)}
	cases := []struct {
		epoch, label uint64
		want         []Schedule
		names        []string
	}{
		{0, 0, []Schedule{s.Set, s.Mark, proj}, []string{"set", "mark", "projection"}},
		{1, 0, []Schedule{s.Mark, s.Deleg, proj}, []string{"mark", "delegation", "projection"}},
		{7, 7, []Schedule{s.Set, s.Mark, proj}, []string{"set", "mark", "projection"}},
		{8, 7, []Schedule{s.Mark, s.Deleg, proj}, []string{"mark", "delegation", "projection"}},
		{9, 7, []Schedule{s.Set}, []string{"set"}},
	}
	for _, c := range cases {
		s.Epoch, s.Label = c.epoch, c.label
		ws, err := s.horizon(proj)
		if err != nil {
			t.Fatalf("epoch %d label %d: %v", c.epoch, c.label, err)
		}
		var got []Schedule
		var names []string
		for _, w := range ws {
			got, names = append(got, w.d), append(names, w.name)
		}
		if !reflect.DeepEqual(got, c.want) || !reflect.DeepEqual(names, c.names) {
			t.Errorf("epoch %d label %d: got %v %v", c.epoch, c.label, names, got)
		}
	}
	s.Epoch, s.Label = 3, 4
	if _, err := s.horizon(proj); err == nil {
		t.Fatal("label past the epoch accepted")
	}
}

func TestGuardUsesSnapshotNotLive(t *testing.T) {
	s := base()
	s.Deleg = moved(moved(s.Deleg, 13, pA), 14, pA)
	v := guard(70, 0).Check(s, dark(pB))
	if v.OK || !strings.Contains(v.Why, "set") {
		t.Fatalf("taking B dark on live stake alone: %+v", v)
	}
}

func TestGuardLooksTwoEpochsAhead(t *testing.T) {
	g := guard(70, 0)
	// Only the next schedule breaks: the mark holds B's stake on C, and the
	// confirmed delegation has already moved it back.
	s := base()
	s.Mark = moved(moved(s.Mark, 13, pC), 14, pC)
	if v := g.Check(s, dark(pC)); v.OK || !strings.Contains(v.Why, "mark") {
		t.Fatalf("next schedule only: %+v", v)
	}
	// Only the projection breaks: the stake moved onto C this epoch.
	s = base()
	s.Deleg = moved(moved(s.Deleg, 13, pC), 14, pC)
	if v := g.Check(s, dark(pC)); v.OK || !strings.Contains(v.Why, "projection") {
		t.Fatalf("projection only: %+v", v)
	}
	if v := g.Check(base(), dark(pC)); !v.OK {
		t.Fatalf("control: %+v", v)
	}
}

func TestGuardCountsInFlight(t *testing.T) {
	// A 50 and B 30 forge, C (20) is dark: 80%. Moving 6 from B to C
	// leaves 74%; moving 12 leaves 68%, below the floor.
	all := Schedule{d(11, pA, 50), d(12, pB, 18), d(15, pB, 6), d(16, pB, 6), d(13, pC, 10), d(14, pC, 10)}
	s := State{Set: all, Mark: all, Deleg: all, Pools: live(pA, pB, pC)}
	pool(&s, pC).Live = false
	g := guard(70, 0)
	second := Intent{Op: Delegate, Wallet: 16, Pool: pC}
	if v := g.Check(s, second); !v.OK {
		t.Fatalf("one move alone: %+v", v)
	}
	s.InFlight = []Intent{{Op: Delegate, Wallet: 15, Pool: pC}}
	if v := g.Check(s, second); v.OK {
		t.Fatalf("second move with the first in flight: %+v", v)
	}
}

func TestGuardRetiringIsDark(t *testing.T) {
	g := guard(70, 0)
	confirmed := base()
	pool(&confirmed, pA).Reg = Retiring
	inflight := base()
	inflight.InFlight = []Intent{{Op: Retire, Pool: pA}}
	// A re-registration counts only once it confirms.
	rereg := confirmed
	rereg.InFlight = []Intent{{Op: Reregister, Pool: pA}}
	for name, s := range map[string]State{"confirmed": confirmed, "in flight": inflight, "reregistering": rereg} {
		if v := g.Check(s, dark(pC)); v.OK {
			t.Errorf("%s: retiring A counted as forging: %+v", name, v)
		}
	}
}

func TestGuardDenominatorFromSnapshot(t *testing.T) {
	// A retired at this epoch's transition and its delegator is reaped, but
	// its 3 units stay in set and mark for two epochs. B alone holds 2 of 6
	// (33%), not the 2 of 3 a count over live pools gives.
	s := base()
	pool(&s, pA).Reg = Retired
	s.Deleg = moved(s.Deleg, 11, 0)
	if v := guard(60, 0).Check(s, dark(pC)); v.OK {
		t.Fatalf("retired stake left out of the denominator: %+v", v)
	}
}

func TestGuardFaultMargin(t *testing.T) {
	s := base()
	if v := guard(30, 0).Check(s, dark(pB)); !v.OK {
		t.Fatalf("no margin: %+v", v)
	}
	if v := guard(30, 1).Check(s, dark(pB)); v.OK {
		t.Fatalf("losing A leaves C with 1 of 6: %+v", v)
	}
	if v := guard(30, 1).Check(s, dark(pC)); !v.OK {
		t.Fatalf("losing A leaves B with 2 of 6: %+v", v)
	}
}

func TestGuardUsesObservedPods(t *testing.T) {
	s := base()
	pool(&s, pA).Running = false
	if v := guard(70, 0).Check(s, dark(pC)); v.OK {
		t.Fatalf("planned-live pool with a dead pod counted: %+v", v)
	}
}

func TestGuardNeverBlocksRecovery(t *testing.T) {
	s := base()
	pool(&s, pA).Running = false
	pool(&s, pB).Live = false
	g := guard(70, 0)
	for _, in := range []Intent{
		{Op: GoLive, Pool: pB},
		{Op: Repair, Pool: pA},
		{Op: Reregister, Pool: pA},
		{Op: Delegate, Wallet: 11, Pool: pC}, // off a dead pod onto a forging pool
		{Op: Delegate, Wallet: 13, Pool: pC}, // off a dark pool onto a forging pool
	} {
		if v := g.Check(s, in); !v.OK {
			t.Errorf("%+v refused: %+v", in, v)
		}
	}
	for _, in := range []Intent{
		{Op: Delegate, Wallet: 12, Pool: pB},       // off a forging pool
		{Op: Undelegate, Wallet: 13},               // undelegation is always guarded
		{Op: Delegate, Wallet: 11, Pool: pB},       // between pools neither forging
		{Op: Retire, Pool: pB},                     // a dark pool retiring
		{Op: Delegate, Wallet: 99, Pool: pC},       // a wallet with no stake entry
		{Op: Op(99), Pool: pC},                     // an unknown intent
		{Op: GoDark, Pool: population.ActorID(77)}, // a pool not in the state
	} {
		if v := g.Check(s, in); v.OK {
			t.Errorf("%+v allowed below the floor: %+v", in, v)
		}
	}
}

func TestGuardRejectsMalformedState(t *testing.T) {
	s := base()
	s.Set = append(s.Set, d(11, pB, 1))
	if v := guard(30, 0).Check(s, dark(pC)); v.OK || !strings.Contains(v.Why, "twice") {
		t.Fatalf("duplicate wallet: %+v", v)
	}
	s = base()
	s.Set = Schedule{d(11, pA, 1<<63), d(12, pA, 1<<63)}
	if v := guard(30, 0).Check(s, dark(pC)); v.OK || !strings.Contains(v.Why, "overflow") {
		t.Fatalf("overflow: %+v", v)
	}
	s = base()
	s.Set = nil
	if v := guard(30, 0).Check(s, dark(pC)); v.OK {
		t.Fatalf("empty schedule elects nobody: %+v", v)
	}
}

func TestCheckIsOrderIndependent(t *testing.T) {
	s := base()
	s.InFlight = []Intent{{Op: Delegate, Wallet: 13, Pool: pC}, {Op: Retire, Pool: pB}}
	r := s
	r.Pools = []Pool{s.Pools[2], s.Pools[0], s.Pools[1]}
	r.Set = Schedule{s.Set[3], s.Set[1], s.Set[0], s.Set[2]}
	r.InFlight = []Intent{s.InFlight[1], s.InFlight[0]}
	g := guard(50, 1)
	for _, in := range []Intent{dark(pA), dark(pC), {Op: Delegate, Wallet: 11, Pool: pC}} {
		if a, b := g.Check(s, in), g.Check(r, in); a != b {
			t.Errorf("%+v: %+v vs %+v", in, a, b)
		}
	}
}
