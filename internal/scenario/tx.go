package scenario

import (
	"sort"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// txIn names a transaction output.
type txIn struct {
	ID    common.Blake2b256
	Index uint32
}

func (i txIn) encode() []byte { return cArray(cBytes(i.ID[:]), cUint(uint64(i.Index))) }

// asset is a quantity of one native token.
type asset struct {
	Policy common.Blake2b224
	Name   []byte
	Qty    int64
}

// txOut is one output: an address, lovelace and at most one token.
type txOut struct {
	Addr  []byte
	Coin  uint64
	Token *asset
	// Datum, when set, is the encoded datum option (field 2).
	Datum []byte
	// Value, when set, replaces the encoded value: codec scenarios write
	// quantities in forms the fields above cannot express.
	Value []byte
}

func (o txOut) encode() []byte {
	val := o.Value
	if val == nil {
		val = cUint(o.Coin)
		if o.Token != nil {
			val = cArray(cUint(o.Coin), cMap(cBytes(o.Token.Policy[:]),
				cMap(cBytes(o.Token.Name), cUint(uint64(o.Token.Qty)))))
		}
	}
	kv := [][]byte{cUint(0), cBytes(o.Addr), cUint(1), val}
	if o.Datum != nil {
		kv = append(kv, cUint(2), o.Datum)
	}
	return cMap(kv...)
}

// Conway transaction body keys (conway.cddl transaction_body).
const (
	bodyInputs     = 0
	bodyOutputs    = 1
	bodyFee        = 2
	bodyTTL        = 3
	bodyCerts      = 4
	bodyWdrl       = 5
	bodyStart      = 8
	bodyMint       = 9
	bodyDataHash   = 11
	bodyCollateral = 13
	bodySigners    = 14
	bodyCollReturn = 16
	bodyTotalColl  = 17
	bodyRefInputs  = 18
)

// Witness set keys (conway.cddl transaction_witness_set).
const (
	witVKeys     = 0
	witNative    = 1
	witRedeemers = 5
	witPlutusV3  = 7
)

// framing is how the transaction's outer array is written.
type framing int

const (
	frameDefinite   framing = iota // 84
	frameWide                      // 98 04
	frameIndefinite                // 9f ... ff
)

// txSpec is a transaction written out field by field. Nothing is checked or
// balanced: the adapter that fills it decides whether it is valid.
type txSpec struct {
	Inputs, Collateral, Refs []txIn
	Outputs                  []txOut
	Fee                      uint64
	TTL, Start               *uint64
	Mint                     *asset
	Certs                    [][]byte
	// Wdrl is the reward account (address bytes) withdrawn from, with Amount.
	Wdrl       []byte
	WdrlAmount uint64
	Return     *txOut
	TotalColl  *uint64
	ReqSigners [][]byte
	DataHash   []byte

	Natives   [][]byte
	PlutusV3  []byte
	Redeemers []byte
	Signers   []keys.Signer
	// BadSigners witness the body with a signature that does not verify.
	BadSigners []keys.Signer
	Invalid    bool

	// WitWrap, when set, rewrites the vkey witness list: codec scenarios
	// put it under a tag.
	WitWrap func(list []byte) []byte
	// Raw replaces the encoding of whole body fields, by key.
	Raw   map[uint64][]byte
	Frame framing
}

func encodeIns(ins []txIn) []byte {
	items := make([][]byte, len(ins))
	for i, in := range ins {
		items[i] = in.encode()
	}
	return cSet(items...)
}

func (t txSpec) fields() map[uint64][]byte {
	outs := make([][]byte, len(t.Outputs))
	for i, o := range t.Outputs {
		outs[i] = o.encode()
	}
	f := map[uint64][]byte{
		bodyInputs:  encodeIns(t.Inputs),
		bodyOutputs: cArray(outs...),
		bodyFee:     cUint(t.Fee),
	}
	t.optional(f)
	for k, v := range t.Raw {
		f[k] = v
	}
	return f
}

func (t txSpec) optional(f map[uint64][]byte) {
	if t.TTL != nil {
		f[bodyTTL] = cUint(*t.TTL)
	}
	if t.Start != nil {
		f[bodyStart] = cUint(*t.Start)
	}
	if len(t.Certs) > 0 {
		f[bodyCerts] = cArray(t.Certs...)
	}
	if t.Wdrl != nil {
		f[bodyWdrl] = cMap(cBytes(t.Wdrl), cUint(t.WdrlAmount))
	}
	if t.Mint != nil {
		f[bodyMint] = cMap(cBytes(t.Mint.Policy[:]), cMap(cBytes(t.Mint.Name), cInt(t.Mint.Qty)))
	}
	if t.DataHash != nil {
		f[bodyDataHash] = cBytes(t.DataHash)
	}
	if len(t.Collateral) > 0 {
		f[bodyCollateral] = encodeIns(t.Collateral)
	}
	if len(t.ReqSigners) > 0 {
		hs := make([][]byte, len(t.ReqSigners))
		for i, h := range t.ReqSigners {
			hs[i] = cBytes(h)
		}
		f[bodySigners] = cSet(hs...)
	}
	if t.Return != nil {
		f[bodyCollReturn] = t.Return.encode()
	}
	if t.TotalColl != nil {
		f[bodyTotalColl] = cUint(*t.TotalColl)
	}
	if len(t.Refs) > 0 {
		f[bodyRefInputs] = encodeIns(t.Refs)
	}
}

// body encodes the body map with its keys ascending.
func (t txSpec) body() []byte {
	f := t.fields()
	keys := make([]uint64, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	kv := make([][]byte, 0, 2*len(keys))
	for _, k := range keys {
		kv = append(kv, cUint(k), f[k])
	}
	return cMap(kv...)
}

func (t txSpec) witnesses(body []byte) []byte {
	var kv [][]byte
	var vkeys [][]byte
	for _, s := range t.Signers {
		w := s.SignTxBody(body)
		vkeys = append(vkeys, cArray(cBytes(w.VKey), cBytes(w.Signature)))
	}
	for _, s := range t.BadSigners {
		w := s.SignTxBody(body)
		w.Signature[0] ^= 0xff
		vkeys = append(vkeys, cArray(cBytes(w.VKey), cBytes(w.Signature)))
	}
	if len(vkeys) > 0 {
		list := cArray(vkeys...)
		if t.WitWrap != nil {
			list = t.WitWrap(list)
		}
		kv = append(kv, cUint(witVKeys), list)
	}
	if len(t.Natives) > 0 {
		kv = append(kv, cUint(witNative), cArray(t.Natives...))
	}
	if t.Redeemers != nil {
		kv = append(kv, cUint(witRedeemers), t.Redeemers)
	}
	if t.PlutusV3 != nil {
		kv = append(kv, cUint(witPlutusV3), cArray(t.PlutusV3))
	}
	return cMap(kv...)
}

// encode returns the signed transaction and its id, the hash of the body.
func (t txSpec) encode() ([]byte, common.Blake2b256) {
	body := t.body()
	parts := [][]byte{body, t.witnesses(body), cBool(!t.Invalid), cNull}
	var tx []byte
	switch t.Frame {
	case frameWide:
		tx = wideHead(majArray, uint8(len(parts)))
		for _, p := range parts {
			tx = append(tx, p...)
		}
	case frameIndefinite:
		tx = cIndefArray(parts...)
	default:
		tx = cArray(parts...)
	}
	return tx, common.Blake2b256Hash(body)
}

// nativeHash is a native script's hash: Blake2b-224 over the language tag 0
// and the script (cardano-ledger hashScript).
func nativeHash(script []byte) common.Blake2b224 {
	return common.Blake2b224Hash(append([]byte{0}, script...))
}

// anyoneScript is the native script "all of nothing": it holds for every
// transaction, so it locks an output anyone can spend and names a policy
// anyone can mint under, with no signature and no redeemer.
var anyoneScript = cArray(cUint(1), cArray())
