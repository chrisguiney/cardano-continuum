package intent

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func gid(b byte) chain.GovID { return chain.GovID{Tx: chain.TxID{b}} }

// The proposer changes a parameter, then reverts it naming the change, then
// moves to the next parameter; a link that did not pass is proposed again
// from the same root, and nothing is proposed beside an open link.
func TestPlanParamsChangesThenReverts(t *testing.T) {
	first, ok := planParams(nil)
	if !ok || first.prev != nil || first.update.A0 == nil || first.update.A0.Cmp(big.NewRat(1, 4)) != 0 {
		t.Fatalf("first proposal %+v: want a0 to 1/4 after no root", first)
	}
	if _, ok := planParams([]paramLink{{id: gid(1)}}); ok {
		t.Fatal("proposed beside an open parameter change")
	}
	revert, ok := planParams([]paramLink{{id: gid(1), closed: true, enacted: true}})
	if !ok || revert.prev == nil || *revert.prev != gid(1) || revert.update.A0 == nil || revert.update.A0.Cmp(big.NewRat(3, 10)) != 0 {
		t.Fatalf("after the change enacted %+v: want a0 back to 3/10 after it", revert)
	}
	again, _ := planParams([]paramLink{{id: gid(1), closed: true}})
	if again.prev != nil || again.update.A0 == nil || again.update.A0.Cmp(big.NewRat(1, 4)) != 0 {
		t.Fatalf("after the change failed %+v: want it again after no root", again)
	}
	next, _ := planParams([]paramLink{{id: gid(1), closed: true, enacted: true}, {id: gid(2), closed: true, enacted: true}})
	if next.prev == nil || *next.prev != gid(2) || next.update.A0 != nil || next.update.Tau == nil {
		t.Fatalf("after change and revert %+v: want the next parameter after the revert", next)
	}
}

// govChain builds views for the parameter-change tests: actors 1 and 2
// hold stake credentials 1 and 2, DReps 8 and 7, committee seat 0 (hot key
// 50). Epochs are 100 slots.
type govChain struct {
	t    *testing.T
	view *chain.View
	tip  *chain.Block
	n    uint64
}

func hash(b byte) common.Blake2b224 {
	var h common.Blake2b224
	for i := range h {
		h[i] = b
	}
	return h
}

func newGovChain(t *testing.T, stake1, stake2 uint64) *govChain {
	t.Helper()
	b := chain.NewBook()
	for _, n := range []byte{1, 2} {
		if err := b.AddAddress(addr(t, n), population.ActorID(n)); err != nil {
			t.Fatal(err)
		}
		b.AddStake(hash(n), population.ActorID(n))
	}
	b.AddDRep(hash(8), 8)
	b.AddDRep(hash(7), 7)
	b.AddCommittee(hash(50), 0)

	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}

	v, err := chain.New(chain.Config{K: 10, Slots: slots, Book: b, GovLifetime: 1}, chain.State{})
	if err != nil {
		t.Fatal(err)
	}

	g := &govChain{
		t:    t,
		view: v,
	}
	g.block(1, g.pay(1, 1, stake1), g.pay(2, 2, stake2), chain.Tx{Hash: txHash(3), Certs: []common.Certificate{
		&common.StakeRegistrationCertificate{StakeCredential: cred(1)},
		&common.StakeRegistrationCertificate{StakeCredential: cred(2)},
		&common.RegistrationDrepCertificate{DrepCredential: cred(8)},
		&common.RegistrationDrepCertificate{DrepCredential: cred(7)},
		&common.VoteDelegationCertificate{StakeCredential: cred(1), Drep: drep(8)},
		&common.VoteDelegationCertificate{StakeCredential: cred(2), Drep: drep(7)},
	}})
	return g
}

func addr(t *testing.T, n byte) common.Address {
	t.Helper()
	h := hash(n)
	a, err := common.NewAddressFromParts(common.AddressTypeKeyKey, common.AddressNetworkTestnet, h[:], h[:])
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func cred(n byte) common.Credential { return common.Credential{Credential: hash(n)} }

func drep(n byte) common.Drep {
	h := hash(n)
	return common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: h[:]}
}

func txHash(n byte) chain.TxID { return chain.TxID{0xee, n} }

// pay is a transaction creating coin lovelace at actor n's address.
func (g *govChain) pay(tx, n byte, coin uint64) chain.Tx {
	out := babbage.BabbageTransactionOutput{
		OutputAddress: addr(g.t, n),
		OutputAmount:  mary.MaryTransactionOutputValue{Amount: coin},
	}
	u := common.Utxo{
		Id:     shelley.ShelleyTransactionInput{TxId: txHash(tx), OutputIndex: 0},
		Output: out,
	}
	return chain.Tx{Hash: txHash(tx), Creates: []common.Utxo{u}}
}

// block applies a block at slot with txs.
func (g *govChain) block(slot uint64, txs ...chain.Tx) {
	g.t.Helper()
	g.n++
	b := chain.Block{
		Point:  chain.Point{Slot: slot, Hash: common.Blake2b256{0xbb, byte(g.n)}},
		Number: g.n,
		Txs:    txs,
	}
	if g.tip != nil {
		b.Prev = g.tip.Point.Hash
	}

	if _, err := g.view.Apply(b); err != nil {
		g.t.Fatal(err)
	}
	g.tip = &b
}

// The ledger tallies a parameter change with the stake at the boundary it
// ratifies at, so stake moving afterwards does not undo the enactment:
// the revert still names the change (ParamPowerMovesBug in
// models/specs/governance-change). DRep 8 (800 of 1000) and the seat vote
// yes in epoch 1; the boundary into epoch 2 enacts; then actor 2's DRep
// gains 600, which would sink the yes share to a half.
func TestPlanParamsIgnoresStakeMovedAfterEnactment(t *testing.T) {
	t.Parallel()
	g := newGovChain(t, 800, 200)
	change := chain.GovID{Tx: txHash(10)}
	g.block(101, chain.Tx{
		Hash:      txHash(10),
		Proposals: []chain.Proposal{{ID: change, Type: "parameter_change"}},
		Votes: []chain.Vote{
			{Voter: chain.Voter{Kind: common.VoterTypeDRepKeyHash, Hash: hash(8)}, Action: change, Choice: common.GovVoteYes},
			{Voter: chain.Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash(50)}, Action: change, Choice: common.GovVoteYes},
		},
	})
	g.block(201, g.pay(11, 2, 600))
	g.block(301)

	revert, ok := planParams(paramLinks(g.view))
	if !ok || revert.prev == nil || *revert.prev != change {
		t.Fatalf("after the change enacted and stake moved: %+v, ok %t; want the revert naming %s", revert, ok, change)
	}
}
