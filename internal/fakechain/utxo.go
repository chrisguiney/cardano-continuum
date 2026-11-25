package fakechain

import (
	"bytes"
	"cmp"
	"errors"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// TxID is a transaction hash: Blake2b-256 of the body's CBOR.
type TxID = common.Blake2b256

// Ref names a transaction output.
type Ref struct {
	TxID  TxID
	Index uint32
}

func refOf(in common.TransactionInput) Ref { return Ref{TxID: in.Id(), Index: in.Index()} }

func compareRefs(a, b Ref) int {
	if c := bytes.Compare(a.TxID[:], b.TxID[:]); c != 0 {
		return c
	}
	return cmp.Compare(a.Index, b.Index)
}

var errNoUtxo = errors.New("fakechain: no such output")

// utxoSet is the confirmed UTxO set.
type utxoSet map[Ref]common.Utxo

func (s utxoSet) sorted() []common.Utxo {
	refs := make([]Ref, 0, len(s))
	for r := range s {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, compareRefs)
	out := make([]common.Utxo, len(refs))
	for i, r := range refs {
		out[i] = s[r]
	}
	return out
}

// overlay is a UTxO set with a run of not-yet-confirmed transactions applied
// on top of it: the view a mempool or a block under assembly validates
// against.
type overlay struct {
	base  utxoSet
	spent map[Ref]struct{}
	added map[Ref]common.Utxo
}

func newOverlay(base utxoSet) *overlay {
	return &overlay{base: base, spent: map[Ref]struct{}{}, added: map[Ref]common.Utxo{}}
}

func (o *overlay) get(r Ref) (common.Utxo, bool) {
	if _, gone := o.spent[r]; gone {
		return common.Utxo{}, false
	}
	if u, ok := o.added[r]; ok {
		return u, true
	}
	u, ok := o.base[r]
	return u, ok
}

func (o *overlay) apply(tx *conway.ConwayTransaction) {
	for _, in := range tx.Inputs() {
		o.spent[refOf(in)] = struct{}{}
	}
	for _, u := range tx.Produced() {
		o.added[refOf(u.Id)] = u
	}
}

// undo records what applying one block changed, so a rollback can reverse it.
type undo struct {
	spent   []common.Utxo
	created []Ref
	fees    uint64
}

// applyTx moves tx into s and records the change in u. tx has already passed
// validation against s, so every input is present.
func (s utxoSet) applyTx(tx *conway.ConwayTransaction, u *undo) {
	for _, in := range tx.Inputs() {
		r := refOf(in)
		u.spent = append(u.spent, s[r])
		delete(s, r)
	}
	for _, out := range tx.Produced() {
		r := refOf(out.Id)
		s[r] = out
		u.created = append(u.created, r)
	}
	u.fees += tx.Fee().Uint64()
}

func (s utxoSet) revert(u undo) {
	for _, r := range u.created {
		delete(s, r)
	}
	for _, out := range u.spent {
		s[refOf(out.Id)] = out
	}
}
