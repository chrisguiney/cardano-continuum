package explorer

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// applier folds one block's transactions into the index while Add builds
// its entry. The caller holds the index lock.
type applier struct {
	st *state
	m  *meta
}

// tx applies one transaction: it spends what Consumed names (the collateral
// of an invalid transaction), creates what Produced names, and notes each
// address it touches.
func (a *applier) tx(tx common.Transaction) {
	id := [32]byte(tx.Hash())
	for _, in := range tx.Consumed() {
		a.spend(id, refOf(in))
	}
	for _, u := range tx.Produced() {
		a.create(id, refOf(u.Id), u.Output)
	}
	fee := txFee(tx)
	mem, steps := exUnits(tx)
	a.m.txs = append(a.m.txs, txMeta{id: id, fee: fee})
	a.m.fees += fee
	a.m.exMem += mem
	a.m.exSteps += steps
}

func (a *applier) spend(tx [32]byte, r ref) {
	o, ok := a.st.utxos.take(r)
	if !ok {
		return
	}
	a.m.full.inputs[r] = o.cbor
	a.touch(o.addr, tx)
}

func (a *applier) create(tx [32]byte, r ref, o common.TransactionOutput) {
	addr, err := o.Address().Bytes()
	if err != nil {
		return
	}
	raw := o.Cbor()
	a.st.utxos.put(r, outRec{cbor: raw, addr: string(addr)})
	a.m.full.born[r] = struct{}{}
	a.touch(string(addr), tx)
}

func (a *applier) touch(addr string, tx [32]byte) {
	if a.st.hist.add(addr, tx) {
		a.m.full.touched = append(a.m.full.touched, addrTx{addr: addr, tx: tx})
	}
}
