package fakechain

import (
	"fmt"
	"sync"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// world is the fake ledger's state. Chain.mu is the only lock; every part
// (txpool, producer, follow, reader) assumes its caller holds it, except
// reader's exported methods, which take it. The lock is a leaf: nothing
// under it calls out of the package or blocks.
type world struct {
	cfg      Config
	pp       *conway.ConwayProtocolParameters
	ls       *ledgerView
	capBytes uint64

	mu        sync.Mutex // protects slot, nextBlock, faults, utxo, certs, fees, supply, blocks, immutable, pool, include, overfull, evicted, view
	slot      uint64
	nextBlock uint64
	faults    []Scheduled
	utxo      utxoSet
	certs     *certState
	fees      uint64
	supply    uint64
	blocks    []record
	// immutable is the number of leading blocks that can never be rolled
	// back. It only grows, so after a rollback of depth d the next one reaches
	// at most k-d further back until new blocks arrive; blocks[immutable:]
	// keep their undo records.
	immutable int
	pool      mempool
	include   []TxID
	overfull  bool
	evicted   []Eviction
	view      func(Ref) (common.Utxo, bool)
}

// check runs the rule list against view at slot. The caller holds w.mu.
func (w *world) check(tx *conway.ConwayTransaction, slot uint64, view *overlay) []Failure {
	w.view, w.ls.certs = view.get, view.certs
	defer func() { w.view, w.ls.certs = nil, nil }()
	return validate(tx, slot, w.ls, rulesAt(w.pp, view.certs))
}

// rulesAt is pp, the configured protocol parameters at the protocol version s
// has enacted: the version the ledger rules (gouroboros gates its PV11
// rules on it) and the HardForkInitiation pvCanFollow check read. Other
// enacted parameter changes do not reach validation.
func rulesAt(pp *conway.ConwayProtocolParameters, s *certState) *conway.ConwayProtocolParameters {
	out := *pp
	out.ProtocolVersion = s.enact.params.ProtocolVersion
	return &out
}

func loadGenesis(w *world, gs []GenesisOutput) error {
	for _, g := range gs {
		if g.Address.NetworkId() != uint(w.cfg.NetworkID) {
			return fmt.Errorf("fakechain: genesis address %s is not on network %d", g.Address, w.cfg.NetworkID)
		}
		u, err := genesisUtxo(g)
		if err != nil {
			return err
		}
		r := refOf(u.Id)
		if _, dup := w.utxo[r]; dup {
			return fmt.Errorf("fakechain: genesis address %s appears twice", g.Address)
		}
		w.utxo[r] = u
		w.supply += g.Lovelace
	}
	return nil
}

// newLedgerView adapts the fake to the common.LedgerState the gouroboros
// rules read: UTxO lookup, network id and slot arithmetic here, delegation
// and governance state in ledgerView.
//
// The purpose roots are the last enacted parameter change and hard fork of
// the cert state being checked; the fake enacts no other purpose.
func newLedgerView(w *world) *ledgerView {
	lv := &ledgerView{}
	g := w.cfg.Slots
	start := time.Unix(0, 0).UTC()
	ls := mockledger.NewLedgerStateBuilder().
		WithNetworkId(uint(w.cfg.NetworkID)).
		WithUtxoById(func(in common.TransactionInput) (common.Utxo, error) {
			if u, ok := w.view(refOf(in)); ok {
				return u, nil
			}
			return common.Utxo{}, errNoUtxo
		}).
		WithEpochForSlot(func(slot uint64) (uint64, error) { return g.EpochOfSlot(slot), nil }).
		WithSlotToTime(func(slot uint64) (time.Time, error) {
			return start.Add(time.Duration(g.SlotStart(slot)) * time.Millisecond), nil
		}).
		WithTimeToSlot(func(t time.Time) (uint64, error) {
			return g.SlotAt(sched.Time(t.Sub(start).Milliseconds())), nil
		}).
		WithConstitutionValue(&common.Constitution{}).
		WithGovPurposeRoots(func() (*common.GovPurposeRoots, error) {
			return &common.GovPurposeRoots{PParamUpdate: lv.certs.enact.root, HardFork: lv.certs.enact.hardFork}, nil
		}).
		Build()
	lv.MockLedgerState = ls
	return lv
}

// effectsOf returns what certState.apply needs. A proposal can be voted on up
// to the end of the epoch govActionLifetime epochs after the one it was made
// in.
func effectsOf(w *world) effects {
	g, life := w.cfg.Slots, uint64(w.pp.GovActionValidityPeriod)
	return effects{
		pp: w.pp,
		expiry: func(slot uint64) uint64 {
			return g.SlotAt(g.EpochStart(g.EpochOfSlot(slot)+life+1)) - 1
		},
		epochOf: g.EpochOfSlot,
	}
}
