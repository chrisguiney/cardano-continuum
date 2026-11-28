package steer

import (
	"context"
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type emitted struct {
	kind string
	body any
}

type fakeJournal struct{ got []emitted }

func (j *fakeJournal) Emit(_ sched.Time, kind string, _ population.ActorID, body any) {
	j.got = append(j.got, emitted{kind, body})
}

type fakeClock struct{ got []uint64 }

func (c *fakeClock) Adopt(_ sched.Time, i uint64) { c.got = append(c.got, i) }

type fakeRoster struct {
	ids  []population.ActorID
	dark map[population.ActorID]bool
}

func (r fakeRoster) PoolIDs() []population.ActorID { return r.ids }
func (r fakeRoster) Genesis(id population.ActorID) (int, bool) {
	return int(id) - 1, id == r.ids[0]
}
func (r fakeRoster) Retired(population.ActorID) bool  { return false }
func (r fakeRoster) Eager(population.ActorID) float64 { return 1 }
func (r fakeRoster) Dark(id population.ActorID) bool  { return r.dark[id] }

type fakeScenarios struct{ dark string }

func (fakeScenarios) Propose(context.Context, sched.Time) (string, error)  { return "", nil }
func (fakeScenarios) SPOProbe(context.Context, sched.Time) (string, error) { return "", nil }
func (fakeScenarios) HotKeys(context.Context, sched.Time) (string, error)  { return "", nil }
func (s *fakeScenarios) GoDark(_ sched.Time, arg string) string {
	s.dark = arg
	return "refused"
}

func newSteer(t *testing.T) (*Steer, *fakeJournal, *fakeClock, *fakeScenarios) {
	t.Helper()
	j, c, sc := &fakeJournal{}, &fakeClock{}, &fakeScenarios{}
	r := fakeRoster{ids: []population.ActorID{1, 2, 3}, dark: map[population.ActorID]bool{2: true}}
	slots := sched.Slots{}
	s := New(Spec{Seed: rng.Seed{}, Slots: slots}, Env{Scenarios: sc, Roster: r, Clock: c, Journal: j})
	return s, j, c, sc
}

func controlled(t *testing.T, j *fakeJournal) wire.Controlled {
	t.Helper()
	if len(j.got) == 0 || j.got[len(j.got)-1].kind != wire.KindControlled {
		t.Fatalf("no Controlled decision in %v", j.got)
	}
	return j.got[len(j.got)-1].body.(wire.Controlled)
}

func TestControlKnobs(t *testing.T) {
	t.Parallel()
	s, j, _, _ := newSteer(t)
	ctx := context.Background()
	for _, tc := range []struct {
		c   wire.Control
		why string
	}{
		{wire.Control{RequestID: "a", Op: "pause"}, ""},
		{wire.Control{RequestID: "b", Op: "set_k"}, "k must be positive"},
		{wire.Control{RequestID: "c", Op: "set_k", K: 3}, ""},
		{wire.Control{RequestID: "d", Op: "set_weights", Weights: map[string]float64{"send": 0}}, ""},
		{wire.Control{RequestID: "e", Op: "bogus"}, "unknown op"},
		{wire.Control{RequestID: "f", Op: "inject", Scenario: "nope"}, "unknown scenario"},
	} {
		if err := s.Control(ctx, 0, tc.c); err != nil {
			t.Fatal(err)
		}
		if got := controlled(t, j); got.Why != tc.why || got.RequestID != tc.c.RequestID {
			t.Fatalf("%s: %+v, want why %q", tc.c.Op, got, tc.why)
		}
	}
	if !s.Paused() || s.RateK() != 3 || s.Weights()[profile.IntentSend] != 0 || len(s.Weights()) != 1 {
		t.Fatalf("knobs: paused %v k %d weights %v", s.Paused(), s.RateK(), s.Weights())
	}
	if err := s.Control(ctx, 0, wire.Control{Op: "resume"}); err != nil || s.Paused() {
		t.Fatalf("resume: paused %v, %v", s.Paused(), err)
	}
}

func TestInjectGoDarkRoutes(t *testing.T) {
	t.Parallel()
	s, j, _, sc := newSteer(t)
	if err := s.Control(context.Background(), 0, wire.Control{Op: "inject", Scenario: "go_dark:2"}); err != nil {
		t.Fatal(err)
	}
	if sc.dark != "2" || controlled(t, j).Why != "refused" {
		t.Fatalf("routed %q, decision %+v", sc.dark, controlled(t, j))
	}
}

func TestWaveNeedsAnImage(t *testing.T) {
	t.Parallel()
	s, j, c, _ := newSteer(t)
	for _, arg := range []string{"", "0:img"} {
		if err := s.Control(context.Background(), 0, wire.Control{Op: "inject", Scenario: "upgrade:" + arg}); err != nil {
			t.Fatal(err)
		}
		if controlled(t, j).Why == "" || len(c.got) != 0 {
			t.Fatalf("upgrade:%s accepted: %+v", arg, c.got)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	s, j, c, _ := newSteer(t)
	_ = j
	s.paused, s.rateK, s.waves = true, 2, 1
	s.weights = map[profile.IntentKind]float64{profile.IntentSend: 0.5}
	s.adoptions = []wire.Adoption{{At: 5, Pool: "1", Ref: "a"}, {At: 6, Pool: "2", Ref: "a", Done: true}}
	s.images[2] = "a"
	saved := s.Save()

	r, _, c2, _ := newSteer(t)
	r.Load(saved)
	if !reflect.DeepEqual(r.Save(), saved) {
		t.Fatalf("round trip: %+v, want %+v", r.Save(), saved)
	}
	if !reflect.DeepEqual(c2.got, []uint64{0}) || len(c.got) != 0 {
		t.Fatalf("only the undone adoption is queued again: %v", c2.got)
	}
	r.Adopt(5, 0)
	if p := r.Pools(); p[0].Image != "a" || p[0].Genesis != 0 || p[1].Genesis != -1 || !p[1].Dark || p[1].Image != "a" {
		t.Fatalf("plan %+v", p)
	}
}

func TestWavePlansOneAdoptionPerPool(t *testing.T) {
	t.Parallel()
	s, j, c, _ := newSteer(t)
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	s.spec.Slots = slots
	if err := s.Control(context.Background(), 1000, wire.Control{Op: "inject", Scenario: "upgrade:2:img@sha256:ab"}); err != nil {
		t.Fatal(err)
	}
	if controlled(t, j).Why != "" || !reflect.DeepEqual(c.got, []uint64{0, 1, 2}) || s.waves != 1 {
		t.Fatalf("wave: %+v queued %v", controlled(t, j), c.got)
	}
	end := 1000 + 2*slots.EpochStart(1)
	for i, a := range s.adoptions {
		if a.At < 1000 || a.At > end || a.Ref != "img@sha256:ab" {
			t.Fatalf("adoption %d %+v outside [1000,%d]", i, a, end)
		}
		s.Adopt(a.At, uint64(i))
	}
	for _, p := range s.Pools() {
		if p.Image != "img@sha256:ab" {
			t.Fatalf("plan %+v", p)
		}
	}
	s.Adopt(0, 0)
	if n := len(j.got); n != 1+3 {
		t.Fatalf("a repeated adoption emitted again: %d decisions", n)
	}
}

// Every catalogued scenario is one inject routes, with an argument where
// the catalogue says one is needed, so the UI cannot offer a scenario the
// engine answers "unknown scenario".
func TestCatalogNamesInjectableScenarios(t *testing.T) {
	t.Parallel()
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("empty catalogue")
	}

	for _, c := range cat {
		if c.Name == "" || c.Description == "" {
			t.Errorf("catalogue entry %+v lacks a name or description", c)
		}

		sc := c.Name
		if c.Arg != "" {
			sc += ":" + c.Example
		}

		s, j, _, _ := newSteer(t)
		if err := s.Control(context.Background(), 0, wire.Control{Op: "inject", Scenario: sc}); err != nil {
			t.Fatal(err)
		}

		if why := controlled(t, j).Why; why == "unknown scenario" {
			t.Errorf("catalogued scenario %q is unknown to inject", sc)
		}
	}
}

// TestClearWeightsRestoresProfiles: clear_weights drops the whole override,
// so the profiles' own weights apply again, and a snapshot carries no
// override afterwards.
func TestClearWeightsRestoresProfiles(t *testing.T) {
	t.Parallel()
	s, j, _, _ := newSteer(t)
	ctx := context.Background()
	if err := s.Control(ctx, 0, wire.Control{RequestID: "a", Op: "set_weights", Weights: map[string]float64{"send": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Control(ctx, 0, wire.Control{RequestID: "b", Op: "clear_weights"}); err != nil {
		t.Fatal(err)
	}
	if got := controlled(t, j); got.Why != "" || got.Op != "clear_weights" {
		t.Fatalf("clear_weights: %+v", got)
	}
	if s.Weights() != nil {
		t.Fatalf("weights %v, want none", s.Weights())
	}
	if w := s.Save().Weights; w != nil {
		t.Fatalf("saved weights %v, want none", w)
	}
}
