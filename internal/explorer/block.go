package explorer

import (
	"encoding/hex"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// ref names an output by its transaction and index.
type ref struct {
	tx  [32]byte
	idx uint32
}

func refOf(in common.TransactionInput) ref { return ref{tx: in.Id(), idx: in.Index()} }

type txMeta struct {
	id  [32]byte
	fee uint64
}

// addrTx records that a transaction touched an address, so a rollback can
// take it out of the address's history.
type addrTx struct {
	addr string
	tx   [32]byte
}

// meta is the compact entry kept for every block.
type meta struct {
	hash, prev [32]byte
	height     uint64
	slot       uint64
	issuer     [28]byte
	size       uint64
	fees       uint64
	exMem      uint64
	exSteps    uint64
	txs        []txMeta
	// full is nil once the block is evicted.
	full *fullBlock
}

// fullBlock is what only the newest blocks keep.
type fullBlock struct {
	raw []byte
	// inputs are the outputs the block's transactions consumed, by ref, as
	// output CBOR: they resolve each input and, for those that predate the
	// block, are what a rollback restores.
	inputs map[ref][]byte
	// born are the refs the block's own transactions created.
	born    map[ref]struct{}
	touched []addrTx
}

func (f *fullBlock) resolve(in common.TransactionInput) (common.TransactionOutput, bool) {
	raw, ok := f.inputs[refOf(in)]
	if !ok {
		return nil, false
	}
	o, err := decodeOut(raw)
	return o, err == nil
}

func (m *meta) hashHex() string { return hex.EncodeToString(m.hash[:]) }

// decodeBlock decodes a Conway block, the era the engine follows
// (engine.Digest).
func decodeBlock(raw []byte) (*conway.ConwayBlock, error) {
	b, err := conway.NewConwayBlockFromCbor(raw)
	if err != nil {
		return nil, fmt.Errorf("explorer: decode block: %w", err)
	}
	return b, nil
}

// headerOf reads the header fields the ledger interface does not expose.
func headerOf(b *conway.ConwayBlock) Header {
	h := b.BlockHeader
	body := h.Body
	return Header{
		ProtoMajor: body.ProtoVersion.Major, ProtoMinor: body.ProtoVersion.Minor,
		BodyHash: b.BlockBodyHash().String(), BodySize: b.BlockBodySize(),
		IssuerVkey: hex.EncodeToString(body.IssuerVkey[:]), VRFKey: hex.EncodeToString(body.VrfKey),
		OpCertHotVkey: hex.EncodeToString(body.OpCert.HotVkey), OpCertSeq: body.OpCert.SequenceNumber, KESPeriod: body.OpCert.KesPeriod,
	}
}
