package explorer

import (
	"encoding/hex"
	"fmt"
)

func blocksOf(st *state, q BlockQuery) BlockPage {
	p := q.norm()
	out := BlockPage{Offset: p.Offset, Limit: p.Limit, Rows: []BlockRow{}, Limits: st.cfg.Limits}
	var pick func(i int) *meta
	if q.Pool != "" {
		id, err := parsePool(q.Pool)
		if err != nil {
			return out
		}
		list := st.byIssuer[id]
		out.Total = len(list)
		pick = func(i int) *meta { return st.metas[list[len(list)-1-i]] }
	} else {
		out.Total = len(st.metas)
		pick = func(i int) *meta { return st.metas[len(st.metas)-1-i] }
	}
	lo, hi := q.bounds(out.Total)
	for i := lo; i < hi; i++ {
		out.Rows = append(out.Rows, st.row(pick(i)))
	}
	return out
}

func blockOf(st *state, key string) (BlockView, error) {
	pos, err := st.locate(key)
	if err != nil {
		return BlockView{}, err
	}
	m := st.metas[pos]
	v := BlockView{BlockRow: st.row(m), Txs: make([]TxRow, 0, len(m.txs)), Evicted: m.full == nil}
	for _, t := range m.txs {
		v.Txs = append(v.Txs, TxRow{Hash: hex.EncodeToString(t.id[:]), Fee: t.fee})
	}
	if pos+1 < len(st.metas) {
		v.Next = st.metas[pos+1].hashHex()
	}
	if m.full != nil {
		if blk, err := decodeBlock(m.full.raw); err == nil {
			v.Header = headerOf(blk)
		}
	}
	return v, nil
}

func txOf(st *state, h [32]byte, hash string) (TxView, error) {
	loc, ok := st.txs[h]
	if !ok {
		return TxView{}, fmt.Errorf("%w: transaction %s", ErrNotFound, hash)
	}
	m := st.metas[loc.block]
	base := TxView{
		Hash: hash, Block: m.hashHex(), Height: m.height, Slot: m.slot, Index: loc.idx, Evicted: true, Valid: true, Fee: m.txs[loc.idx].fee,
	}
	if m.full == nil {
		return base, nil
	}
	blk, err := decodeBlock(m.full.raw)
	if err != nil {
		return TxView{}, err
	}
	txs := blk.Transactions()
	if loc.idx >= len(txs) {
		return base, nil
	}
	v := txView(txs[loc.idx], m.full.resolve)
	v.Block, v.Height, v.Slot, v.Index = base.Block, base.Height, base.Slot, loc.idx
	return v, nil
}
