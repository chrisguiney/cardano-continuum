package chain

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/population"
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
type View struct {
	cfg Config

	tip      Point
	height   uint64
	settled  Point
	settledH uint64

	utxo    map[Ref]common.Utxo
	owner   map[Ref]population.ActorID
	balance map[population.ActorID]uint64
	stake   map[common.Blake2b224]StakeState
	pools   map[common.Blake2b224]PoolState
	dreps   map[common.Blake2b224]bool
	// epoch state: see ledger.go
	ep epochState
	// governance state: see gov.go
	props map[GovID]proposal
	votes map[voteKey]uint8
	// window holds the blocks above the settled point, oldest first. It is
	// never longer than K.
	window []undo

	returns map[population.ActorID]float64
}

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
		cfg: cfg, tip: s.At, height: s.Height, settled: s.At, settledH: s.Height,
		utxo:    map[Ref]common.Utxo{},
		owner:   map[Ref]population.ActorID{},
		balance: map[population.ActorID]uint64{},
		stake:   map[common.Blake2b224]StakeState{},
		pools:   map[common.Blake2b224]PoolState{},
		dreps:   map[common.Blake2b224]bool{},
		ep:      epochState{label: s.Label, frozen: s.Frozen, mark: slices.Clone(s.Mark), set: slices.Clone(s.Set)},
		props:   map[GovID]proposal{},
		votes:   map[voteKey]uint8{},
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

// Tip is the point of the last applied block.
func (v *View) Tip() Point { return v.tip }

// Settled is the K-deep point: the newest block that has had K successors
// in the view. It never moves back.
func (v *View) Settled() Point { return v.settled }

// Utxo returns the tracked output at r.
func (v *View) Utxo(r Ref) (common.Utxo, bool) {
	u, ok := v.utxo[r]
	return u, ok
}

// UtxosAt returns the tracked outputs paying a, sorted by reference.
func (v *View) UtxosAt(a common.Address) []common.Utxo {
	want, err := addrKey(a)
	if err != nil {
		return nil
	}
	var out []common.Utxo
	for _, u := range v.sorted() {
		if k, err := addrKey(u.Output.Address()); err == nil && k == want {
			out = append(out, u)
		}
	}
	return out
}

// Apply folds b onto the tip. It returns the effects in transaction order,
// then a Settled effect for each block b gave its Kth successor.
func (v *View) Apply(b Block) ([]Effect, error) {
	if !v.tip.IsOrigin() && (b.Prev != v.tip.Hash || b.Point.Slot <= v.tip.Slot) {
		return nil, fmt.Errorf("%w: block %d at slot %d", ErrNotConnected, b.Number, b.Point.Slot)
	}
	u := undo{point: b.Point, number: b.Number, prev: v.tip, prevH: v.height}
	v.newEpoch(b, &u)
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
		if v.applyCert(c, u) {
			eff = append(eff, Effect{Kind: CertSeen, Tx: tx.Hash, Cert: c})
		}
	}
	if v.applyGov(tx, u) {
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
	v.restoreEpoch(u)
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
	c.utxo = maps.Clone(v.utxo)
	c.owner = maps.Clone(v.owner)
	c.balance = maps.Clone(v.balance)
	c.stake = maps.Clone(v.stake)
	c.pools = maps.Clone(v.pools)
	c.dreps = maps.Clone(v.dreps)
	c.props = maps.Clone(v.props)
	c.votes = maps.Clone(v.votes)
	c.window = slices.Clone(v.window)
	return &c
}

// add stores u if the book names its address, and reports whether it did.
func (v *View) add(u common.Utxo) bool {
	id, ok := v.cfg.Book.owner(u.Output.Address())
	if !ok {
		return false
	}
	r := RefOf(u.Id)
	v.utxo[r] = u
	v.owner[r] = id
	v.balance[id] += u.Output.Amount().Uint64()
	return true
}

func (v *View) remove(r Ref) {
	u, ok := v.utxo[r]
	if !ok {
		return
	}
	id := v.owner[r]
	v.balance[id] -= u.Output.Amount().Uint64()
	if v.balance[id] == 0 {
		delete(v.balance, id)
	}
	delete(v.utxo, r)
	delete(v.owner, r)
}

func (v *View) sorted() []common.Utxo {
	refs := slices.Collect(maps.Keys(v.utxo))
	slices.SortFunc(refs, Ref.compare)
	var out []common.Utxo
	for _, r := range refs {
		out = append(out, v.utxo[r])
	}
	return out
}

// consistent reports whether the balance index matches the outputs.
func (v *View) consistent() bool {
	sum := map[population.ActorID]uint64{}
	for r, u := range v.utxo {
		sum[v.owner[r]] += u.Output.Amount().Uint64()
	}
	return maps.Equal(sum, v.balance)
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
	epoch *epochState
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
