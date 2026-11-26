package forger

import (
	"errors"
	"fmt"
	"math"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/kes"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/vrf"
)

// Tip is a block a new block can extend.
type Tip struct {
	// Origin is true before the first block; the other fields are then zero.
	Origin bool
	Slot   uint64
	Number uint64
	Hash   [32]byte
}

// PeerID identifies a peer by the address it connects from.
type PeerID string

// Expect is what an honest node must do with a block.
type Expect string

// Expectations.
const (
	// Adopt: the block is valid; a node may select it or a competitor.
	Adopt Expect = "adopt"
	// Reject: the block breaks a rule; no honest node may select it.
	Reject Expect = "reject"
)

// Forge is one block to build: where it goes on the chain and every way it
// departs from the block an honest node would forge there. The zero
// departures give an honest block.
type Forge struct {
	Kind   Kind
	Parent Tip
	Slot   uint64
	// VRFSlot is the slot the leader VRF proof is made for; an honest block
	// has VRFSlot == Slot.
	VRFSlot uint64
	// Cert is the operational certificate in the header.
	Cert CertName
	// KESShift is added to the KES evolution the header is signed at, so
	// the signature belongs to another period than the header's slot.
	KESShift uint64
	// BadBodyHash makes the header commit to another body than the one
	// served.
	BadBodyHash bool
	// Pad adds at least this many bytes to the body.
	Pad uint64
	// MinorBump is added to the header's protocol minor version, which no
	// rule constrains: it makes a second, equally valid block for a slot.
	MinorBump uint64
	// SendTo is the peers offered the block; empty means every peer.
	SendTo []PeerID
	Expect Expect
	// Rule is the rule the block breaks, by its name in the Haskell node.
	Rule string
}

// Block is an assembled Conway block.
type Block struct {
	Slot, Number uint64
	Hash, Prev   [32]byte
	// Header is the header alone; CBOR is the whole block.
	Header, CBOR []byte
	BodySize     int
	// VRFOutput is the leader VRF output, which the nonce evolves with.
	VRFOutput []byte
	// Issuer is the cold verification key and Counter the operational
	// certificate counter of the header.
	Issuer  [32]byte
	Counter uint64
}

// Tip is the block as a parent.
func (b Block) Tip() Tip { return Tip{Slot: b.Slot, Number: b.Number, Hash: b.Hash} }

// headerBody is the Babbage and Conway header body
// (cardano-ledger eras/conway/impl/cddl-files/conway.cddl, header_body).
type headerBody struct {
	cbor.StructAsArray
	Number   uint64
	Slot     uint64
	Prev     any
	Issuer   []byte
	VRFVKey  []byte
	VRF      vrfCert
	BodySize uint64
	BodyHash []byte
	OpCert   headerCert
	Version  protoVersion
}

type vrfCert struct {
	cbor.StructAsArray
	Output, Proof []byte
}

type headerCert struct {
	cbor.StructAsArray
	HotVKey []byte
	Counter uint64
	Start   uint64
	Sig     []byte
}

type protoVersion struct {
	cbor.StructAsArray
	Major, Minor uint64
}

// Assemble builds the block f describes and signs it. It is pure: the same
// arguments give the same bytes.
func Assemble(p Params, c *Credentials, epochNonce [32]byte, f Forge) (Block, error) {
	cert, ok := c.Certs[f.Cert]
	if !ok {
		return Block{}, fmt.Errorf("no operational certificate %q in the keys", certFile(f.Cert))
	}
	if f.Slot > math.MaxInt64 || f.VRFSlot > math.MaxInt64 {
		return Block{}, errors.New("slot does not fit a VRF input")
	}
	body, err := makeBody(f.Pad)
	if err != nil {
		return Block{}, err
	}
	bodyHash := body.hash()
	if f.BadBodyHash {
		bodyHash[0] ^= 0xff
	}
	alpha, err := vrf.MkInputVrf(int64(f.VRFSlot), epochNonce[:])
	if err != nil {
		return Block{}, fmt.Errorf("VRF input for slot %d: %w", f.VRFSlot, err)
	}
	proof, output, err := c.vrfProve(alpha)
	if err != nil {
		return Block{}, fmt.Errorf("VRF proof for slot %d: %w", f.VRFSlot, err)
	}
	hb := headerBody{
		Number: f.Parent.Number + 1, Slot: f.Slot, Prev: f.Parent.Hash[:],
		Issuer: c.ColdVKey, VRFVKey: c.VRFVKey, VRF: vrfCert{Output: output, Proof: proof},
		BodySize: uint64(body.size()), BodyHash: bodyHash[:],
		OpCert:  headerCert{HotVKey: cert.KESVKey, Counter: cert.Counter, Start: cert.Start, Sig: cert.Sig},
		Version: protoVersion{Major: p.ProtoMajor, Minor: p.ProtoMinor + f.MinorBump},
	}
	if f.Parent.Origin {
		hb.Number, hb.Prev = 0, nil
	}
	header, err := signHeader(c, hb, kesEvolution(p, cert, f))
	if err != nil {
		return Block{}, err
	}
	b := Block{
		Slot: hb.Slot, Number: hb.Number, Hash: common.Blake2b256Hash(header), Prev: f.Parent.Hash,
		Header: header, BodySize: body.size(), VRFOutput: output, Counter: cert.Counter,
	}
	copy(b.Issuer[:], c.ColdVKey)
	// A block is the header followed by the four body segments.
	b.CBOR = append([]byte{0x85}, header...)
	for _, seg := range body.segments() {
		b.CBOR = append(b.CBOR, seg...)
	}
	return b, nil
}

// kesEvolution is how many times the KES key has evolved at the header's
// slot under cert, plus the forge's shift. Outside the certificate's window
// no evolution is right, and the node rejects on the period before it looks
// at the signature, so any evolution the key has will do.
func kesEvolution(p Params, cert OpCert, f Forge) uint64 {
	last := kes.MaxPeriod(kes.CardanoKesDepth) - 1
	period := f.Slot / p.SlotsPerKESPeriod
	var evo uint64
	if period >= cert.Start && period-cert.Start <= last {
		evo = period - cert.Start
	}
	return (evo + f.KESShift) % (last + 1)
}

func signHeader(c *Credentials, hb headerBody, evolution uint64) ([]byte, error) {
	raw, err := cbor.Encode(hb)
	if err != nil {
		return nil, fmt.Errorf("encode header body: %w", err)
	}
	sig, err := c.kesSign(evolution, raw)
	if err != nil {
		return nil, err
	}
	header, err := cbor.Encode([]any{cbor.RawMessage(raw), sig})
	if err != nil {
		return nil, fmt.Errorf("encode header: %w", err)
	}
	return header, nil
}

// body is a Conway block body as its four CBOR segments: transaction
// bodies, witness sets, auxiliary data by transaction index, and the
// indices of phase-2-invalid transactions.
type body struct {
	bodies, witnesses, aux, invalid []byte
}

func (b body) segments() [4][]byte { return [4][]byte{b.bodies, b.witnesses, b.aux, b.invalid} }

func (b body) size() int {
	n := 0
	for _, s := range b.segments() {
		n += len(s)
	}
	return n
}

// hash is the Alonzo-onward body hash: the hash of the four segment hashes
// (cardano-ledger Alonzo TxSeq, hashAlonzoSegWits).
func (b body) hash() [32]byte {
	var cat []byte
	for _, s := range b.segments() {
		h := common.Blake2b256Hash(s)
		cat = append(cat, h[:]...)
	}
	return common.Blake2b256Hash(cat)
}

// makeBody is an empty body, or with pad > 0 a body of one filler
// transaction whose metadata makes the body at least pad bytes long. The
// filler spends an output that does not exist: nothing can make a body
// over the limit valid, and a node must refuse it on the size its header
// declares, before the transaction is looked at.
func makeBody(pad uint64) (body, error) {
	if pad == 0 {
		return body{bodies: []byte{0x80}, witnesses: []byte{0x80}, aux: []byte{0xa0}, invalid: []byte{0x80}}, nil
	}
	if pad > 16<<20 {
		return body{}, fmt.Errorf("body padding of %d bytes is over 16 MiB", pad)
	}
	// Metadata byte strings are at most 64 bytes each.
	chunk := make([]byte, 64)
	chunks := make([]any, pad/64+1)
	for i := range chunks {
		chunks[i] = chunk
	}
	var b body
	var err error
	txBody := map[uint]any{0: [][]any{{make([]byte, 32), uint(0)}}, 1: []any{}, 2: uint(0)}
	if b.bodies, err = cbor.Encode([]any{txBody}); err != nil {
		return body{}, fmt.Errorf("encode filler transaction: %w", err)
	}
	if b.witnesses, err = cbor.Encode([]any{map[uint]any{}}); err != nil {
		return body{}, fmt.Errorf("encode filler witness set: %w", err)
	}
	if b.aux, err = cbor.Encode(map[uint]any{0: map[uint]any{1: chunks}}); err != nil {
		return body{}, fmt.Errorf("encode filler metadata: %w", err)
	}
	b.invalid = []byte{0x80}
	return b, nil
}

// BlockType is the block type every forged block has on the wire.
const BlockType = ledger.BlockTypeConway
