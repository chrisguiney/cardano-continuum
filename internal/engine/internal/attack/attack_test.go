package attack

import (
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type clock struct{ now sched.Time }

func (c clock) Now() sched.Time { return c.now }

func dustProfile(t *testing.T) (population.ActorID, agenda.Policy, *Attack) {
	t.Helper()
	ps, err := adversary.Profiles(adversary.DustFlood)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	k := New(Spec{Slots: slots}, Env{Clock: clock{}})
	pol := k.Arm(7, ps[0], nil)
	if pol == nil {
		t.Fatal("Arm returned no policy for an adversary profile")
	}
	return 7, pol, k
}

func TestArmOpensAccount(t *testing.T) {
	t.Parallel()
	id, _, k := dustProfile(t)
	if !k.Adversary(id) || k.Adversary(id+1) {
		t.Fatalf("Adversary(%d) = %v, Adversary(%d) = %v", id, k.Adversary(id), id+1, k.Adversary(id+1))
	}
	if k.Remaining(id) == 0 {
		t.Fatal("an armed adversary has no budget")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	id, _, k := dustProfile(t)
	if err := k.costs.Charge(id, 0, 5); err != nil {
		t.Fatal(err)
	}
	a, b := wire.Ref{Tx: wire.TxID{2}, Index: 1}, wire.Ref{Tx: wire.TxID{1}, Index: 3}
	k.contested[a], k.contested[b] = true, true
	charges, contested := k.Save()
	if !slices.Equal(contested, []wire.Ref{b, a}) {
		t.Fatalf("contested %v, want by Ref", contested)
	}
	_, _, r := dustProfile(t)
	r.Load(charges, contested)
	if !r.Contested(a) || !r.Contested(b) || r.Spent(id) != 5 {
		t.Fatalf("loaded: contested %v %v, spent %d", r.Contested(a), r.Contested(b), r.Spent(id))
	}
}

func TestIsKind(t *testing.T) {
	t.Parallel()
	if !IsKind(adversary.DustFlood.Kind()) || IsKind("send") {
		t.Fatal("IsKind does not tell attacks from agenda kinds")
	}
}
