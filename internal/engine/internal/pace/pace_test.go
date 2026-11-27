package pace

import (
	"math"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type knobs map[profile.IntentKind]float64

func (knobs) RateK() uint32                                   { return 1 }
func (k knobs) Weights() map[profile.IntentKind]float64       { return k }
func (knobs) Act(sched.Time, population.ActorID) sched.Handle { return 0 }

func newTest(over knobs) *Pace {
	return New(Spec{}, Env{Sched: over, Knobs: over})
}

func TestBurstIsOneAtBaseline(t *testing.T) {
	pc := newTest(nil)
	if got := pc.Burst(0); math.Abs(got-marketMu/marketBaseline) > 1e-12 {
		t.Fatalf("idle burst %v", got)
	}
	pc.Excite(1000)
	hot := pc.Burst(1000)
	if later := pc.Burst(60_000); !(later < hot) {
		t.Fatalf("burst did not decay: %v then %v", hot, later)
	}
}

func TestMarketRoundTrips(t *testing.T) {
	pc := newTest(nil)
	pc.Excite(500)
	m := pc.Save()
	other := newTest(nil)
	other.Load(m)
	if other.Burst(2000) != pc.Burst(2000) {
		t.Fatal("restored market differs")
	}
}

func TestWeightsOverride(t *testing.T) {
	p := profile.Profile{Weights: map[profile.IntentKind]float64{"pay": 1, "idle": 2}}
	pc := newTest(knobs{"pay": 5})
	if w := pc.Weights(p); w["pay"] != 5 || w["idle"] != 2 {
		t.Fatalf("weights %v", w)
	}
	if p.Weights["pay"] != 1 {
		t.Fatal("override mutated the profile")
	}
	if w := newTest(nil).Weights(p); w["pay"] != 1 {
		t.Fatalf("no override: %v", w)
	}
}
