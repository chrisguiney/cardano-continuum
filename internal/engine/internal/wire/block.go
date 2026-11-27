package wire

import (
	"errors"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// VoteDigest is one voting procedure: the voter (common.VoterType* and
// its key or script hash), the governance action and the choice.
type VoteDigest struct {
	Kind   uint8             `json:"kind"`
	Hash   common.Blake2b224 `json:"hash"`
	Tx     TxID              `json:"tx"`
	Index  uint32            `json:"index"`
	Choice uint8             `json:"choice"`
}

// TxDigest is one transaction as the engine reads it from a block: the
// outputs it consumed and produced (collateral ones for an invalid
// transaction), its fee, and its certificates (each its CBOR), the number
// of governance actions it proposes and its votes. An invalid
// transaction has no certificates, proposals or votes.
type TxDigest struct {
	ID        TxID         `json:"id"`
	Inputs    []Ref        `json:"inputs"`
	Outputs   []TxOut      `json:"outputs"`
	Fee       uint64       `json:"fee"`
	Certs     [][]byte     `json:"certs,omitempty"`
	Proposals uint32       `json:"proposals,omitzero"`
	Votes     []VoteDigest `json:"votes,omitempty"`
	// Gov describes each of the Proposals in order. A log written before
	// it existed has the count alone; those proposals have no known type.
	Gov []ProposalDigest `json:"gov,omitempty"`
	// Units are the execution units the redeemers declare, nil without
	// any.
	Units *Units `json:"units,omitempty"`
}

// Units are execution units, as common.ExUnits counts them. The gouroboros
// type embeds cbor.StructAsArray, which JSON cannot encode.
type Units struct {
	// Memory is the memory units.
	Memory int64 `json:"memory"`
	// Steps is the CPU step units.
	Steps int64 `json:"steps"`
}

// ProposalDigest is what a block says of one proposal: its action type as
// profile files name it, whether stake pools may vote on it, and its
// deposit.
type ProposalDigest struct {
	Type    string `json:"type,omitempty"`
	Pools   bool   `json:"pools,omitzero"`
	Deposit uint64 `json:"deposit,omitzero"`
}

// Block is an applied block as the engine keeps it: its point, its
// transactions and its block number.
type Block struct {
	Point  eventlog.ChainPoint `json:"point"`
	Txs    []TxDigest          `json:"txs"`
	Number uint64              `json:"number,omitzero"`
}

// BlockContent is the Content of a BlockApplied input.
type BlockContent struct {
	Txs    []TxDigest `json:"txs"`
	Number uint64     `json:"number,omitzero"`
	// Raw is the block's own CBOR. The engine ignores it; the explorer
	// decodes it from the log.
	Raw []byte `json:"raw,omitempty"`
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
