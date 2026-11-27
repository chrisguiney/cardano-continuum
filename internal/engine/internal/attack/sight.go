package attack

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// maxVictims bounds the honest outputs a steal chooses from.
const maxVictims = 256

// Sight is what the adversaries see of the honest actors: the locked
// outputs, the wallets and the settled outputs (adversary.Sight, less
// Budgets, which the composite's view adds).
type Sight struct {
	core *core
	acct *Accounts
	// owners maps an actor's lock datum to the actor, derived on demand.
	owners map[string]population.ActorID
}

// Shared implements adversary.Sight: the honest locked outputs at the
// shared script address no adversary is already spending.
func (s *Sight) Shared() []adversary.Locked {
	busy, taken := map[wire.Ref]bool{}, map[wire.Ref]bool{}
	for q := range s.core.env.Flight.All() {
		if q.Locked == nil {
			continue
		}
		if q.Attack == "" {
			busy[*q.Locked] = true
		} else {
			taken[*q.Locked] = true
		}
	}
	var out []adversary.Locked
	for _, u := range s.core.env.Chain.View().UtxosAt(build.AlwaysSucceeds.Address(s.core.spec.NetworkID)) {
		ref := wire.Ref{Tx: wire.TxID(u.Id.Id()), Index: u.Id.Index()}
		owner, ok := s.lockOwner(u.Output.Datum())
		if !ok || taken[ref] {
			continue
		}
		out = append(out, adversary.Locked{Ref: adversary.Ref{Tx: ref.Tx, Index: ref.Index}, Owner: owner,
			Lovelace: u.Output.Amount().Uint64(), Busy: busy[ref]})
	}
	slices.SortFunc(out, func(a, b adversary.Locked) int { return cmpRef(a.Ref, b.Ref) })
	return out
}

func cmpRef(a, b adversary.Ref) int {
	return cmp.Or(bytes.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}

// lockOwner is the honest actor whose datum d is (Spec.Datum).
func (s *Sight) lockOwner(d *common.Datum) (population.ActorID, bool) {
	if d == nil {
		return 0, false
	}
	ids := s.core.env.Roster.IDs()
	if len(s.owners) != len(ids) {
		s.owners = make(map[string]population.ActorID, len(ids))
		for _, id := range ids {
			s.owners[string(s.core.spec.Key(s.core.spec.Datum(id)))] = id
		}
	}
	id, ok := s.owners[string(s.core.spec.Key(*d))]
	return id, ok && !s.acct.Adversary(id)
}

// Payees implements adversary.Sight: the honest wallets not abandoned.
func (s *Sight) Payees() []population.ActorID {
	var out []population.ActorID
	for _, id := range s.core.env.Roster.IDs() {
		if s.acct.Adversary(id) || s.abandoned(id) || s.core.env.Roster.Profile(id).Kind != profile.Wallet {
			continue
		}
		out = append(out, id)
	}
	return out
}

// Victims implements adversary.Sight: the first maxVictims settled honest
// outputs no intent holds, by Ref.
func (s *Sight) Victims() []adversary.Coin {
	var out []adversary.Coin
	for _, u := range s.core.env.Spend.Settled() {
		if s.acct.Adversary(u.Owner) || u.Owner == s.core.spec.Faucet {
			continue
		}
		if _, held := s.core.env.Spend.Holder(u.Ref); held {
			continue
		}
		out = append(out, adversary.Coin{Ref: adversary.Ref{Tx: u.Ref.Tx, Index: u.Ref.Index}, Owner: u.Owner, Lovelace: u.Lovelace})
	}
	slices.SortFunc(out, func(a, b adversary.Coin) int { return cmpRef(a.Ref, b.Ref) })
	return out[:min(len(out), maxVictims)]
}

func (s *Sight) abandoned(id population.ActorID) bool {
	a, ok := s.core.env.Roster.Get(id)
	return !ok || a.State == population.Abandoned
}
