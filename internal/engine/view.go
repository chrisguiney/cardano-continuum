package engine

import (
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// This file keeps the engine's chain.View: the delegation, pool, DRep and
// governance state the agendas and the stake-floor guard read. It is fed
// the same blocks and rollbacks as the planner's ledger, so its settled
// point is the engine's confirmed point, and its state at that point rides
// in the snapshot.

// book adds an actor's keys to the view's book: its address and stake
// credential, and its pool or DRep key by profile kind.
func (e *Engine) bookActor(id population.ActorID, p profile.Profile, st *actor) {
	_ = e.win.Book().AddAddress(st.wallet.Address(), id)
	e.win.Book().AddStake(common.Blake2b224(st.wallet.StakeKeyHash()), id)
	switch p.Kind {
	case profile.Pool:
		if g, ok := e.genesisOf(id); ok {
			e.win.Book().AddPool(common.Blake2b224(g.ID), id)
			e.pools[id] = g.ID
		} else if pool, ok := e.poolID(id); ok {
			e.win.Book().AddPool(pool, id)
			e.pools[id] = pool
		}
	case profile.DRep:
		e.win.Book().AddDRep(common.Blake2b224(e.keys.DRep(keysID(id))), id)
	}
}

// viewRec is chain.State in the snapshot. Outputs travel as CBOR, since a
// ledger output is an interface.
type viewRec struct {
	At        eventlog.ChainPoint   `cbor:"1,keyasint"`
	Height    uint64                `cbor:"2,keyasint"`
	Utxos     []utxoRec             `cbor:"3,keyasint"`
	Stake     []chain.StakeState    `cbor:"4,keyasint"`
	Pools     []chain.PoolState     `cbor:"5,keyasint"`
	DReps     []common.Blake2b224   `cbor:"6,keyasint"`
	Label     uint64                `cbor:"7,keyasint"`
	Frozen    bool                  `cbor:"8,keyasint"`
	Mark      chain.Snapshot        `cbor:"9,keyasint"`
	Set       chain.Snapshot        `cbor:"10,keyasint"`
	Proposals []chain.ProposalState `cbor:"11,keyasint"`
	Votes     []chain.VoteState     `cbor:"12,keyasint"`
}

type utxoRec struct {
	Ref Ref    `cbor:"1,keyasint"`
	Out []byte `cbor:"2,keyasint"`
}

func encodeView(s chain.State) (viewRec, error) {
	r := viewRec{
		At: s.At.Log(), Height: s.Height, Stake: s.Stake, Pools: s.Pools, DReps: s.DReps, Label: s.Label,
		Frozen: s.Frozen, Mark: s.Mark, Set: s.Set, Proposals: s.Proposals, Votes: s.Votes,
	}
	for _, u := range s.Utxos {
		raw := u.Output.Cbor()
		if len(raw) == 0 {
			var err error
			if raw, err = cbor.Encode(u.Output); err != nil {
				return viewRec{}, fmt.Errorf("engine: encode view output: %w", err)
			}
		}
		r.Utxos = append(r.Utxos, utxoRec{Ref: Ref{Tx: TxID(u.Id.Id()), Index: u.Id.Index()}, Out: raw})
	}
	return r, nil
}

func decodeView(r viewRec) (chain.State, error) {
	at, ok := chain.PointFromLog(r.At)
	if !ok {
		return chain.State{}, fmt.Errorf("%w: view at slot %d", ErrBadPoint, r.At.Slot)
	}
	s := chain.State{
		At: at, Height: r.Height, Stake: r.Stake, Pools: r.Pools, DReps: r.DReps, Label: r.Label,
		Frozen: r.Frozen, Mark: r.Mark, Set: r.Set, Proposals: r.Proposals, Votes: r.Votes,
	}
	for _, u := range r.Utxos {
		out, err := ledger.NewTransactionOutputFromCbor(u.Out)
		if err != nil {
			return chain.State{}, fmt.Errorf("engine: snapshot view output: %w", err)
		}
		in := shelley.ShelleyTransactionInput{TxId: common.Blake2b256(u.Ref.Tx), OutputIndex: u.Ref.Index}
		s.Utxos = append(s.Utxos, common.Utxo{Id: in, Output: out})
	}
	return s, nil
}

// view adapts the engine's own chain view to agenda.View and the optional
// Governance. Decisions read only this, never the live chain, so replay
// sees the same world.
type view struct{ e *Engine }

func (v view) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(v.e.Spendable(uint64(id)))
}
func (v view) Pools() []agenda.PoolInfo { return v.e.win.View().Pools() }
func (v view) Epoch() uint64            { return v.e.cfg.Slots.EpochAt(v.e.clk.Now()) }
func (v view) StakeRegistered(id population.ActorID) bool {
	return v.e.win.View().StakeRegistered(id)
}
func (v view) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	return v.e.win.View().DelegatedTo(id)
}
func (v view) PoolRegistered(id population.ActorID) bool { return v.e.win.View().PoolRegistered(id) }

// Budgets implements adversary.Sight: the run's execution limits.
func (v view) Budgets() adversary.Budgets {
	return adversary.Budgets{Tx: v.e.cfg.Build.MaxTxExUnits, Block: v.e.cfg.Build.MaxBlockExUnits}
}

// OpenProposals implements agenda.Governance.
func (v view) OpenProposals() []agenda.ProposalInfo { return v.e.win.View().OpenProposals() }

// Voted implements agenda.Governance.
func (v view) Voted(a population.ActorID, p agenda.ProposalID) bool {
	return v.e.win.View().Voted(a, p)
}

// DRepRegistered implements agenda.Governance.
func (v view) DRepRegistered(a population.ActorID) bool { return v.e.win.View().DRepRegistered(a) }

// DReps implements agenda.Governance.
func (v view) DReps() []population.ActorID { return v.e.win.View().DReps() }

// VoteDelegated implements agenda.Governance.
func (v view) VoteDelegated(a population.ActorID) bool { return v.e.win.View().VoteDelegated(a) }

var (
	_ agenda.Governance = view{}
	_ agenda.Market     = view{}
)

func keysID(id population.ActorID) keys.ActorID { return keys.ActorID(id) }

// poolID is the pool a pool actor operates: its cold key hash.
func (e *Engine) poolID(id population.ActorID) (common.Blake2b224, bool) {
	s, err := e.keys.PoolSigner(keysID(id))
	if err != nil {
		return common.Blake2b224{}, false
	}
	return common.Blake2b224(s.KeyHash()), true
}

// windowMembers finds the population member an output pays.
type windowMembers interface {
	Owner(addr []byte) (population.ActorID, bool)
	Address(id population.ActorID) common.Address
}

// window is the volatile K-block window over the confirmed point, the chain
// view the same blocks fold into and the book it names actors by.
type window struct {
	// cfg.Book is the window's own book.
	cfg chain.Config
	// conf only moves forward; blocks are the volatile blocks above it,
	// oldest first.
	conf   eventlog.ChainPoint
	blocks []blockRec
	view   *chain.View
	// fees and foreign are what confirmed blocks paid in fees and to
	// addresses outside the population.
	fees, foreign uint64
}

// windowSaved is the window's durable state besides the view and blocks.
type windowSaved struct {
	Conf          eventlog.ChainPoint
	Fees, Foreign uint64
}

// windowCut is a rollback the window has checked: the point it unwinds to
// and how many volatile blocks stay.
type windowCut struct {
	to   chain.Point
	keep int
}

// To is the point the rollback unwinds to.
func (c windowCut) To() chain.Point { return c.to }

func newWindow(cfg chain.Config) *window {
	cfg.Book = chain.NewBook()
	return &window{cfg: cfg}
}

// Open builds the view from s, the state at the confirmed point sv.Conf.
func (w *window) Open(s chain.State, sv windowSaved) error {
	v, err := chain.New(w.cfg, s)
	if err != nil {
		return err
	}
	w.view, w.conf, w.fees, w.foreign = v, sv.Conf, sv.Fees, sv.Foreign
	return nil
}

// Saved is the window's durable state besides the view and blocks.
func (w *window) Saved() windowSaved {
	return windowSaved{Conf: w.conf, Fees: w.fees, Foreign: w.foreign}
}

// Book names the actors the view tracks.
func (w *window) Book() *chain.Book { return w.cfg.Book }

// View is the chain view; nil before Open.
func (w *window) View() *chain.View { return w.view }

// Blocks is b as the ledger and as the view read it, on top of the tip.
func (w *window) Blocks(b blockRec, m windowMembers) (lb, vb chain.Block, err error) {
	if lb, err = ledgerBlock(w.Tip(), b, m); err != nil {
		return chain.Block{}, chain.Block{}, err
	}
	if vb, err = viewBlock(w.Tip(), b); err != nil {
		return chain.Block{}, chain.Block{}, err
	}
	return lb, vb, nil
}

// Push applies vb, the view's reading of b, and puts b on the window. It
// confirms nothing: Confirm does.
func (w *window) Push(b blockRec, vb chain.Block) error {
	if _, err := w.view.Apply(vb); err != nil {
		return err
	}
	w.blocks = append(w.blocks, b)
	return nil
}

// Confirm moves every block with K successors below the confirmed point,
// counting its fees and its outputs outside the population.
func (w *window) Confirm(m windowMembers) {
	for uint64(len(w.blocks)) > w.cfg.K {
		b := w.blocks[0]
		fees, foreign := tally(b, m)
		w.fees += fees
		w.foreign += foreign
		w.conf = b.Point
		w.blocks = slices.Delete(w.blocks, 0, 1)
	}
}

// Check finds the rollback to p without changing anything. A point below
// the confirmed one is refused: durable state never unwinds.
func (w *window) Check(p eventlog.ChainPoint) (windowCut, error) {
	keep := -1
	if samePoint(p, w.conf) {
		keep = 0
	}
	for i := len(w.blocks) - 1; i >= 0 && keep < 0; i-- {
		if samePoint(w.blocks[i].Point, p) {
			keep = i + 1
		}
	}
	if keep < 0 {
		if p.Slot < w.conf.Slot {
			return windowCut{}, fmt.Errorf("%w: slot %d, confirmed slot %d", ErrRollbackBelowConfirmed, p.Slot, w.conf.Slot)
		}
		return windowCut{}, fmt.Errorf("%w: slot %d", ErrUnknownPoint, p.Slot)
	}
	lp, _ := chain.PointFromLog(p)
	return windowCut{to: lp, keep: keep}, nil
}

// Rollback unwinds the view and the window to c and returns the blocks it
// removed, oldest first.
func (w *window) Rollback(c windowCut) ([]blockRec, error) {
	if _, err := w.view.Rollback(c.to); err != nil {
		return nil, err
	}
	gone := slices.Clone(w.blocks[c.keep:])
	w.blocks = w.blocks[:c.keep]
	return gone, nil
}

// Confirmed is the K-deep point durable state comes from.
func (w *window) Confirmed() eventlog.ChainPoint { return w.conf }

// Tip is the last block applied.
func (w *window) Tip() eventlog.ChainPoint {
	if n := len(w.blocks); n > 0 {
		return w.blocks[n-1].Point
	}
	return w.conf
}

// Points are the intersection points, oldest first: the confirmed point and
// every volatile block above it, so FindIntersect never has to fall back
// below the confirmed point.
func (w *window) Points() []eventlog.ChainPoint {
	out := []eventlog.ChainPoint{w.conf}
	for _, b := range w.blocks {
		out = append(out, b.Point)
	}
	return out
}

// Volatile is a copy of the blocks above the confirmed point, oldest first.
func (w *window) Volatile() []blockRec { return slices.Clone(w.blocks) }

// Reclaim takes n back out of the foreign count, not below zero: value the
// population got back from outside it after it had counted as foreign.
func (w *window) Reclaim(n uint64) { w.foreign -= min(w.foreign, n) }

// tally is what b paid in fees and to addresses outside the population.
func tally(b blockRec, m windowMembers) (fees, foreign uint64) {
	for _, tx := range b.Txs {
		for _, o := range tx.Outputs {
			if _, ok := m.Owner(o.Address); !ok {
				foreign += o.Lovelace
			}
		}
		fees += tx.Fee
	}
	return fees, foreign
}

// ledgerBlock is b as the ledger reads it. The digest has no previous hash;
// the follower delivers blocks in chain order, so it is the engine's tip.
// Only outputs to the population are passed: the ledger tracks no others.
func ledgerBlock(prev eventlog.ChainPoint, b blockRec, m windowMembers) (chain.Block, error) {
	p, ok := chain.PointFromLog(b.Point)
	if !ok {
		return chain.Block{}, fmt.Errorf("%w: slot %d, hash of %d bytes", ErrBadPoint, b.Point.Slot, len(b.Point.Hash))
	}
	pp, _ := chain.PointFromLog(prev)
	out := chain.Block{Point: p, Prev: pp.Hash}
	for _, tx := range b.Txs {
		t := chain.Tx{Hash: chain.TxID(tx.ID)}
		for _, r := range tx.Inputs {
			t.Spends = append(t.Spends, chain.Ref{Tx: chain.TxID(r.Tx), Index: r.Index})
		}
		for _, o := range tx.Outputs {
			owner, ok := m.Owner(o.Address)
			if !ok {
				continue
			}
			t.Creates = append(t.Creates, ownedUtxo(m.Address(owner), Ref{Tx: tx.ID, Index: o.Index}, o.Lovelace))
		}
		out.Txs = append(out.Txs, t)
	}
	return out, nil
}

// viewBlock is b as the view reads it: every output (the book drops those
// it does not name), certificates, proposals and votes.
func viewBlock(prev eventlog.ChainPoint, b blockRec) (chain.Block, error) {
	p, ok := chain.PointFromLog(b.Point)
	if !ok {
		return chain.Block{}, fmt.Errorf("%w: slot %d, hash of %d bytes", ErrBadPoint, b.Point.Slot, len(b.Point.Hash))
	}
	pp, _ := chain.PointFromLog(prev)
	out := chain.Block{Point: p, Prev: pp.Hash, Number: b.Number}
	for _, d := range b.Txs {
		tx, err := viewTx(d)
		if err != nil {
			return chain.Block{}, fmt.Errorf("engine: block at slot %d: %w", b.Point.Slot, err)
		}
		out.Txs = append(out.Txs, tx)
	}
	return out, nil
}

func viewTx(d TxDigest) (chain.Tx, error) {
	t := chain.Tx{Hash: chain.TxID(d.ID), Fee: d.Fee}
	for _, r := range d.Inputs {
		t.Spends = append(t.Spends, chain.Ref{Tx: chain.TxID(r.Tx), Index: r.Index})
	}
	for _, o := range d.Outputs {
		u, err := outputUtxo(d.ID, o)
		if err != nil {
			return chain.Tx{}, err
		}
		t.Creates = append(t.Creates, u)
	}
	for _, raw := range d.Certs {
		var w common.CertificateWrapper
		if _, err := cbor.Decode(raw, &w); err != nil {
			return chain.Tx{}, fmt.Errorf("decode certificate: %w", err)
		}
		t.Certs = append(t.Certs, w.Certificate)
	}
	for i := range d.Proposals {
		p := chain.Proposal{ID: chain.GovID{Tx: t.Hash, Index: i}}
		if int(i) < len(d.Gov) {
			g := d.Gov[i]
			p.Type, p.Pools, p.Deposit = g.Type, g.Pools, g.Deposit
		}
		t.Proposals = append(t.Proposals, p)
	}
	for _, v := range d.Votes {
		t.Votes = append(t.Votes, chain.Vote{
			Voter:  chain.Voter{Kind: v.Kind, Hash: common.Blake2b224(v.Hash)},
			Action: chain.GovID{Tx: chain.TxID(v.Tx), Index: v.Index},
			Choice: v.Choice,
		})
	}
	return t, nil
}

// outputUtxo rebuilds a digest output. One with its CBOR keeps its assets
// and datum; one without (a test digest) is plain lovelace.
func outputUtxo(tx TxID, o TxOut) (common.Utxo, error) {
	in := shelley.ShelleyTransactionInput{TxId: common.Blake2b256(tx), OutputIndex: o.Index}
	if len(o.Cbor) > 0 {
		out, err := ledger.NewTransactionOutputFromCbor(o.Cbor)
		if err != nil {
			return common.Utxo{}, fmt.Errorf("decode output %d: %w", o.Index, err)
		}
		return common.Utxo{Id: in, Output: out}, nil
	}
	addr, err := common.NewAddressFromBytes(o.Address)
	if err != nil {
		return common.Utxo{}, fmt.Errorf("output %d address: %w", o.Index, err)
	}
	return ownedUtxo(addr, Ref{Tx: tx, Index: o.Index}, o.Lovelace), nil
}
