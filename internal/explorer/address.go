package explorer

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// addressOf builds an address's view; p pages the outputs and the history
// alike.
func addressOf(st *state, s string, p Page) (AddressView, error) {
	addr, err := common.NewAddress(s)
	if err != nil {
		return AddressView{}, fmt.Errorf("%w: address %q: %v", ErrBadKey, s, err)
	}
	raw, err := addr.Bytes()
	if err != nil {
		return AddressView{}, fmt.Errorf("%w: address %q: %v", ErrBadKey, s, err)
	}
	key := string(raw)
	refs := st.utxos.at(key)
	txs := st.hist.of(key)
	if len(refs) == 0 && len(txs) == 0 {
		return AddressView{}, fmt.Errorf("%w: address %s", ErrNotFound, s)
	}
	p = p.norm()
	v := AddressView{Address: addr.String(), Assets: []AssetView{}, Utxos: []OutputView{}, History: []TxRef{}, UtxoTotal: len(refs), TxCount: len(txs), Offset: p.Offset, Limit: p.Limit}
	if st := addr.StakeAddress(); st != nil {
		v.Stake = st.String()
	}
	sumUtxos(st, &v, refs, p)
	fillHistory(st, &v, txs, p)
	return v, nil
}

// sumUtxos totals every output at the address (up to maxScan) and lists the
// requested page of them.
func sumUtxos(st *state, v *AddressView, refs []ref, p Page) {
	tot := map[[2]string]*big.Int{}
	lo, hi := p.bounds(len(refs))
	for i, r := range refs[:min(len(refs), maxScan)] {
		o, err := decodeOut(st.utxos.m[r].cbor)
		if err != nil {
			continue
		}
		ov := outputView(hex.EncodeToString(r.tx[:]), r.idx, o)
		v.Balance += ov.Lovelace
		for _, a := range ov.Assets {
			k := [2]string{a.Policy, a.Name}
			if tot[k] == nil {
				tot[k] = new(big.Int)
			}
			q, _ := new(big.Int).SetString(a.Quantity, 10)
			tot[k].Add(tot[k], q)
		}
		if i >= lo && i < hi {
			v.Utxos = append(v.Utxos, ov)
		}
	}
	for k, q := range tot {
		v.Assets = append(v.Assets, AssetView{Policy: k[0], Name: k[1], Quantity: q.String()})
	}
	sortAssets(v.Assets)
}

// fillHistory lists the page of the address's transactions, newest first.
func fillHistory(st *state, v *AddressView, txs [][32]byte, p Page) {
	lo, hi := p.bounds(len(txs))
	for i := lo; i < hi; i++ {
		id := txs[len(txs)-1-i]
		loc, ok := st.txs[id]
		if !ok {
			continue
		}
		m := st.metas[loc.block]
		v.History = append(v.History, TxRef{Hash: hex.EncodeToString(id[:]), Block: m.hashHex(), Height: m.height, Slot: m.slot})
	}
}
