package fakechain

import (
	"fmt"
	"slices"
)

// Fault is an environment event the fake applies only when told to: through
// Config.Faults at a slot, or Chain.Inject at once. The set is closed.
type Fault interface {
	apply(c *Chain) error
}

// Scheduled is a fault due at a slot.
type Scheduled struct {
	Slot  uint64
	Fault Fault
}

// Drop removes Tx from the mempool, as an eviction the submitter is not told
// about. Transactions that spend its outputs are evicted with it on the
// revalidation that follows.
type Drop struct{ Tx TxID }

// Rollback removes the last Depth blocks, never reaching the immutable point
// (see Chain.Immutable). With Readd their
// transactions go back to the front of the mempool and are revalidated;
// without it they vanish, which is what a node does.
type Rollback struct {
	Depth uint64
	Readd bool
}

// Delay postpones the next block, and so the whole schedule, by Slots.
type Delay struct{ Slots uint64 }

// Include makes the next block contain exactly Txs, in that order (each still
// subject to validation). Every one must be in the mempool now. Use it to
// decide which of two conflicting transactions wins. Overfull forges them
// past the block's execution budget, as a faulty forger would: no honest
// node adopts such a block (cardano-ledger Alonzo BBODY, TooManyExUnits).
type Include struct {
	Txs      []TxID
	Overfull bool
}

// Inject applies f now, at the current slot.
func (c *Chain) Inject(f Fault) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return f.apply(c)
}

func (d Drop) apply(c *Chain) error {
	tx := c.pool.find(d.Tx)
	if tx == nil {
		return fmt.Errorf("%w: drop %s", ErrNotInMempool, d.Tx)
	}
	c.evict(tx, ReasonDropped, nil)
	c.revalidate()
	return nil
}

func (r Rollback) apply(c *Chain) error { return c.rollback(r.Depth, r.Readd) }

func (d Delay) apply(c *Chain) error {
	c.nextBlock += d.Slots
	return nil
}

func (in Include) apply(c *Chain) error {
	for _, id := range in.Txs {
		if c.pool.find(id) == nil {
			return fmt.Errorf("%w: include %s", ErrNotInMempool, id)
		}
	}
	c.include = append([]TxID{}, in.Txs...)
	c.overfull = in.Overfull
	return nil
}

// sortFaults orders faults by slot, keeping the configured order within a
// slot.
func sortFaults(fs []Scheduled) ([]Scheduled, error) {
	out := slices.Clone(fs)
	for _, f := range out {
		if f.Slot == 0 || f.Fault == nil {
			return nil, fmt.Errorf("fakechain: scheduled fault %T at slot %d: slot must be positive and fault set", f.Fault, f.Slot)
		}
	}
	slices.SortStableFunc(out, func(a, b Scheduled) int {
		switch {
		case a.Slot < b.Slot:
			return -1
		case a.Slot > b.Slot:
			return 1
		}
		return 0
	})
	return out, nil
}
