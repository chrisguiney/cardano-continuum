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

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"

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
// serialised by world.mu, so determinism needs only a deterministic caller.
//
// The state lives in world, behind one lock. Behaviour is split across
// unlocked parts that each take the world and assume the caller holds its
// lock: txpool (mempool validation and eviction), producer (blocks and
// rollbacks), follow (chain-sync positions) and reader (the read-only
// queries, whose methods Chain gains by embedding it and which take the
// lock themselves). Exported methods that mutate take the lock and forward
// to a part.
type Chain struct {
	*world
	reader
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
	w := &world{
		cfg:       cfg,
		pp:        cfg.Params,
		capBytes:  cfg.MempoolBytes,
		nextBlock: cfg.BlockEvery,
		utxo:      utxoSet{},
		certs:     newCertState(cfg.Committee),
	}
	w.certs.stake(cfg.Staking)
	if w.capBytes == 0 {
		w.capBytes = 2 * uint64(cfg.Params.MaxBlockBodySize)
	}
	if err := loadGenesis(w, cfg.Genesis); err != nil {
		return nil, err
	}
	w.pool = newMempool(w.utxo, w.certs)
	faults, err := sortFaults(cfg.Faults)
	if err != nil {
		return nil, err
	}
	w.faults = faults
	w.ls = newLedgerView(w)
	return &Chain{world: w, reader: reader{w}}, nil
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
	return txpool{c.world}.submit(tx)
}

// AdvanceTo moves the clock to slot, firing scheduled faults and producing
// every block due on the way. A failing fault stops the clock at its slot.
func (c *Chain) AdvanceTo(slot uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot < c.slot {
		return fmt.Errorf("fakechain: clock at slot %d cannot move back to %d", c.slot, slot)
	}
	p := producer{c.world}
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
		if err := p.step(); err != nil {
			return err
		}
	}
}

// AdvanceToTime moves the clock to the slot containing t.
func (c *Chain) AdvanceToTime(t sched.Time) error { return c.AdvanceTo(c.cfg.Slots.SlotAt(t)) }

// Follow returns a follower starting at the origin that stays lag blocks
// behind the tip.
func (c *Chain) Follow(lag int) *Follower { return &Follower{c: c, lag: max(lag, 0)} }

// FollowFrom is chain-sync's FindIntersect: it returns a follower positioned
// after the first of points that is on the chain, and that point. Clients
// offer points newest first, so the answer can be older than their cursor
// when the chain rolled back while they were away.
func (c *Chain) FollowFrom(points []ocommon.Point, lag int) (*Follower, ocommon.Point, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return follow{c.world}.from(c, points, lag)
}

// Inject applies f now, at the current slot.
func (c *Chain) Inject(f Fault) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return f.apply(c.world)
}
