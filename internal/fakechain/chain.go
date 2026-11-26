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
// blocks vanish unless the rollback asks for them to be re-added. A block
// takes the longest mempool prefix within its body size and execution
// budget, as the node's forging does.
//
// Certificates, withdrawals, minting, votes, proposals, collateral and
// Plutus scripts of every language (with the devnet genesis cost models,
// ParamsFor) are checked by the same gouroboros rules against the
// fake's delegation and governance state (see certState), which accepted
// transactions update. Epoch-boundary effects are not modelled: no rewards,
// pool retirement, ratification or enactment. Treasury donations, the
// current-treasury-value field and phase-2-invalid transactions are refused
// with ErrUnsupported, so the supply invariant is exactly: unspent lovelace +
// collected fees + deposits held = genesis lovelace.
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
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
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
	// Committee is the genesis constitutional committee.
	Committee []CommitteeSeat
	// Staking is the genesis delegation, as Shelley genesis staking: the
	// pools are registered and the delegators' stake credentials
	// registered and delegated, all with no deposit.
	Staking []GenesisStake
}

// GenesisStake is one genesis pool and the stake credentials delegated to
// it.
type GenesisStake struct {
	Pool       common.PoolRegistrationCertificate
	Delegators []common.Blake2b224
}

// DefaultParams returns ParamsFor the default devnet genesis.
func DefaultParams() conway.ConwayProtocolParameters {
	pp, err := ParamsFor(genesis.Default())
	if err != nil {
		// Unreachable: the default spec ships cost models for its version.
		panic("fakechain: default params: " + err.Error())
	}
	return pp
}

// ParamsFor returns ouroboros-mock's mainnet-like Conway parameters with
// the protocol version, execution limits and cost models of the devnet
// genesis s, so a fake run and a devnet judge scripts alike.
func ParamsFor(s genesis.Spec) (conway.ConwayProtocolParameters, error) {
	pp := mockledger.NewMockConwayProtocolParams()
	cms, err := genesis.CostModels(s)
	if err != nil {
		return pp, fmt.Errorf("fakechain: params: %w", err)
	}
	pp.ProtocolVersion.Major, pp.ProtocolVersion.Minor = s.ProtocolVersion[0], s.ProtocolVersion[1]
	pp.CostModels = cms
	pp.MaxTxExUnits = exUnits(s.TxExUnits)
	pp.MaxBlockExUnits = exUnits(s.BlockExUnits)
	return pp, nil
}

// exUnits converts a genesis budget; genesis.ParseOverride bounds each
// value below 2^62, so it fits an int64.
func exUnits(u genesis.ExUnits) common.ExUnits {
	return common.ExUnits{Memory: int64(u.Memory), Steps: int64(u.Steps)}
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
	ls       *ledgerView
	capBytes uint64

	mu sync.Mutex
	// protects everything below
	slot      uint64
	nextBlock uint64
	faults    []Scheduled
	utxo      utxoSet
	certs     *certState
	fees      uint64
	supply    uint64
	blocks    []record
	// immutable is the number of leading blocks that can never be rolled
	// back. It only grows, so after a rollback of depth d the next one reaches
	// at most k-d further back until new blocks arrive; blocks[immutable:]
	// keep their undo records.
	immutable int
	pool      mempool
	include   []TxID
	overfull  bool
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
		certs:     newCertState(cfg.Committee),
	}
	c.certs.stake(cfg.Staking)
	if c.capBytes == 0 {
		c.capBytes = 2 * uint64(cfg.Params.MaxBlockBodySize)
	}
	if err := c.loadGenesis(cfg.Genesis); err != nil {
		return nil, err
	}
	c.pool = newMempool(c.utxo, c.certs)
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
// read: UTxO lookup, network id and slot arithmetic here, delegation and
// governance state in ledgerView.
func (c *Chain) ledgerState() *ledgerView {
	g := c.cfg.Slots
	start := time.Unix(0, 0).UTC()
	ls := mockledger.NewLedgerStateBuilder().
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
		WithConstitutionValue(&common.Constitution{}).
		WithGovPurposeRootsValue(&common.GovPurposeRoots{}).
		Build()
	return &ledgerView{MockLedgerState: ls}
}

// effects returns what certState.apply needs. A proposal can be voted on up to
// the end of the epoch govActionLifetime epochs after the one it was made in.
func (c *Chain) effects() effects {
	g, life := c.cfg.Slots, uint64(c.pp.GovActionValidityPeriod)
	return effects{pp: c.pp, expiry: func(slot uint64) uint64 {
		return g.SlotAt(g.EpochStart(g.EpochOfSlot(slot)+life+1)) - 1
	}}
}

// check runs the rule list against view at slot. The caller holds c.mu.
func (c *Chain) check(tx *conway.ConwayTransaction, slot uint64, view *overlay) []Failure {
	c.view, c.ls.certs = view.get, view.certs
	defer func() { c.view, c.ls.certs = nil, nil }()
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
	slot := c.mempoolSlot()
	if fs := c.check(tx, slot, c.pool.view); len(fs) > 0 {
		return &RejectError{Tx: tx.Hash(), Failures: fs}
	}
	c.pool.push(tx, slot, c.effects())
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
	u := undo{certs: c.certs.clone()}
	r := c.effects()
	c.certs.newEpoch(c.cfg.Slots.EpochOfSlot(c.slot), len(c.blocks) == 0, c.utxo.stakeByCred())
	for _, tx := range txs {
		c.utxo.applyTx(tx, &u)
		c.certs.apply(tx, c.slot, r)
	}
	c.fees += u.fees
	b, err := c.encode(txs)
	if err != nil {
		return err
	}
	c.blocks = append(c.blocks, record{Block: b, txs: txs, undo: u})
	c.settle()
	c.revalidate()
	check.PostFn(c.conserved, "fakechain: supply conserved")
	return nil
}

// assemble chooses the block's transactions: the Include list if one is
// pending, otherwise the mempool in order. Each is revalidated at the block
// slot against the block so far; failures are evicted. Assembly stops at the
// first transaction that does not fit the body size or the execution
// budget, as the node takes the longest mempool prefix within the block
// capacity (ouroboros-consensus Mempool/Impl/Common.hs, splitAfterTxSize
// over blockCapacityTxMeasure, at 136122cc). So a transaction within the
// per-transaction budget but over the per-block one is never forged and
// holds back every later one until it leaves the mempool.
func (c *Chain) assemble() []*conway.ConwayTransaction {
	cands, overfull := c.pool.txs, c.overfull
	c.overfull = false
	if c.include != nil {
		cands = c.pool.pick(c.include)
		c.include = nil
	}
	view := newOverlay(c.utxo, c.certs)
	r := c.effects()
	var chosen []*conway.ConwayTransaction
	var used blockUse
	for _, tx := range cands {
		if fs := c.check(tx, c.slot, view); len(fs) > 0 {
			c.evict(tx, ReasonInvalid, fs)
			continue
		}
		next := used.add(tx)
		if !overfull && !next.fits(c.pp) {
			break
		}
		used = next
		view.apply(tx, c.slot, r)
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
	c.pool = newMempool(c.utxo, c.certs)
	slot := c.mempoolSlot()
	r := c.effects()
	for _, tx := range old {
		if fs := c.check(tx, slot, c.pool.view); len(fs) > 0 {
			c.evicted = append(c.evicted, Eviction{Tx: tx.Hash(), Slot: c.slot, Reason: ReasonInvalid, Failures: fs})
			continue
		}
		c.pool.push(tx, slot, r)
	}
}

// settle makes the block k deep immutable, if the chain is long enough, and
// forgets the undo records of the blocks it newly covers.
func (c *Chain) settle() {
	n := len(c.blocks)
	if n <= int(c.cfg.K) {
		return
	}
	to := n - int(c.cfg.K)
	for i := c.immutable; i < to; i++ {
		c.blocks[i].undo = undo{}
		c.blocks[i].txs = nil
	}
	c.immutable = max(c.immutable, to)
}

func (c *Chain) conserved() bool {
	var sum uint64
	for _, u := range c.utxo {
		sum += u.Output.Amount().Uint64()
	}
	return sum+c.fees+c.certs.deposits() == c.supply
}

// rollback removes the last depth blocks. It refuses, changing nothing, a
// depth that would remove an immutable block.
func (c *Chain) rollback(depth uint64, readd bool) error {
	if depth == 0 || depth > uint64(len(c.blocks)-c.immutable) {
		return fmt.Errorf("%w: depth %d, height %d, immutable %d", ErrTooDeep, depth, len(c.blocks), c.immutable)
	}
	cut := len(c.blocks) - int(depth)
	var back []*conway.ConwayTransaction
	for i := len(c.blocks) - 1; i >= cut; i-- {
		c.utxo.revert(c.blocks[i].undo)
		c.fees -= c.blocks[i].undo.fees
		c.certs = c.blocks[i].undo.certs
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
