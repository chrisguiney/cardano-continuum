// Package fakechain is an in-memory Conway ledger and block producer on a
// virtual clock, for unit tests and `continuum run --fake`.
//
// Transactions arrive as CBOR and go through the same gouroboros decoder a
// real node's blocks take. They are checked by gouroboros' authoritative
// Conway UTXO/UTXOW rule list (conway.UtxoValidationRuleDescriptors), against
// a common.LedgerState backed by the fake's UTxO set, and a rejection names
// each failed predicate with its cardano-ledger class (BadInputsUTxO,
// FeeTooSmallUTxO, ...). Blocks are real Conway block CBOR built with
// ouroboros-mock's fixtures.BlockBuilder; their headers carry zero VRF and KES
// material, so they decode but would not pass header validation.
//
// The chain is deterministic: it reads no wall clock, starts no goroutine and
// never iterates a map where order is observable, so the same configuration
// and calls produce the same blocks and hashes. Faults (drops, rollbacks,
// delays, forced block contents) happen only when scheduled in
// Config.Faults or injected with Chain.Inject.
//
// Mempool behaviour follows the node's: a submission is validated against
// the ledger plus every earlier mempool transaction at the slot after the
// tip; after each block and rollback the mempool is revalidated in order and
// transactions that no longer apply are evicted. Transactions in rolled-back
// blocks vanish unless the rollback asks for them to be re-added.
//
// Certificates, withdrawals, minting, governance, collateral and Plutus are
// not modelled and are refused with ErrUnsupported, so the supply invariant
// is exactly: unspent lovelace + collected fees = genesis lovelace.
package fakechain

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"

	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// EraConway is the hard-fork-combinator era index a local-tx-submission
// client sends with a Conway transaction.
const EraConway uint = conway.EraIdConway

// GenesisOutput is one initial fund. As in Shelley genesis, its output is
// identified as (Blake2b-256 of the address bytes, 0), so each address may
// appear once.
type GenesisOutput struct {
	Address  common.Address
	Lovelace uint64
}

// Config describes a fake chain.
type Config struct {
	Slots     sched.Slots
	NetworkID uint8
	// Params defaults to DefaultParams.
	Params *conway.ConwayProtocolParameters
	// K is the security parameter: the deepest rollback allowed.
	K uint64
	// BlockEvery is the number of slots between blocks.
	BlockEvery uint64
	// MempoolBytes is the mempool capacity in transaction bytes; zero means
	// twice the maximum block body size, the node's default.
	MempoolBytes uint64
	Genesis      []GenesisOutput
	// Faults fire when the clock reaches their slot, before that slot's
	// block. Slots must be at least 1.
	Faults []Scheduled
	// Issuer is the block issuer key written into every header.
	Issuer common.IssuerVkey
}

// DefaultParams returns ouroboros-mock's mainnet-like Conway parameters at
// protocol version 10 (past the Conway bootstrap phase).
func DefaultParams() conway.ConwayProtocolParameters {
	pp := mockledger.NewMockConwayProtocolParams()
	pp.ProtocolVersion.Major, pp.ProtocolVersion.Minor = 10, 0
	return pp
}

// Block is one block on the fake chain.
type Block struct {
	Number   uint64
	Slot     uint64
	Hash     common.Blake2b256
	PrevHash common.Blake2b256
	// Cbor is the Conway block as a node would serve it.
	Cbor []byte
	Txs  []TxID
}

// Point returns the chain-sync point of b.
func (b Block) Point() ocommon.Point { return ocommon.NewPoint(b.Slot, b.Hash[:]) }

type record struct {
	Block
	txs  []*conway.ConwayTransaction
	undo undo
}

// Reason says why a transaction left the mempool without being included.
type Reason int

// Eviction reasons.
const (
	// ReasonDropped: a Drop fault removed it.
	ReasonDropped Reason = iota + 1
	// ReasonInvalid: revalidation failed (expired, inputs spent or rolled
	// back).
	ReasonInvalid
)

// Eviction records a transaction leaving the mempool unincluded.
type Eviction struct {
	Tx       TxID
	Slot     uint64
	Reason   Reason
	Failures []Failure
}

// Chain is the fake ledger. It is safe for concurrent use; calls are
// serialised, so determinism needs only a deterministic caller.
type Chain struct {
	cfg      Config
	pp       *conway.ConwayProtocolParameters
	ls       *mockledger.MockLedgerState
	capBytes uint64

	mu sync.Mutex
	// protects everything below
	slot      uint64
	nextBlock uint64
	faults    []Scheduled
	utxo      utxoSet
	fees      uint64
	supply    uint64
	blocks    []record
	pool      mempool
	include   []TxID
	evicted   []Eviction
	view      func(Ref) (common.Utxo, bool)
}

// New returns a chain at slot 0 holding the genesis outputs and no blocks.
func New(cfg Config) (*Chain, error) {
	if cfg.BlockEvery == 0 || cfg.K == 0 {
		return nil, fmt.Errorf("fakechain: BlockEvery and K must be positive")
	}
	if cfg.Params == nil {
		pp := DefaultParams()
		cfg.Params = &pp
	}
	c := &Chain{
		cfg:       cfg,
		pp:        cfg.Params,
		capBytes:  cfg.MempoolBytes,
		nextBlock: cfg.BlockEvery,
		utxo:      utxoSet{},
	}
	if c.capBytes == 0 {
		c.capBytes = 2 * uint64(cfg.Params.MaxBlockBodySize)
	}
	if err := c.loadGenesis(cfg.Genesis); err != nil {
		return nil, err
	}
	c.pool = newMempool(c.utxo)
	faults, err := sortFaults(cfg.Faults)
	if err != nil {
		return nil, err
	}
	c.faults = faults
	c.ls = c.ledgerState()
	return c, nil
}

func (c *Chain) loadGenesis(gs []GenesisOutput) error {
	for _, g := range gs {
		if g.Address.NetworkId() != uint(c.cfg.NetworkID) {
			return fmt.Errorf("fakechain: genesis address %s is not on network %d", g.Address, c.cfg.NetworkID)
		}
		u, err := genesisUtxo(g)
		if err != nil {
			return err
		}
		r := refOf(u.Id)
		if _, dup := c.utxo[r]; dup {
			return fmt.Errorf("fakechain: genesis address %s appears twice", g.Address)
		}
		c.utxo[r] = u
		c.supply += g.Lovelace
	}
	return nil
}

// ledgerState adapts the fake to the common.LedgerState the gouroboros rules
// read. Only UTxO lookup, network id and slot arithmetic carry state;
// stake, pool and governance state are empty, which is exact for the
// transactions the fake accepts.
func (c *Chain) ledgerState() *mockledger.MockLedgerState {
	g := c.cfg.Slots
	start := time.Unix(0, 0).UTC()
	return mockledger.NewLedgerStateBuilder().
		WithNetworkId(uint(c.cfg.NetworkID)).
		WithUtxoById(func(in common.TransactionInput) (common.Utxo, error) {
			if u, ok := c.view(refOf(in)); ok {
				return u, nil
			}
			return common.Utxo{}, errNoUtxo
		}).
		WithEpochForSlot(func(slot uint64) (uint64, error) { return g.EpochOfSlot(slot), nil }).
		WithSlotToTime(func(slot uint64) (time.Time, error) {
			return start.Add(time.Duration(g.SlotStart(slot)) * time.Millisecond), nil
		}).
		WithTimeToSlot(func(t time.Time) (uint64, error) {
			return g.SlotAt(sched.Time(t.Sub(start).Milliseconds())), nil
		}).
		Build()
}

// check runs the rule list against view at slot. The caller holds c.mu.
func (c *Chain) check(tx *conway.ConwayTransaction, slot uint64, view func(Ref) (common.Utxo, bool)) []Failure {
	c.view = view
	defer func() { c.view = nil }()
	return validate(tx, slot, c.ls, c.pp)
}

// Name implements the submitter's target interface.
func (c *Chain) Name() string { return "fakechain" }

// Submit validates tx against the ledger and mempool and adds it to the
// mempool. A ledger rejection is a *RejectError.
func (c *Chain) Submit(ctx context.Context, era uint, txCbor []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if era != EraConway {
		return fmt.Errorf("%w: era %d", ErrUnsupported, era)
	}
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDecode, err)
	}
	if f := unsupported(tx); f != "" {
		return fmt.Errorf("%w: %s", ErrUnsupported, f)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	size := uint64(len(tx.Cbor()))
	if c.pool.bytes+size > c.capBytes {
		return fmt.Errorf("%w: %d + %d bytes over %d", ErrMempoolFull, c.pool.bytes, size, c.capBytes)
	}
	if fs := c.check(tx, c.mempoolSlot(), c.pool.view.get); len(fs) > 0 {
		return &RejectError{Tx: tx.Hash(), Failures: fs}
	}
	c.pool.push(tx)
	return nil
}

// mempoolSlot is the slot a mempool validates at: the one after the tip, as
// the node's mempool ticks its ledger state.
func (c *Chain) mempoolSlot() uint64 {
	if len(c.blocks) == 0 {
		return 0
	}
	return c.blocks[len(c.blocks)-1].Slot + 1
}

// AdvanceTo moves the clock to slot, firing scheduled faults and producing
// every block due on the way. A failing fault stops the clock at its slot.
func (c *Chain) AdvanceTo(slot uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot < c.slot {
		return fmt.Errorf("fakechain: clock at slot %d cannot move back to %d", c.slot, slot)
	}
	for {
		next := c.nextBlock
		if len(c.faults) > 0 && c.faults[0].Slot <= next {
			next = c.faults[0].Slot
		}
		if next > slot {
			c.slot = slot
			return nil
		}
		c.slot = next
		if err := c.step(); err != nil {
			return err
		}
	}
}

// AdvanceToTime moves the clock to the slot containing t.
func (c *Chain) AdvanceToTime(t sched.Time) error { return c.AdvanceTo(c.cfg.Slots.SlotAt(t)) }

// step fires the faults due at c.slot, then the block if one is due.
func (c *Chain) step() error {
	for len(c.faults) > 0 && c.faults[0].Slot == c.slot {
		f := c.faults[0]
		c.faults = c.faults[1:]
		if err := f.Fault.apply(c); err != nil {
			return fmt.Errorf("fakechain: fault %T at slot %d: %w", f.Fault, f.Slot, err)
		}
	}
	if c.nextBlock != c.slot {
		return nil
	}
	c.nextBlock += c.cfg.BlockEvery
	return c.produce()
}

// produce assembles, applies and records the block at c.slot.
func (c *Chain) produce() error {
	txs := c.assemble()
	var u undo
	for _, tx := range txs {
		c.utxo.applyTx(tx, &u)
	}
	c.fees += u.fees
	b, err := c.encode(txs)
	if err != nil {
		return err
	}
	c.blocks = append(c.blocks, record{Block: b, txs: txs, undo: u})
	c.trimUndo()
	c.revalidate()
	check.PostFn(c.conserved, "fakechain: supply conserved")
	return nil
}

// assemble chooses the block's transactions: the Include list if one is
// pending, otherwise the mempool in order. Each is revalidated at the block
// slot against the block so far; failures are evicted. Assembly stops at the
// first transaction that does not fit, as the node takes a mempool prefix.
func (c *Chain) assemble() []*conway.ConwayTransaction {
	cands := c.pool.txs
	if c.include != nil {
		cands = c.pool.pick(c.include)
		c.include = nil
	}
	view := newOverlay(c.utxo)
	var chosen []*conway.ConwayTransaction
	var used uint64
	for _, tx := range cands {
		if fs := c.check(tx, c.slot, view.get); len(fs) > 0 {
			c.evict(tx, ReasonInvalid, fs)
			continue
		}
		size := uint64(len(tx.Cbor()))
		if used+size > uint64(c.pp.MaxBlockBodySize) {
			break
		}
		used += size
		view.apply(tx)
		chosen = append(chosen, tx)
	}
	c.pool.remove(chosen)
	return chosen
}

func (c *Chain) evict(tx *conway.ConwayTransaction, why Reason, fs []Failure) {
	c.pool.remove([]*conway.ConwayTransaction{tx})
	c.evicted = append(c.evicted, Eviction{Tx: tx.Hash(), Slot: c.slot, Reason: why, Failures: fs})
}

// revalidate rebuilds the mempool against the current tip, in order,
// evicting what no longer applies.
func (c *Chain) revalidate() {
	old := c.pool.txs
	c.pool = newMempool(c.utxo)
	slot := c.mempoolSlot()
	for _, tx := range old {
		if fs := c.check(tx, slot, c.pool.view.get); len(fs) > 0 {
			c.evicted = append(c.evicted, Eviction{Tx: tx.Hash(), Slot: c.slot, Reason: ReasonInvalid, Failures: fs})
			continue
		}
		c.pool.push(tx)
	}
}

// trimUndo forgets the undo records of blocks deeper than k: they are
// immutable.
func (c *Chain) trimUndo() {
	if n := uint64(len(c.blocks)); n > c.cfg.K {
		c.blocks[n-c.cfg.K-1].undo = undo{}
		c.blocks[n-c.cfg.K-1].txs = nil
	}
}

func (c *Chain) conserved() bool {
	var sum uint64
	for _, u := range c.utxo {
		sum += u.Output.Amount().Uint64()
	}
	return sum+c.fees == c.supply
}

// rollback removes the last depth blocks.
func (c *Chain) rollback(depth uint64, readd bool) error {
	if depth == 0 || depth > c.cfg.K || depth > uint64(len(c.blocks)) {
		return fmt.Errorf("%w: depth %d, k %d, height %d", ErrTooDeep, depth, c.cfg.K, len(c.blocks))
	}
	cut := len(c.blocks) - int(depth)
	var back []*conway.ConwayTransaction
	for i := len(c.blocks) - 1; i >= cut; i-- {
		c.utxo.revert(c.blocks[i].undo)
		c.fees -= c.blocks[i].undo.fees
	}
	for _, r := range c.blocks[cut:] {
		back = append(back, r.txs...)
	}
	c.blocks = slices.Clip(c.blocks[:cut])
	if readd {
		c.pool.txs = append(back, c.pool.txs...)
	}
	c.revalidate()
	check.PostFn(c.conserved, "fakechain: supply conserved")
	return nil
}
