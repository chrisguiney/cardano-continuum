package explorer

import (
	"encoding/hex"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Bounds on what the index keeps beyond the retained blocks: proposals and
// anchored votes are few on any run, but a hostile chain should not grow the
// index without limit.
const (
	maxActions        = 20_000
	maxNotesPerAction = 5_000
)

type govKey struct {
	tx  [32]byte
	idx uint32
}

// VoteAnchor is the anchor a vote carries. The vote feed of the governance
// view names votes but not their anchors; the index reads them off the
// transaction.
type VoteAnchor struct {
	Tx        string `json:"tx"`
	VoterHash string `json:"voter_hash"`
	Anchor    Anchor `json:"anchor"`
}

type blockGov struct {
	acts  []govKey
	notes []govKey
}

// govBook keeps each proposal decoded, and the anchors of the votes cast on
// it, for as long as the block that carried them is on the chain: unlike a
// block's full contents, they survive eviction. The index lock covers it.
type govBook struct {
	acts    map[govKey]GovAction
	notes   map[govKey][]VoteAnchor
	byBlock map[[32]byte]blockGov
}

func newGovBook() *govBook {
	return &govBook{acts: map[govKey]GovAction{}, notes: map[govKey][]VoteAnchor{}, byBlock: map[[32]byte]blockGov{}}
}

// add records the proposals and anchored votes of a block's transactions.
func (b *govBook) add(block [32]byte, slot uint64, txs []common.Transaction) {
	var bg blockGov
	for _, tx := range txs {
		b.addActions(&bg, tx, slot)
		b.addNotes(&bg, tx)
	}
	if len(bg.acts) > 0 || len(bg.notes) > 0 {
		b.byBlock[block] = bg
	}
}

func (b *govBook) addActions(bg *blockGov, tx common.Transaction, slot uint64) {
	id := [32]byte(tx.Hash())
	for i, p := range tx.ProposalProcedures() {
		k := govKey{id, uint32(i)}
		if _, dup := b.acts[k]; dup || len(b.acts) >= maxActions {
			continue
		}
		g := govDecode(p)
		g.Tx, g.Index, g.Slot = hex.EncodeToString(id[:]), uint32(i), slot
		b.acts[k] = g
		bg.acts = append(bg.acts, k)
	}
}

func (b *govBook) addNotes(bg *blockGov, tx common.Transaction) {
	id := hex.EncodeToString(tx.Hash().Bytes())
	for voter, acts := range tx.VotingProcedures() {
		for aid, vp := range acts {
			k := govKey{aid.TransactionId, aid.GovActionIdx}
			if vp.Anchor == nil || len(b.notes[k]) >= maxNotesPerAction {
				continue
			}
			b.notes[k] = append(b.notes[k], VoteAnchor{Tx: id, VoterHash: hex.EncodeToString(voter.Hash[:]), Anchor: *anchorOf(*vp.Anchor)})
			bg.notes = append(bg.notes, k)
		}
	}
}

// drop takes a block's proposals and anchored votes off, for a rollback.
// Notes are appended in chain order, so the block's are the last ones of
// their action.
func (b *govBook) drop(block [32]byte) {
	bg, ok := b.byBlock[block]
	if !ok {
		return
	}
	delete(b.byBlock, block)
	for _, k := range bg.acts {
		delete(b.acts, k)
		delete(b.notes, k)
	}
	for i := len(bg.notes) - 1; i >= 0; i-- {
		k := bg.notes[i]
		if n := b.notes[k]; len(n) > 0 {
			b.notes[k] = n[:len(n)-1]
		}
	}
}
