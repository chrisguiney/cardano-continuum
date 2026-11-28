package fakechain

import (
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/ouroboros-mock/fixtures"

	"github.com/chrisguiney/cardano-continuum/internal/check"
)

// producer forges blocks and rolls them back. Its methods assume the caller
// holds world.mu.
type producer struct{ w *world }

// step fires the faults due at w.slot, then the block if one is due.
func (p producer) step() error {
	w := p.w
	for len(w.faults) > 0 && w.faults[0].Slot == w.slot {
		f := w.faults[0]
		w.faults = w.faults[1:]
		if err := f.Fault.apply(w); err != nil {
			return fmt.Errorf("fakechain: fault %T at slot %d: %w", f.Fault, f.Slot, err)
		}
	}
	if w.nextBlock != w.slot {
		return nil
	}
	w.nextBlock += w.cfg.BlockEvery
	return p.produce()
}

// produce assembles, applies and records the block at w.slot.
func (p producer) produce() error {
	w := p.w
	u := undo{certs: w.certs.clone()}
	r := effectsOf(w)
	// The epoch transition runs before the block's transactions are
	// chosen, so they are validated against the state after it, as the
	// ledger's TICK precedes the block body: a vote in the first block of
	// an epoch on a change the transition removed is refused.
	newEpoch(w.certs, w.cfg.Slots.EpochOfSlot(w.slot), len(w.blocks) == 0, w.utxo.stakeByCred())
	txs := p.assemble()
	for _, tx := range txs {
		w.utxo.applyTx(tx, &u)
		w.certs.apply(tx, w.slot, r)
	}
	w.fees += u.fees
	b, err := p.encode(txs)
	if err != nil {
		return err
	}
	w.blocks = append(w.blocks, record{Block: b, txs: txs, undo: u})
	p.settle()
	txpool{w}.revalidate()
	check.PostFn(p.conserved, "fakechain: supply conserved")
	return nil
}

// assemble chooses the block's transactions: the Include list if one is
// pending, otherwise the mempool in order. Each is revalidated at the block
// slot against the block so far; failures are evicted. Assembly stops at the
// first transaction that does not fit the body size or the execution
// budget, as the node takes the longest mempool prefix within the block
// capacity (ouroboros-consensus Mempool/Impl/Common.hs, splitAfterTxSize
// over blockCapacityTxMeasure, at 136122cc). So a transaction within the
// per-transaction budget but over the per-block one is never forged and
// holds back every later one until it leaves the mempool.
func (p producer) assemble() []*conway.ConwayTransaction {
	w := p.w
	cands, overfull := w.pool.txs, w.overfull
	w.overfull = false
	if w.include != nil {
		cands = w.pool.pick(w.include)
		w.include = nil
	}
	view := newOverlay(w.utxo, w.certs)
	r := effectsOf(w)
	var chosen []*conway.ConwayTransaction
	var used blockUse
	for _, tx := range cands {
		if fs := w.check(tx, w.slot, view); len(fs) > 0 {
			txpool{w}.evict(tx, ReasonInvalid, fs)
			continue
		}
		next := used.add(tx)
		if !overfull && !next.fits(w.pp) {
			break
		}
		used = next
		view.apply(tx, w.slot, r)
		chosen = append(chosen, tx)
	}
	w.pool.remove(chosen)
	return chosen
}

// settle makes the block k deep immutable, if the chain is long enough, and
// forgets the undo records of the blocks it newly covers.
func (p producer) settle() {
	w := p.w
	n := len(w.blocks)
	if n <= int(w.cfg.K) {
		return
	}
	to := n - int(w.cfg.K)
	for i := w.immutable; i < to; i++ {
		w.blocks[i].undo = undo{}
		w.blocks[i].txs = nil
	}
	w.immutable = max(w.immutable, to)
}

func (p producer) conserved() bool {
	w := p.w
	var sum uint64
	for _, u := range w.utxo {
		sum += u.Output.Amount().Uint64()
	}
	return sum+w.fees+w.certs.deposits() == w.supply
}

// rollback removes the last depth blocks. It refuses, changing nothing, a
// depth that would remove an immutable block.
func (p producer) rollback(depth uint64, readd bool) error {
	w := p.w
	if depth == 0 || depth > uint64(len(w.blocks)-w.immutable) {
		return fmt.Errorf("%w: depth %d, height %d, immutable %d", ErrTooDeep, depth, len(w.blocks), w.immutable)
	}
	cut := len(w.blocks) - int(depth)
	var back []*conway.ConwayTransaction
	for i := len(w.blocks) - 1; i >= cut; i-- {
		w.utxo.revert(w.blocks[i].undo)
		w.fees -= w.blocks[i].undo.fees
		w.certs = w.blocks[i].undo.certs
	}
	for _, r := range w.blocks[cut:] {
		back = append(back, r.txs...)
	}
	w.blocks = slices.Clip(w.blocks[:cut])
	if readd {
		w.pool.txs = append(back, w.pool.txs...)
	}
	txpool{w}.revalidate()
	check.PostFn(p.conserved, "fakechain: supply conserved")
	return nil
}

// encode builds the Conway block at w.slot holding txs.
func (p producer) encode(txs []*conway.ConwayTransaction) (Block, error) {
	w := p.w
	var prev common.Blake2b256
	if n := len(w.blocks); n > 0 {
		prev = w.blocks[n-1].Hash
	}
	number := uint64(len(w.blocks)) + 1
	gtxs := make([]common.Transaction, len(txs))
	ids := make([]TxID, len(txs))
	for i, tx := range txs {
		gtxs[i] = tx
		ids[i] = tx.Hash()
	}
	// The header claims the version the tip has enacted, as a node that
	// writes its ledger's version (dingo) does.
	pv := w.certs.enact.params.ProtocolVersion
	b, err := fixtures.NewBlockBuilder(conway.EraConway).
		WithBlockNumber(number).
		WithSlot(w.slot).
		WithPreviousHash(prev).
		WithIssuerVkey(w.cfg.Issuer).
		WithProtocolVersion(uint64(pv.Major), uint64(pv.Minor)).
		WithTransactions(gtxs...).
		Build()
	if err != nil {
		return Block{}, fmt.Errorf("fakechain: build block %d at slot %d: %w", number, w.slot, err)
	}
	return Block{
		Number:   number,
		Slot:     w.slot,
		Hash:     b.Hash(),
		PrevHash: prev,
		Cbor:     b.Cbor(),
		Txs:      ids,
	}, nil
}
