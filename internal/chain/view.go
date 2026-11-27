package chain

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Errors returned by the view.
var (
	// ErrNotConnected: the block does not extend the view's tip.
	ErrNotConnected = errors.New("chain: block does not extend the tip")
	// ErrTooDeep: the rollback target is below the settled point or not on
	// the view's chain. Within the security parameter a node never asks for
	// one, so it is an error, never a silent re-sync.
	ErrTooDeep = errors.New("chain: rollback below the settled point")
)

// Config is fixed for a view's lifetime.
type Config struct {
	// K is the security parameter: a block with K blocks after it is
	// settled.
	K     uint64
	Slots sched.Slots
	Book  *Book
	// NOpt is the protocol parameter nOpt (desired number of pools). Zero
	// means no saturation cap is known.
	NOpt uint64
	// GovLifetime is govActionLifetime in epochs: a proposal made in epoch e
	// can be voted on through epoch e + GovLifetime.
	GovLifetime uint64
}

// View is the engine's knowledge of the chain: the UTxOs, stake, pools and
// DReps the Book names, as of the tip. It is pure: Apply and Rollback fold
// blocks, and the same blocks give the same view whatever rollbacks came
// between.
//
// Blocks above the settled point keep undo records, so a rollback down to
// the settled point is exact. The settled point is a high-water mark: it
// advances when a block gets its Kth successor and never moves back, so
// a rollback past it is ErrTooDeep. That is the planner-reservations
// contract (models/specs/planner-reservations, rule 5) and engine-recovery
// design A, where durable state comes from settled blocks only.
//
// View is a composite: each part owns its state and the methods on it, and
// the parts' reads are promoted. View itself folds blocks across the parts
// and answers the reads that need more than one.
//
//	```mermaid
//	flowchart LR
//	  view[View: Apply / Rollback] --> span[span: tip, settled, window]
//	  view --> utxos[utxos: outputs, balances]
//	  view --> accounts[accounts: stake creds, DReps]
//	  view --> registry[registry: pools]
//	  view --> gov[gov: proposals, votes]
//	  view --> ep[epochs: mark, set]
//	  ep -- distribution --> accounts
//	  ep -- reap --> registry
//	```
type View struct {
	cfg Config
	span
	utxos
	accounts
	registry
	// epoch state and the stake snapshots: see ledger.go
	ep epochs
	gov
}

// span is where the view stands on the chain: the tip, the settled point
// and the blocks between them.
type span struct {
	slots    sched.Slots
	tip      Point
	height   uint64
	settled  Point
	settledH uint64
	// window holds the blocks above the settled point, oldest first. It is
	// never longer than K.
	window []undo
}

// Tip is the point of the last applied block.
func (s *span) Tip() Point { return s.tip }

// Settled is the K-deep point: the newest block that has had K successors
// in the view. It never moves back.
func (s *span) Settled() Point { return s.settled }

// Epoch is the epoch of the tip's slot.
func (s *span) Epoch() uint64 { return s.slots.EpochOfSlot(s.tip.Slot) }

// StakeState is a tracked stake credential.
type StakeState struct {
	Cred       common.Blake2b224
	Registered bool
	Delegated  bool
	Pool       common.Blake2b224
	Votes      bool
	DRep       DRep
}

// DRep is a vote delegation target: a DRep credential, or the
// always-abstain and no-confidence options with no credential. Kind
// follows the ledger's drep encoding.
type DRep struct {
	Kind int
	Cred common.Blake2b224
}

// PoolState is a tracked pool.
type PoolState struct {
	ID common.Blake2b224
	// Retiring means the pool retires at the start of epoch RetireAt.
	Retiring bool
	RetireAt uint64
	// Retired is set by the epoch transition that applied the retirement.
	Retired bool
}

// State is a view's content at one point, sorted, for starting a view and
// for comparing two. Base gives the state at the settled point, which is
// what a restart resumes from.
type State struct {
	At     Point
	Height uint64
	Utxos  []common.Utxo
	Stake  []StakeState
	Pools  []PoolState
	DReps  []common.Blake2b224
	// Label, Frozen, Mark and Set are the epoch state (see Label, Frozen
	// and the snapshots in ledger.go).
	Label  uint64
	Frozen bool
	Mark   Snapshot
	Set    Snapshot
	// Proposals and Votes are the tracked governance state.
	Proposals []ProposalState
	Votes     []VoteState
}

// New returns a view holding s, with s.At as both tip and settled point.
// Outputs the book does not name are dropped.
func New(cfg Config, s State) (*View, error) {
	if cfg.K == 0 || cfg.Book == nil {
		return nil, fmt.Errorf("chain: new view: need K > 0 and a book (K = %d)", cfg.K)
	}
	v := &View{
		cfg:      cfg,
		span:     span{slots: cfg.Slots, tip: s.At, height: s.Height, settled: s.At, settledH: s.Height},
		utxos:    newUtxos(cfg.Book),
		accounts: newAccounts(cfg.Book),
		registry: newRegistry(cfg.Book),
		ep:       epochs{label: s.Label, frozen: s.Frozen, mark: slices.Clone(s.Mark), set: slices.Clone(s.Set)},
		gov:      newGov(cfg.Book),
	}
	for _, p := range s.Proposals {
		v.props[p.ID] = proposal{expires: p.Expires, typ: p.Type, pools: p.Pools, deposit: p.Deposit}
	}
	for _, x := range s.Votes {
		v.votes[voteKey{x.Action, x.Voter}] = x.Choice
	}
	for _, u := range s.Utxos {
		v.add(u)
	}
	for _, st := range s.Stake {
		v.stake[st.Cred] = st
	}
	for _, p := range s.Pools {
		v.pools[p.ID] = p
	}
	for _, d := range s.DReps {
		v.dreps[d] = true
	}
	return v, nil
}

// PoolRegistered reports whether the pool actor's pool is registered and
// not yet past its retirement epoch.
func (v *View) PoolRegistered(id population.ActorID) bool {
	pool, ok := v.cfg.Book.poolOf[id]
	return ok && v.live(pool, v.Epoch())
}

// Pools lists the registered pools the book names, by actor ID, with the
// returns last given to SetReturns. Stake is what is delegated to the pool
// now; Saturation is the stake cap, the tracked delegated total over nOpt
// (zero without Config.NOpt). The total counts only what the book names, so
// on a devnet where the engine's population holds all stake it is the
// network's.
func (v *View) Pools() []agenda.PoolInfo {
	per, total := poolStake(v.distribution(v.credStake()))
	var limit profile.Lovelace
	if v.cfg.NOpt > 0 {
		limit = profile.Lovelace(total / v.cfg.NOpt)
	}
	var out []agenda.PoolInfo
	for id, st := range v.pools {
		actor := v.cfg.Book.pools[id]
		if v.live(st.ID, v.Epoch()) {
			out = append(out, agenda.PoolInfo{
				ID: actor, ROI: v.returns[actor],
				Stake: profile.Lovelace(per[id]), Saturation: limit,
			})
		}
	}
	slices.SortFunc(out, func(a, b agenda.PoolInfo) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// OpenProposals lists the tracked governance actions whose last epoch has
// not passed, by ID.
func (v *View) OpenProposals() []agenda.ProposalInfo { return v.open(v.Epoch()) }

// VotingPower is the lovelace behind a voter's vote now: for a DRep the
// stake of every credential that delegates its vote to it, for a pool the
// stake delegated to the pool. A committee member's vote is one vote, not
// stake, so it has none, as has a voter the view does not track. Only the
// outputs the book names count, so this is the power the run's own
// actors give.
func (v *View) VotingPower(x Voter) uint64 {
	switch x.Kind {
	case common.VoterTypeDRepKeyHash, common.VoterTypeDRepScriptHash:
		stake := v.credStake()
		var sum uint64
		for cred, s := range v.stake {
			if s.Registered && s.Votes && s.DRep.Kind == drepKindOf(x.Kind) && s.DRep.Cred == x.Hash {
				sum += stake[cred]
			}
		}
		return sum
	case common.VoterTypeStakingPoolKeyHash:
		per, _ := poolStake(v.distribution(v.credStake()))
		return per[x.Hash]
	}
	return 0
}

// PolicyState builds the stake-floor guard's input (internal/policy,
// models/specs/stake-floor) from the view.
//
// epoch is the current epoch by slot time, which the view cannot know: its
// own epoch is that of the tip. Pods and inflight come from the engine: the
// plan and observed pod health, and the transactions submitted but neither
// confirmed nor expired.
//
// Label, Set and Mark come from the tip, so they agree with each other: Set
// and Mark are what the last transition on the tip's chain took. Deleg and
// Pools come from the settled point, since the contract counts a delegation
// or certificate as confirmed only K deep (rule 3, engine-recovery design
// A); a certificate still above it is neither in Deleg nor in Pools. A pool
// the view has never seen registered is left out.
func (v *View) PolicyState(epoch uint64, pods agenda.Pods, inflight []policy.Intent) policy.State {
	return policyState(v, epoch, pods, inflight)
}

// Apply folds b onto the tip. It returns the effects in transaction order,
// then a Settled effect for each block b gave its Kth successor.
func (v *View) Apply(b Block) ([]Effect, error) {
	if !v.tip.IsOrigin() && (b.Prev != v.tip.Hash || b.Point.Slot <= v.tip.Slot) {
		return nil, fmt.Errorf("%w: block %d at slot %d", ErrNotConnected, b.Number, b.Point.Slot)
	}
	u := undo{point: b.Point, number: b.Number, prev: v.tip, prevH: v.height}
	v.ep.next(v.cfg.Slots.EpochOfSlot(b.Point.Slot), v.tip.IsOrigin(), &v.utxos, &v.accounts, &v.registry, &u)
	var eff []Effect
	for _, tx := range b.Txs {
		eff = append(eff, v.applyTx(tx, &u)...)
	}
	v.tip, v.height = b.Point, b.Number
	v.window = append(v.window, u)
	for uint64(len(v.window)) > v.cfg.K {
		s := v.window[0]
		v.window = v.window[1:]
		v.settled, v.settledH = s.point, s.number
		eff = append(eff, Effect{Kind: Settled, Point: s.point, Txs: s.hashes()})
	}
	check.Post(uint64(len(v.window)) <= v.cfg.K, "chain: window within K")
	check.PostFn(v.consistent, "chain: balances match outputs")
	return eff, nil
}

func (v *View) applyTx(tx Tx, u *undo) []Effect {
	var eff []Effect
	tu := txUndo{hash: tx.Hash}
	for _, r := range tx.Spends {
		old, ok := v.utxo[r]
		if !ok {
			continue
		}
		v.remove(r)
		tu.spent = append(tu.spent, old)
		eff = append(eff, Effect{Kind: Spent, Tx: tx.Hash, Ref: r})
	}
	for _, c := range tx.Creates {
		if !v.add(c) {
			continue
		}
		r := RefOf(c.Id)
		tu.created = append(tu.created, r)
		eff = append(eff, Effect{Kind: Created, Tx: tx.Hash, Ref: r, Utxo: c})
	}
	for _, c := range tx.Certs {
		if applyCert(&v.accounts, &v.registry, c, u) {
			eff = append(eff, Effect{Kind: CertSeen, Tx: tx.Hash, Cert: c})
		}
	}
	if v.applyGov(tx, v.cfg.Slots.EpochOfSlot(u.point.Slot)+v.cfg.GovLifetime, u) {
		eff = append(eff, Effect{Kind: GovSeen, Tx: tx.Hash})
	}
	if len(eff) == 0 {
		return nil
	}
	u.txs = append(u.txs, tu)
	return append([]Effect{{Kind: Confirmed, Tx: tx.Hash}}, eff...)
}

// Rollback unwinds the view to the block at to, newest block first and,
// within a block, newest transaction first. A target below the settled
// point, or not on the view's chain, is ErrTooDeep and changes nothing.
func (v *View) Rollback(to Point) ([]Effect, error) {
	if to == v.tip {
		return nil, nil
	}
	keep := -1
	if to == v.settled {
		keep = 0
	}
	for i, u := range v.window {
		if u.point == to {
			keep = i + 1
		}
	}
	if keep < 0 {
		return nil, fmt.Errorf("%w: slot %d (settled at slot %d)", ErrTooDeep, to.Slot, v.settled.Slot)
	}
	var eff []Effect
	for len(v.window) > keep {
		u := v.window[len(v.window)-1]
		v.window = v.window[:len(v.window)-1]
		eff = append(eff, v.unwind(u)...)
	}
	check.PostFn(v.consistent, "chain: balances match outputs")
	return eff, nil
}

func (v *View) unwind(u undo) []Effect {
	var eff []Effect
	for _, tu := range slices.Backward(u.txs) {
		for _, r := range slices.Backward(tu.created) {
			v.remove(r)
			eff = append(eff, Effect{Kind: Removed, Tx: tu.hash, Ref: r})
		}
		for _, old := range slices.Backward(tu.spent) {
			v.add(old)
			eff = append(eff, Effect{Kind: Restored, Tx: tu.hash, Ref: RefOf(old.Id), Utxo: old})
		}
		eff = append(eff, Effect{Kind: RolledBack, Tx: tu.hash})
	}
	restore(u.stake, v.stake)
	restore(u.pools, v.pools)
	restore(u.dreps, v.dreps)
	v.ep.restore(u)
	v.restoreGov(u)
	v.tip, v.height = u.prev, u.prevH
	return eff
}

// Snapshot returns the view's state at the tip.
func (v *View) Snapshot() State {
	s := State{At: v.tip, Height: v.height, Utxos: v.sorted()}
	for _, k := range sortedKeys(v.stake) {
		s.Stake = append(s.Stake, v.stake[k])
	}
	for _, k := range sortedKeys(v.pools) {
		s.Pools = append(s.Pools, v.pools[k])
	}
	s.DReps = sortedKeys(v.dreps)
	s.Label, s.Frozen = v.ep.label, v.ep.frozen
	s.Mark, s.Set = slices.Clone(v.ep.mark), slices.Clone(v.ep.set)
	s.Proposals, s.Votes = v.govState()
	return s
}

// Base returns the view's state at the settled point: the tip with every
// block above the settled point unwound. A restart starts a view from it
// and has chain-sync intersect at its point, so no settled block is
// replayed and none is missed.
func (v *View) Base() State { return v.Confirmed().Snapshot() }

// Confirmed returns a copy of the view at its settled point: the tip with
// every block above it unwound. It shares nothing with v.
func (v *View) Confirmed() *View {
	c := v.clone()
	for len(c.window) > 0 {
		u := c.window[len(c.window)-1]
		c.window = c.window[:len(c.window)-1]
		c.unwind(u)
	}
	return c
}

func (v *View) clone() *View {
	c := *v
	c.utxos = v.utxos.clone()
	c.accounts = v.accounts.clone()
	c.registry = v.registry.clone()
	c.gov = v.gov.clone()
	c.window = slices.Clone(v.window)
	return &c
}

// undo is what Rollback needs to remove one block.
type undo struct {
	point  Point
	number uint64
	prev   Point
	prevH  uint64
	txs    []txUndo
	// first value each key had before this block, absent when ok is false
	stake map[common.Blake2b224]prior[StakeState]
	pools map[common.Blake2b224]prior[PoolState]
	dreps map[common.Blake2b224]prior[bool]
	// epoch is set when the block moved the epoch state, gov when it
	// touched proposals or votes; each holds what to put back.
	epoch *epochs
	gov   *govUndo
}

type txUndo struct {
	hash    TxID
	spent   []common.Utxo
	created []Ref
}

func (u undo) hashes() []TxID {
	out := make([]TxID, len(u.txs))
	for i, t := range u.txs {
		out[i] = t.hash
	}
	return out
}

type prior[V any] struct {
	v  V
	ok bool
}

// remember records cur[k] in log the first time a block touches k.
func remember[V any](log *map[common.Blake2b224]prior[V], cur map[common.Blake2b224]V, k common.Blake2b224) {
	if *log == nil {
		*log = map[common.Blake2b224]prior[V]{}
	}
	if _, done := (*log)[k]; done {
		return
	}
	old, ok := cur[k]
	(*log)[k] = prior[V]{old, ok}
}

// restore puts back every remembered value. Keys are distinct, so the
// map's iteration order does not matter.
func restore[V any](log map[common.Blake2b224]prior[V], cur map[common.Blake2b224]V) {
	for k, p := range log {
		if p.ok {
			cur[k] = p.v
		} else {
			delete(cur, k)
		}
	}
}

func sortedKeys[V any](m map[common.Blake2b224]V) []common.Blake2b224 {
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, func(a, b common.Blake2b224) int { return slices.Compare(a[:], b[:]) })
	return keys
}
