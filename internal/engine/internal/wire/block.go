package wire

import (
	"errors"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// VoteDigest is one voting procedure: the voter (common.VoterType* and
// its key or script hash), the governance action and the choice.
type VoteDigest struct {
	Kind   uint8    `cbor:"1,keyasint"`
	Hash   [28]byte `cbor:"2,keyasint"`
	Tx     TxID     `cbor:"3,keyasint"`
	Index  uint32   `cbor:"4,keyasint"`
	Choice uint8    `cbor:"5,keyasint"`
}

// TxDigest is one transaction as the engine reads it from a block: the
// outputs it consumed and produced (collateral ones for an invalid
// transaction), its fee, and its certificates (each its CBOR), the number
// of governance actions it proposes and its votes. An invalid
// transaction has no certificates, proposals or votes.
type TxDigest struct {
	ID        TxID         `cbor:"1,keyasint"`
	Inputs    []Ref        `cbor:"2,keyasint"`
	Outputs   []TxOut      `cbor:"3,keyasint"`
	Fee       uint64       `cbor:"4,keyasint"`
	Certs     [][]byte     `cbor:"5,keyasint,omitempty"`
	Proposals uint32       `cbor:"6,keyasint,omitempty"`
	Votes     []VoteDigest `cbor:"7,keyasint,omitempty"`
	// Gov describes each of the Proposals in order. A log written before
	// it existed has the count alone; those proposals have no known type.
	Gov []ProposalDigest `cbor:"8,keyasint,omitempty"`
	// Units are the execution units the redeemers declare, nil without
	// any.
	Units *common.ExUnits `json:"units,omitempty"`
}

// ProposalDigest is what a block says of one proposal: its action type as
// profile files name it, whether stake pools may vote on it, and its
// deposit.
type ProposalDigest struct {
	Type    string `cbor:"1,keyasint,omitempty"`
	Pools   bool   `cbor:"2,keyasint,omitempty"`
	Deposit uint64 `cbor:"3,keyasint,omitempty"`
}

// Block is an applied block as the engine keeps it: its point, its
// transactions and its block number.
type Block struct {
	Point  eventlog.ChainPoint `cbor:"1,keyasint"`
	Txs    []TxDigest          `cbor:"2,keyasint"`
	Number uint64              `cbor:"3,keyasint,omitempty"`
}

// BlockContent is the Content of a BlockApplied input.
type BlockContent struct {
	Txs    []TxDigest `cbor:"1,keyasint"`
	Number uint64     `cbor:"2,keyasint,omitempty"`
	// Raw is the block's own CBOR. The engine ignores it; the explorer
	// decodes it from the log.
	Raw []byte `cbor:"3,keyasint,omitempty"`
}

// ChainEvent is one chain-sync message as the loop reads it.
type ChainEvent struct {
	// Rollback is true for a roll-backward to Point, false for a block at
	// Point whose digest is Content.
	Rollback bool
	Point    eventlog.ChainPoint
	Content  []byte
}

// Follower delivers chain-sync messages. Next returns false when caught up.
type Follower interface {
	Next() (ChainEvent, bool, error)
}

// ErrBadPoint: a block point that is neither the origin nor a 32-byte
// hash, which no node can have produced.
var ErrBadPoint = errors.New("engine: malformed block point")
