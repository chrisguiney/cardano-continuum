package fakechain

import (
	"bytes"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// Slot returns the clock's current slot.
func (c *Chain) Slot() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slot
}

// Epoch returns the epoch of the current slot.
func (c *Chain) Epoch() uint64 { return c.cfg.Slots.EpochOfSlot(c.Slot()) }

// Tip returns the point of the last block, or the origin.
func (c *Chain) Tip() ocommon.Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tip(len(c.blocks))
}

// tip returns the point of the block at height n (origin for 0).
func (c *Chain) tip(n int) ocommon.Point {
	if n == 0 {
		return ocommon.NewPointOrigin()
	}
	return c.blocks[n-1].Point()
}

// Blocks returns the current chain, oldest first.
func (c *Chain) Blocks() []Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Block, len(c.blocks))
	for i, r := range c.blocks {
		out[i] = r.Block
	}
	return out
}

// Utxo returns the confirmed output at r.
func (c *Chain) Utxo(r Ref) (common.Utxo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.utxo[r]
	return u, ok
}

// AllUtxos returns the confirmed UTxO set sorted by reference.
func (c *Chain) AllUtxos() []common.Utxo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.utxo.sorted()
}

// UtxosAt returns the confirmed outputs paying addr, sorted by reference.
func (c *Chain) UtxosAt(addr common.Address) []common.Utxo {
	want, err := addr.Bytes()
	if err != nil {
		return nil
	}
	var out []common.Utxo
	for _, u := range c.AllUtxos() {
		got, err := u.Output.Address().Bytes()
		if err == nil && bytes.Equal(got, want) {
			out = append(out, u)
		}
	}
	return out
}

// UnspentLovelace returns the lovelace held in confirmed outputs.
func (c *Chain) UnspentLovelace() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sum uint64
	for _, u := range c.utxo {
		sum += u.Output.Amount().Uint64()
	}
	return sum
}

// Fees returns the fees collected by the blocks on the current chain.
func (c *Chain) Fees() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fees
}

// HasTx reports whether the mempool holds id, as local-tx-monitor's HasTx.
func (c *Chain) HasTx(id TxID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pool.find(id) != nil
}

// Mempool returns the mempool's transactions in order.
func (c *Chain) Mempool() []TxID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]TxID, len(c.pool.txs))
	for i, tx := range c.pool.txs {
		out[i] = tx.Hash()
	}
	return out
}

// Evictions returns every transaction that left the mempool unincluded, in
// order. A node does not report these; tests use them as ground truth.
func (c *Chain) Evictions() []Eviction {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Eviction(nil), c.evicted...)
}

// Deposits returns the lovelace held in confirmed stake, pool, DRep and
// proposal deposits.
func (c *Chain) Deposits() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.certs.deposits()
}

// StakeRegistered reports whether cred is a confirmed registered stake
// credential.
func (c *Chain) StakeRegistered(cred common.Credential) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.certs.accounts[keyOf(cred)]
	return ok
}

// Evaluate runs txCbor's Plutus scripts against the mempool's view of the
// ledger, each against the per-transaction budget rather than its declared
// units, and returns the units each redeemer used. It is the measurement
// the PlutusScripts rule compares declared units with.
func (c *Chain) Evaluate(txCbor []byte) (map[common.RedeemerKey]common.ExUnits, error) {
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view, c.ls.certs = c.pool.view.get, c.pool.view.certs
	defer func() { c.view, c.ls.certs = nil, nil }()
	budget := c.pp.MaxTxExUnits
	return conway.EvaluatePlutusScripts(tx, c.ls, c.pp, &budget)
}
