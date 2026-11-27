package fakechain

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

func h(b byte) common.Blake2b224 {
	var x common.Blake2b224
	for i := range x {
		x[i] = b
	}
	return x
}

func cred(b byte) credKey { return credKey{kind: common.CredentialTypeAddrKeyHash, hash: h(b)} }

func TestEpochTransition(t *testing.T) {
	s := newCertState(nil, DefaultParams())
	id := common.PoolKeyHash(h(9))
	s.accounts[cred(1)] = &account{pool: &id}
	s.accounts[cred(2)] = &account{} // registered, delegated nowhere
	s.pools[id] = &pool{}
	stake := map[common.Blake2b224]uint64{h(1): 70, h(2): 30}

	newEpoch(s, 0, true, stake) // first block: no earlier epoch to close
	if s.epoch.label != 0 || s.epoch.mark != nil {
		t.Fatalf("first block rotated: %+v", s.epoch)
	}
	newEpoch(s, 1, false, stake)
	want := []Share{{Cred: h(1), Pool: h(9), Stake: 70}}
	if len(s.epoch.mark) != 1 || s.epoch.mark[0] != want[0] || s.epoch.set != nil {
		t.Fatalf("epoch 1: %+v", s.epoch)
	}
	// Retirement applies at its epoch, after the snapshot took the stake.
	e := uint64(2)
	s.pools[id].retire = &e
	newEpoch(s, 2, false, stake)
	if len(s.epoch.mark) != 1 || len(s.epoch.set) != 1 {
		t.Fatalf("epoch 2 snapshots: %+v", s.epoch)
	}
	if _, ok := s.pools[id]; ok || !s.epoch.retired[id] || s.accounts[cred(1)].pool != nil {
		t.Fatal("pool not retired or wallet still delegated")
	}
	saved := s.clone()
	newEpoch(s, 4, false, stake) // epoch 3 had no block
	if !s.epoch.frozen || s.epoch.label != 2 {
		t.Fatalf("not frozen: %+v", s.epoch)
	}
	if saved.epoch.frozen {
		t.Fatal("clone shares epoch state")
	}
}

// govWorld is a cert state with one DRep holding all delegated stake, a
// three-seat committee with hot keys, and a proposal helper.
type govWorld struct {
	s     *certState
	stake map[common.Blake2b224]uint64
}

func newGovWorld() govWorld {
	pp := DefaultParams()
	pp.MinCommitteeSize = 3
	s := newCertState([]CommitteeSeat{{Cold: h(31), Expiry: 100}, {Cold: h(32), Expiry: 100}, {Cold: h(33), Expiry: 100}}, pp)
	for i, k := range sortedCreds(s.committee) {
		hot := cred(byte(41 + i))
		s.committee[k].hot = &hot
	}
	s.dreps[cred(20)] = common.DRepRegistration{}
	s.accounts[cred(1)] = &account{drep: &common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: h(20).Bytes()}}
	return govWorld{s: s, stake: map[common.Blake2b224]uint64{h(1): 100}}
}

// propose adds a parameter change setting a0 to a0, after prev, voted on by
// the DRep and by yes of the committee's hot keys.
func (g govWorld) propose(tx byte, prev *common.GovActionId, a0 *big.Rat, drepYes bool, yes int) common.GovActionId {
	id := common.GovActionId{TransactionId: common.Blake2b256{tx}}
	act := &conway.ConwayParameterChangeGovAction{ActionId: prev, ParamUpdate: conway.ConwayProtocolParameterUpdate{A0: &cbor.Rat{Rat: a0}}}
	g.s.proposals[id] = proposal{state: common.GovActionState{ActionId: id, Action: act}, expires: 10}
	if drepYes {
		g.s.votes[voteKey{id, common.VoterTypeDRepKeyHash, h(20)}] = common.GovVoteYes
	}
	for i := range yes {
		g.s.votes[voteKey{id, common.VoterTypeConstitutionalCommitteeHotKeyHash, h(byte(41 + i))}] = common.GovVoteYes
	}
	return id
}

// A parameter change with the DRep and two of three seats in favour enacts
// at the next epoch boundary; a revert naming it enacts at the one after;
// a change naming a stale root, or short of the committee's quorum, or of
// the DRep's, does not.
func TestParameterChangeEnactsThenReverts(t *testing.T) {
	g := newGovWorld()
	base := g.s.enact.params.A0.Rat
	newEpoch(g.s, 0, true, g.stake)
	change := g.propose(1, nil, big.NewRat(1, 4), true, 2)
	g.propose(2, nil, big.NewRat(1, 5), true, 1)
	newEpoch(g.s, 1, false, g.stake)
	if got := g.s.enact.params.A0.Rat; got.Cmp(big.NewRat(1, 4)) != 0 || !sameAction(g.s.enact.root, &change) {
		t.Fatalf("epoch 1: a0 %v, root %v; want 1/4 enacted by the change", got, g.s.enact.root)
	}
	stale := g.propose(3, nil, big.NewRat(1, 10), true, 3)
	revert := g.propose(4, &change, base, true, 2)
	newEpoch(g.s, 2, false, g.stake)
	if got := g.s.enact.params.A0.Rat; got.Cmp(base) != 0 || !sameAction(g.s.enact.root, &revert) {
		t.Fatalf("epoch 2: a0 %v, root %v; want %v back, enacted by the revert", got, g.s.enact.root, base)
	}
	if g.s.proposals[stale].enacted {
		t.Fatal("a change naming a stale root enacted")
	}
	g.propose(5, &revert, big.NewRat(1, 3), false, 3)
	newEpoch(g.s, 3, false, g.stake)
	if g.s.enact.params.A0.Cmp(base) != 0 {
		t.Fatal("a change without the DRep's vote enacted")
	}
}
