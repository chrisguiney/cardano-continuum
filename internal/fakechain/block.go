package fakechain

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/ouroboros-mock/fixtures"
)

// blockUse is what a block's transactions take of its capacity: body bytes
// and the execution units their redeemers declare.
type blockUse struct {
	bytes uint64
	units common.ExUnits
}

// add returns u with tx's size and declared units added.
func (u blockUse) add(tx *conway.ConwayTransaction) blockUse {
	u.bytes += uint64(len(tx.Cbor()))
	if w := tx.Witnesses(); w != nil && w.Redeemers() != nil {
		for _, r := range w.Redeemers().Iter() {
			u.units.Memory += r.ExUnits.Memory
			u.units.Steps += r.ExUnits.Steps
		}
	}
	return u
}

// fits reports whether u is within a block's body size and execution
// budget.
func (u blockUse) fits(pp *conway.ConwayProtocolParameters) bool {
	return u.bytes <= uint64(pp.MaxBlockBodySize) &&
		u.units.Memory <= pp.MaxBlockExUnits.Memory && u.units.Steps <= pp.MaxBlockExUnits.Steps
}

// encode builds the Conway block at c.slot holding txs.
func (c *Chain) encode(txs []*conway.ConwayTransaction) (Block, error) {
	var prev common.Blake2b256
	if n := len(c.blocks); n > 0 {
		prev = c.blocks[n-1].Hash
	}
	number := uint64(len(c.blocks)) + 1
	gtxs := make([]common.Transaction, len(txs))
	ids := make([]TxID, len(txs))
	for i, tx := range txs {
		gtxs[i] = tx
		ids[i] = tx.Hash()
	}
	pv := c.pp.ProtocolVersion
	b, err := fixtures.NewBlockBuilder(conway.EraConway).
		WithBlockNumber(number).
		WithSlot(c.slot).
		WithPreviousHash(prev).
		WithIssuerVkey(c.cfg.Issuer).
		WithProtocolVersion(uint64(pv.Major), uint64(pv.Minor)).
		WithTransactions(gtxs...).
		Build()
	if err != nil {
		return Block{}, fmt.Errorf("fakechain: build block %d at slot %d: %w", number, c.slot, err)
	}
	return Block{
		Number:   number,
		Slot:     c.slot,
		Hash:     b.Hash(),
		PrevHash: prev,
		Cbor:     b.Cbor(),
		Txs:      ids,
	}, nil
}

// genesisUtxo builds the output for g by decoding its CBOR, so the output
// carries the wire bytes min-UTxO is computed from, exactly as an output
// decoded from a transaction does.
func genesisUtxo(g GenesisOutput) (common.Utxo, error) {
	addr, err := g.Address.Bytes()
	if err != nil {
		return common.Utxo{}, fmt.Errorf("fakechain: genesis address: %w", err)
	}
	raw, err := cbor.Encode(map[uint]any{0: addr, 1: g.Lovelace})
	if err != nil {
		return common.Utxo{}, fmt.Errorf("fakechain: encode genesis output: %w", err)
	}
	var out babbage.BabbageTransactionOutput
	if _, err := cbor.Decode(raw, &out); err != nil {
		return common.Utxo{}, fmt.Errorf("fakechain: decode genesis output: %w", err)
	}
	in := shelley.ShelleyTransactionInput{TxId: common.Blake2b256Hash(addr), OutputIndex: 0}
	return common.Utxo{Id: in, Output: &out}, nil
}
