package fakechain

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// txpool is the mempool's behaviour over the world. Its methods assume the
// caller holds world.mu.
type txpool struct{ w *world }

// submit validates tx against the ledger and the mempool and adds it.
func (p txpool) submit(tx *conway.ConwayTransaction) error {
	w := p.w
	size := uint64(len(tx.Cbor()))
	if w.pool.bytes+size > w.capBytes {
		return fmt.Errorf("%w: %d + %d bytes over %d", ErrMempoolFull, w.pool.bytes, size, w.capBytes)
	}
	slot := p.slot()
	if fs := w.check(tx, slot, w.pool.view); len(fs) > 0 {
		return &RejectError{Tx: tx.Hash(), Failures: fs}
	}
	w.pool.push(tx, slot, effectsOf(w))
	return nil
}

// slot is the slot a mempool validates at: the one after the tip, as
// the node's mempool ticks its ledger state.
func (p txpool) slot() uint64 {
	if len(p.w.blocks) == 0 {
		return 0
	}
	return p.w.blocks[len(p.w.blocks)-1].Slot + 1
}

func (p txpool) evict(tx *conway.ConwayTransaction, why Reason, fs []Failure) {
	w := p.w
	w.pool.remove([]*conway.ConwayTransaction{tx})
	w.evicted = append(w.evicted, Eviction{Tx: tx.Hash(), Slot: w.slot, Reason: why, Failures: fs})
}

// revalidate rebuilds the mempool against the current tip, in order,
// evicting what no longer applies.
func (p txpool) revalidate() {
	w := p.w
	old := w.pool.txs
	w.pool = newMempool(w.utxo, w.certs)
	slot := p.slot()
	r := effectsOf(w)
	for _, tx := range old {
		if fs := w.check(tx, slot, w.pool.view); len(fs) > 0 {
			w.evicted = append(w.evicted, Eviction{Tx: tx.Hash(), Slot: w.slot, Reason: ReasonInvalid, Failures: fs})
			continue
		}
		w.pool.push(tx, slot, r)
	}
}
