package fakechain

import (
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// mempool is an ordered list of transactions that apply, in order, on top of
// the confirmed UTxO set; view is that set with all of them applied.
type mempool struct {
	txs   []*conway.ConwayTransaction
	bytes uint64
	view  *overlay
}

func newMempool(base utxoSet) mempool { return mempool{view: newOverlay(base)} }

func (m *mempool) push(tx *conway.ConwayTransaction) {
	m.txs = append(m.txs, tx)
	m.bytes += uint64(len(tx.Cbor()))
	m.view.apply(tx)
}

func (m *mempool) find(id TxID) *conway.ConwayTransaction {
	for _, tx := range m.txs {
		if tx.Hash() == id {
			return tx
		}
	}
	return nil
}

// pick returns the named transactions in the given order, skipping any no
// longer held.
func (m *mempool) pick(ids []TxID) []*conway.ConwayTransaction {
	var out []*conway.ConwayTransaction
	for _, id := range ids {
		if tx := m.find(id); tx != nil {
			out = append(out, tx)
		}
	}
	return out
}

// remove deletes txs from the list. The view is left stale; callers
// revalidate afterwards, which rebuilds it.
func (m *mempool) remove(txs []*conway.ConwayTransaction) {
	if len(txs) == 0 {
		return
	}
	gone := make(map[TxID]bool, len(txs))
	for _, tx := range txs {
		gone[tx.Hash()] = true
	}
	kept := m.txs[:0:0]
	m.bytes = 0
	for _, tx := range m.txs {
		if !gone[tx.Hash()] {
			kept = append(kept, tx)
			m.bytes += uint64(len(tx.Cbor()))
		}
	}
	m.txs = kept
}
