package agenda

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// owned is a view whose wallet 7 has a sibling, wallet 8, delegated to pool
// 2, and an owner of the given spread.
type owned struct {
	fakeView
	spread float64
}

func (o owned) Siblings(population.ActorID) []population.ActorID { return []population.ActorID{8} }
func (o owned) Spread(population.ActorID) float64                { return o.spread }
func (o owned) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	if id == 8 {
		return 2, true
	}
	return 0, false
}

// An owner that spreads its delegations sends a new wallet to another pool
// than its sibling's, and one that does not stays with the best pool.
func TestSpreadMovesSiblingsApart(t *testing.T) {
	pools := []PoolInfo{{ID: 1, ROI: 0.04}, {ID: 2, ROI: 0.05}}
	base := fakeView{balance: 10_000 * ada, pools: pools, stake: true}
	a := actor(profile.Wallet, 0)
	for _, c := range []struct {
		spread float64
		want   population.ActorID
	}{{0, 2}, {0.5, 1}, {1, 1}} {
		for _, name := range []profile.AgendaName{"yield", "profit"} {
			pol, _ := For(name)
			s := score(pol.Score(a, owned{fakeView: base, spread: c.spread}), profile.IntentDelegate)
			if s.Target != c.want {
				t.Errorf("%s spread %v: delegates to %d, want %d (%s)", name, c.spread, s.Target, c.want, s.Why)
			}
		}
	}

	pol, _ := For("yield")
	if s := score(pol.Score(a, base), profile.IntentDelegate); s.Target != 2 {
		t.Fatalf("a view without owners delegates to %d, want the best pool", s.Target)
	}
}
