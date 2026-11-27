package floor

import (
	"reflect"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type fakeStake struct {
	st  policy.State
	got []policy.Intent
}

func (s *fakeStake) PolicyState(epoch uint64, _ agenda.Pods, inflight []policy.Intent) policy.State {
	s.got = inflight
	st := s.st
	st.Epoch = epoch
	return st
}

type fakeBook map[uint64]*wire.Pending

func (b fakeBook) IDs() []uint64 {
	ids := make([]uint64, 0, len(b))
	for id := range b {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (b fakeBook) Get(id uint64) *wire.Pending { return b[id] }

type fakeCensus map[population.ActorID]bool

func (c fakeCensus) Retiring(id population.ActorID) bool { return c[id] }

type fakeJournal struct{ kinds []string }

func (j *fakeJournal) Emit(_ sched.Time, kind string, _ population.ActorID, _ any) {
	j.kinds = append(j.kinds, kind)
}

type fakeClock sched.Time

func (c fakeClock) Now() sched.Time { return sched.Time(c) }

func pending(actor uint64, kind profile.IntentKind, to uint64) *wire.Pending {
	return &wire.Pending{Actor: actor, Args: wire.Args{Kind: string(kind), To: to}}
}

func slots(t *testing.T) sched.Slots {
	t.Helper()
	s, err := sched.NewSlots(1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newFloor(t *testing.T, st policy.State, b fakeBook, c fakeCensus) (*Floor, *fakeStake, *fakeJournal) {
	t.Helper()
	sk, jr := &fakeStake{st: st}, &fakeJournal{}
	f, err := New(Spec{Guard: policy.Config{Floor: 50, Margin: 1}, Slots: slots(t), SimulatedPods: true},
		Env{Stake: sk, Book: b, Census: c, Journal: jr, Clock: fakeClock(0)})
	if err != nil {
		t.Fatal(err)
	}
	return f, sk, jr
}

// TestInflightOrder: the guard sees every guarded intent in flight in
// intent ID order, and a pool registration only while its pool is retiring
// (contract rule 5).
func TestInflightOrder(t *testing.T) {
	t.Parallel()
	b := fakeBook{
		4: pending(11, profile.IntentRegisterPool, 0),
		1: pending(7, profile.IntentDelegate, 10),
		3: pending(10, profile.IntentRetirePool, 0),
		2: pending(8, profile.IntentUndelegate, 0),
		5: pending(12, profile.IntentRegisterPool, 0),
		6: pending(9, profile.IntentSend, 0),
	}
	f, sk, _ := newFloor(t, policy.State{}, b, fakeCensus{11: true})
	f.State(0)
	want := []policy.Intent{
		{Op: policy.Delegate, Wallet: 7, Pool: 10},
		{Op: policy.Undelegate, Wallet: 8},
		{Op: policy.Retire, Pool: 10},
		{Op: policy.Reregister, Pool: 11},
	}
	if !reflect.DeepEqual(sk.got, want) {
		t.Fatalf("in flight %+v, want %+v", sk.got, want)
	}
}

// TestRecoverFrozen: a frozen ledger is one stall incident, after which
// recovery stops (contract rule 14).
func TestRecoverFrozen(t *testing.T) {
	t.Parallel()
	st := policy.State{Label: 0, Pools: []policy.Pool{{ID: 1, Reg: policy.Registered, Live: true, Running: true}}}
	f, _, jr := newFloor(t, st, fakeBook{}, fakeCensus{})
	at := slots(t).SlotStart(25)
	if mv := f.Recover(at); mv != nil || !f.Stalled() {
		t.Fatalf("moves %v, stalled %v", mv, f.Stalled())
	}
	f.Recover(at)
	if !reflect.DeepEqual(jr.kinds, []string{wire.KindIncident}) {
		t.Fatalf("emitted %v, want one incident", jr.kinds)
	}
}

// TestSaveLoad: the plan, the pods and the stall flag survive a snapshot,
// and a tier change that changes nothing emits nothing.
func TestSaveLoad(t *testing.T) {
	t.Parallel()
	f, _, jr := newFloor(t, policy.State{}, fakeBook{}, fakeCensus{})
	f.SetTier(0, 3, true, "agenda")
	f.SetTier(0, 3, true, "agenda")
	f.SetTier(0, 1, true, "agenda")
	f.PodStatus(2, false)
	if len(jr.kinds) != 2 {
		t.Fatalf("emitted %v, want two tier changes", jr.kinds)
	}
	saved := f.Save()
	want := Saved{Dark: []uint64{1, 3}, Down: []uint64{2}, Repairs: []uint64{}}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("Save %+v, want %+v", saved, want)
	}
	g, _, _ := newFloor(t, policy.State{}, fakeBook{}, fakeCensus{})
	g.Load(saved)
	if !reflect.DeepEqual(g.Save(), want) || !g.Dark(3) || g.PodRunning(2) || !slices.Equal(g.Plan(), []population.ActorID{1, 3}) {
		t.Fatalf("after Load: %+v, plan %v", g.Save(), g.Plan())
	}
}
