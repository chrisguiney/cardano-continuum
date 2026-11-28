package chain

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// TxID is a transaction hash.
type TxID = common.Blake2b256

// Point is a slot and block hash. The zero Point is the origin.
type Point struct {
	Slot uint64
	Hash common.Blake2b256
}

// IsOrigin reports whether p is the origin.
func (p Point) IsOrigin() bool { return p == Point{} }

// Wire returns p as a chain-sync point.
func (p Point) Wire() ocommon.Point {
	if p.IsOrigin() {
		return ocommon.NewPointOrigin()
	}
	return ocommon.NewPoint(p.Slot, bytes.Clone(p.Hash[:]))
}

// Log returns p as an event-log point; the origin has no hash.
func (p Point) Log() eventlog.ChainPoint {
	if p.IsOrigin() {
		return eventlog.ChainPoint{}
	}
	return eventlog.ChainPoint{Slot: p.Slot, Hash: bytes.Clone(p.Hash[:])}
}

// PointFromWire converts a chain-sync point. A hash that is not 32 bytes
// gives the origin only when it is empty; otherwise ok is false.
func PointFromWire(w ocommon.Point) (p Point, ok bool) {
	return pointFrom(w.Slot, w.Hash)
}

// PointFromLog converts an event-log point.
func PointFromLog(c eventlog.ChainPoint) (p Point, ok bool) {
	return pointFrom(c.Slot, c.Hash)
}

func pointFrom(slot uint64, hash []byte) (Point, bool) {
	if len(hash) == 0 {
		return Point{}, slot == 0
	}
	if len(hash) != len(common.Blake2b256{}) {
		return Point{}, false
	}
	return Point{Slot: slot, Hash: common.Blake2b256(hash)}, true
}

// Ref names a transaction output.
type Ref struct {
	Tx    TxID
	Index uint32
}

// RefOf converts a ledger input.
func RefOf(in common.TransactionInput) Ref {
	return Ref{Tx: in.Id(), Index: in.Index()}
}

// Compare orders references by transaction hash, then output index.
func (r Ref) Compare(o Ref) int {
	if c := bytes.Compare(r.Tx[:], o.Tx[:]); c != 0 {
		return c
	}
	return cmp.Compare(r.Index, o.Index)
}

// Block is what the view needs of a block: its place in the chain and the
// ledger effect of each transaction.
type Block struct {
	Point  Point
	Prev   common.Blake2b256
	Number uint64
	Txs    []Tx
	// Raw is the block's CBOR as the node sent it; empty for a block built
	// in a test.
	Raw []byte
}

// Tx is the ledger effect of one transaction. A transaction that failed
// phase-2 validation (isValid = false) spends its collateral and creates
// only its collateral return, and its certificates take no effect, so
// Spends, Creates and Certs already reflect the flag.
type Tx struct {
	Hash    TxID
	Spends  []Ref
	Creates []common.Utxo
	Certs   []common.Certificate
	// Proposals are the governance actions the transaction proposes, and
	// Votes its voting procedures. Like Certs, both are empty for a
	// transaction that failed phase-2 validation.
	Proposals []Proposal
	Votes     []Vote
	// Withdrawals are the key-hash reward withdrawals; empty for a
	// transaction that failed phase-2 validation.
	Withdrawals []Withdrawal
	// Fee is the lovelace the transaction moves to the fee pot: the body
	// fee, or for an invalid transaction its forfeited total collateral.
	Fee uint64
	// Units are the execution units its redeemers declare, which count
	// against the block's budget whether or not its scripts passed.
	Units common.ExUnits
}

// TxFrom converts a ledger transaction through gouroboros' Consumed and
// Produced, which apply the isValid rule (ledger/conway/conway.go).
func TxFrom(tx common.Transaction) Tx {
	out := Tx{Hash: tx.Hash(), Creates: tx.Produced()}
	for _, in := range tx.Consumed() {
		out.Spends = append(out.Spends, RefOf(in))
	}
	if tx.IsValid() {
		out.Certs = tx.Certificates()
		for i, p := range tx.ProposalProcedures() {
			out.Proposals = append(out.Proposals, proposalOf(GovID{Tx: out.Hash, Index: uint32(i)}, p))
		}
		out.Votes = votesOf(tx.VotingProcedures())
		out.Withdrawals = txWithdrawals(tx)
		if f := tx.Fee(); f != nil {
			out.Fee = f.Uint64()
		}
	} else if c := tx.TotalCollateral(); c != nil {
		out.Fee = c.Uint64()
	}
	if w := tx.Witnesses(); w != nil && w.Redeemers() != nil {
		for _, r := range w.Redeemers().Iter() {
			out.Units.Memory += r.ExUnits.Memory
			out.Units.Steps += r.ExUnits.Steps
		}
	}
	return out
}

// votesOf flattens a transaction's voting procedures into a fixed order.
func votesOf(vps common.VotingProcedures) []Vote {
	var out []Vote
	for voter, acts := range vps {
		for id, vp := range acts {
			out = append(out, Vote{
				Voter:  Voter{Kind: voter.Type, Hash: voter.Hash},
				Action: GovID{Tx: id.TransactionId, Index: id.GovActionIdx},
				Choice: vp.Vote,
			})
		}
	}
	slices.SortFunc(out, func(a, b Vote) int {
		if c := a.Action.Compare(b.Action); c != 0 {
			return c
		}
		return a.Voter.compare(b.Voter)
	})
	return out
}

// BlockFrom converts a decoded ledger block.
func BlockFrom(b ledger.Block) Block {
	out := Block{
		Point:  Point{Slot: b.SlotNumber(), Hash: b.Hash()},
		Prev:   b.PrevHash(),
		Number: b.BlockNumber(),
		Raw:    b.Cbor(),
	}
	for _, tx := range b.Transactions() {
		out.Txs = append(out.Txs, TxFrom(tx))
	}
	return out
}
