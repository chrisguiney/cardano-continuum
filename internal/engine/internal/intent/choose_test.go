package intent

import (
	"iter"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// viewChain serves a govChain's view as the intent's Chain.
type viewChain struct {
	// g is the chain whose view is served.
	g *govChain
}

func (c viewChain) View() *chain.View        { return c.g.view }
func (c viewChain) Tip() eventlog.ChainPoint { return eventlog.ChainPoint{} }
func (c viewChain) Book() *chain.Book        { return nil }

// flight holds the unfinished intents.
type flight struct {
	// pending are the unfinished intents.
	pending []*wire.Pending
}

func (f flight) Has(id population.ActorID, kind string) bool {
	for _, p := range f.pending {
		if p.Actor == uint64(id) && p.KindOf() == kind {
			return true
		}
	}
	return false
}

func (f flight) All() iter.Seq[*wire.Pending] {
	return func(yield func(*wire.Pending) bool) {
		for _, p := range f.pending {
			if !yield(p) {
				return
			}
		}
	}
}

func (f flight) Of(wire.TxID) (uint64, bool)                         { return 0, false }
func (f flight) Coins(population.ActorID, plan.IntentID) []wire.Coin { return nil }
func (f flight) Spendable(uint64) uint64                             { return 0 }

// wallets is a roster of keystore wallets with no profiles.
type wallets struct {
	Roster
	// store derives the wallets.
	store *keys.Store
}

func (w wallets) Wallet(id population.ActorID) keys.Wallet    { return w.store.Wallet(keys.ActorID(id)) }
func (w wallets) Profile(population.ActorID) *profile.Profile { return nil }

// The view takes a withdrawal out of the reward balance only once a block
// holds it, so an award withdrawal chosen beside one still in the mempool
// withdraws the same balance and the ledger refuses it
// (WithdrawalsNotInRewards): the chooser holds the second back.
func TestWithdrawalWaitsForOneInFlight(t *testing.T) {
	t.Parallel()
	store, err := keys.New(rng.Seed{9}, 0)
	if err != nil {
		t.Fatal(err)
	}

	g := newGovChain(t, 800, 200)
	withdraw := agenda.Intent{Kind: profile.IntentWithdraw}
	actor := population.Actor{ID: 1}
	for _, tc := range []struct {
		name     string
		inFlight []*wire.Pending
		want     bool
	}{
		{"none in flight", nil, true},
		{"another actor's in flight", []*wire.Pending{{Actor: 2, Args: wire.Args{Kind: string(profile.IntentWithdraw)}}}, true},
		{"its own in flight", []*wire.Pending{{Actor: 1, Args: wire.Args{Kind: string(profile.IntentWithdraw), Amount: 5}}}, false},
	} {
		ch := New(Spec{}, Env{
			Chain:  viewChain{g: g},
			Flight: flight{pending: tc.inFlight},
			Roster: wallets{store: store},
		}).Chooser
		p, why := ch.Prepare(0, actor, withdraw, rand.New(rand.NewPCG(1, 2)))
		if got := p != nil; got != tc.want {
			t.Errorf("%s: prepared %t (%q), want %t", tc.name, got, why, tc.want)
		}
	}
}
