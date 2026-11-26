package plan

import (
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// vtx is a transaction in a block above the settled point. Creates holds
// only outputs whose address has an owner.
type vtx struct {
	id      TxID
	spends  []Ref
	creates []Utxo
}

type vblock struct {
	point chain.Point
	txs   []vtx
}

// view is the ledger's view of the chain: the settled point and the blocks
// above it. It owns the model's view and vfloor: the settled point is a
// high-water mark that only pop raises and rollback never crosses, and
// state.forward pops until at most k blocks remain above it.
type view struct {
	k       uint64
	settled chain.Point
	blocks  []vblock // blocks above the settled point, oldest first
	// spent caches spentAbove. It is never modified once built, so clones
	// share it; nil means the blocks changed since. Rebuilding it for each
	// query cost a scan of every block transaction.
	spent map[Ref]struct{}
}

// clone copies the block list; blocks are never modified once made, so
// they are shared.
func (v *view) clone() view {
	c := *v
	c.blocks = slices.Clone(v.blocks)
	return c
}

func (v *view) tip() chain.Point {
	if n := len(v.blocks); n > 0 {
		return v.blocks[n-1].point
	}
	return v.settled
}

// spentAbove is every output a block above the settled point spends.
func (v *view) spentAbove() map[Ref]struct{} {
	if v.spent != nil {
		return v.spent
	}
	out := map[Ref]struct{}{}
	for _, b := range v.blocks {
		for _, tx := range b.txs {
			for _, r := range tx.spends {
				out[r] = struct{}{}
			}
		}
	}
	v.spent = out
	return out
}

// extend appends b, which must connect to the tip, keeping of its outputs
// only those o names an owner for.
func (v *view) extend(b chain.Block, o Owners) error {
	tip := v.tip()
	if !tip.IsOrigin() && (b.Prev != tip.Hash || b.Point.Slot <= tip.Slot) {
		return fmt.Errorf("%w: block %d at slot %d, tip at slot %d", ErrNotConnected, b.Number, b.Point.Slot, tip.Slot)
	}
	vb := vblock{point: b.Point}
	for _, tx := range b.Txs {
		t := vtx{id: tx.Hash, spends: tx.Spends}
		for _, c := range tx.Creates {
			if owner, ok := o.Owner(c.Output.Address()); ok {
				t.creates = append(t.creates, Utxo{Ref: chain.RefOf(c.Id), Owner: owner, Lovelace: c.Output.Amount().Uint64()})
			}
		}
		vb.txs = append(vb.txs, t)
	}
	v.blocks = append(v.blocks, vb)
	v.spent = nil
	return nil
}

// deep reports whether the oldest block has k successors, so must settle.
func (v *view) deep() bool { return uint64(len(v.blocks)) > v.k }

// pop removes the oldest block and makes it the settled point.
func (v *view) pop() vblock {
	b := v.blocks[0]
	v.blocks = v.blocks[1:]
	v.spent = nil
	v.settled = b.point
	return b
}

// rollback cuts the view back to p, which must be the settled point or a
// block above it.
func (v *view) rollback(p chain.Point) error {
	if p == v.tip() {
		return nil
	}
	keep := -1
	if p == v.settled {
		keep = 0
	}
	for i, b := range v.blocks {
		if b.point == p {
			keep = i + 1
		}
	}
	if keep < 0 {
		return fmt.Errorf("%w: slot %d, settled at slot %d", ErrTooDeep, p.Slot, v.settled.Slot)
	}
	v.blocks = v.blocks[:keep]
	v.spent = nil
	return nil
}

// consistent checks that no block above the settled point is k deep.
func (v *view) consistent() error {
	if uint64(len(v.blocks)) > v.k {
		return fmt.Errorf("%d blocks above the settled point, K is %d", len(v.blocks), v.k)
	}
	return nil
}
