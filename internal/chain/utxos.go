package chain

import (
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// utxos is the view's outputs: every output paying an address the book
// names, its owner, and each owner's balance, which add and remove keep
// equal to the sum of its outputs.
type utxos struct {
	book    *Book
	utxo    map[Ref]common.Utxo
	owner   map[Ref]population.ActorID
	balance map[population.ActorID]uint64
}

func newUtxos(b *Book) utxos {
	return utxos{
		book:    b,
		utxo:    map[Ref]common.Utxo{},
		owner:   map[Ref]population.ActorID{},
		balance: map[population.ActorID]uint64{},
	}
}

func (s utxos) clone() utxos {
	s.utxo = maps.Clone(s.utxo)
	s.owner = maps.Clone(s.owner)
	s.balance = maps.Clone(s.balance)
	return s
}

// Utxo returns the tracked output at r.
func (s *utxos) Utxo(r Ref) (common.Utxo, bool) {
	u, ok := s.utxo[r]
	return u, ok
}

// UtxosAt returns the tracked outputs paying a, sorted by reference.
func (s *utxos) UtxosAt(a common.Address) []common.Utxo {
	want, err := addrKey(a)
	if err != nil {
		return nil
	}
	var out []common.Utxo
	for _, u := range s.sorted() {
		if k, err := addrKey(u.Output.Address()); err == nil && k == want {
			out = append(out, u)
		}
	}
	return out
}

// Balance is the lovelace in the actor's tracked outputs.
func (s *utxos) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(s.balance[id])
}

// Owned returns the tracked outputs by owning actor, each list sorted by
// reference. It walks every output once, so a caller wanting several
// actors asks once.
func (s *utxos) Owned() map[population.ActorID][]common.Utxo {
	out := map[population.ActorID][]common.Utxo{}
	for _, u := range s.sorted() {
		if id, ok := s.owner[RefOf(u.Id)]; ok {
			out[id] = append(out[id], u)
		}
	}
	return out
}

// add stores u if the book names its address, and reports whether it did.
func (s *utxos) add(u common.Utxo) bool {
	id, ok := s.book.owner(u.Output.Address())
	if !ok {
		return false
	}
	r := RefOf(u.Id)
	s.utxo[r] = u
	s.owner[r] = id
	s.balance[id] += u.Output.Amount().Uint64()
	return true
}

func (s *utxos) remove(r Ref) {
	u, ok := s.utxo[r]
	if !ok {
		return
	}
	id := s.owner[r]
	s.balance[id] -= u.Output.Amount().Uint64()
	if s.balance[id] == 0 {
		delete(s.balance, id)
	}
	delete(s.utxo, r)
	delete(s.owner, r)
}

func (s *utxos) sorted() []common.Utxo {
	refs := slices.Collect(maps.Keys(s.utxo))
	slices.SortFunc(refs, Ref.Compare)
	var out []common.Utxo
	for _, r := range refs {
		out = append(out, s.utxo[r])
	}
	return out
}

// consistent reports whether the balance index matches the outputs.
func (s *utxos) consistent() bool {
	sum := map[population.ActorID]uint64{}
	for r, u := range s.utxo {
		sum[s.owner[r]] += u.Output.Amount().Uint64()
	}
	return maps.Equal(sum, s.balance)
}

// credStake is the lovelace in tracked outputs per stake credential of the
// output's address.
func (s *utxos) credStake() map[common.Blake2b224]uint64 {
	out := map[common.Blake2b224]uint64{}
	for _, u := range s.utxo {
		addr := u.Output.Address()
		if cred := addr.StakeKeyHash(); cred != (common.Blake2b224{}) {
			out[cred] += u.Output.Amount().Uint64()
		}
	}
	return out
}
