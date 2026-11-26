package explorer

import (
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// outRec is an unspent output: its CBOR and the address it pays, as raw
// address bytes.
type outRec struct {
	cbor []byte
	addr string
}

// utxoSet is every output the index has seen created and not seen spent,
// with the unspent ones of each address. It grows with the chain's unspent
// set, which the engine's own ledger view also holds.
type utxoSet struct {
	m      map[ref]outRec
	byAddr map[string]map[ref]struct{}
}

func newUtxoSet() *utxoSet {
	return &utxoSet{m: map[ref]outRec{}, byAddr: map[string]map[ref]struct{}{}}
}

func (u *utxoSet) put(r ref, o outRec) {
	u.m[r] = o
	set := u.byAddr[o.addr]
	if set == nil {
		set = map[ref]struct{}{}
		u.byAddr[o.addr] = set
	}
	set[r] = struct{}{}
}

func (u *utxoSet) take(r ref) (outRec, bool) {
	o, ok := u.m[r]
	if !ok {
		return outRec{}, false
	}
	delete(u.m, r)
	if set := u.byAddr[o.addr]; set != nil {
		delete(set, r)
		if len(set) == 0 {
			delete(u.byAddr, o.addr)
		}
	}
	return o, true
}

// at lists the unspent refs at an address in a stable order.
func (u *utxoSet) at(addr string) []ref {
	set := u.byAddr[addr]
	out := make([]ref, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b ref) int {
		if c := compareHash(a.tx, b.tx); c != 0 {
			return c
		}
		return int(a.idx) - int(b.idx)
	})
	return out
}

func compareHash(a, b [32]byte) int {
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}

// decodeOut reads an output from CBOR; Babbage's output type reads the
// legacy forms too.
func decodeOut(raw []byte) (common.TransactionOutput, error) {
	var o babbage.BabbageTransactionOutput
	if _, err := cbor.Decode(raw, &o); err != nil {
		return nil, fmt.Errorf("explorer: decode output: %w", err)
	}
	return &o, nil
}
