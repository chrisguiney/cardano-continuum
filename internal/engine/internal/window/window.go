// Package window is the engine's chain side of the engine-recovery
// contract, design A (models/specs/engine-recovery/EngineRecovery.tla): the
// volatile window of up to K blocks above the confirmed point, the chain
// view those blocks fold into and the book it names actors by. Durable state
// comes only from K-deep blocks, so no rollback the node can make reaches it.
//
// The spec's pure Step maps onto the methods as follows; the composite calls
// the planner ledger between them, in the same order for live and replay.
//
//   - Empty: New, then Open with the genesis state and a zero Saved.
//   - Step on a block (k = "fwd"): Blocks reads it on top of the tip, Push
//     appends it (pts), Confirm moves each block with K successors below the
//     confirmed point (conf; Depth = K) and counts it. Nothing below Depth
//     is ever counted (StableOnly).
//   - Feasible on a rollback (k = "back"): Check. A point below the
//     confirmed one is refused with ErrRollbackBelowConfirmed, never
//     silently reset (NeverHalted); Check changes nothing.
//   - Step on a rollback: Rollback cuts the window; conf is unchanged, since
//     a feasible rollback never reaches it.
//   - Confirmed(s) and s.pts: Confirmed, Points and Tip. Points always start
//     at the confirmed point, so FindIntersect never falls back to origin.
//   - The checkpoint's st: Save. Restart's replay of a checkpoint is Load,
//     then Blocks and Push per saved block.
//
// Tick, the log, the checkpoint and Restart are not here: the clock
// package and the engine's Runner own them.
package window

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

var (
	// ErrRollbackBelowConfirmed: the chain rolled back past the K-deep point
	// durable state came from. Design A cannot unwind it, so the engine stops
	// instead of silently resetting.
	ErrRollbackBelowConfirmed = errors.New("engine: rollback below the confirmed point")
	// ErrUnknownPoint: a rollback names a point the engine never applied.
	ErrUnknownPoint = errors.New("engine: rollback to an unknown point")
)

// Members finds the population member an output pays.
type Members interface {
	// Owner is the member whose address addr is.
	Owner(addr []byte) (population.ActorID, bool)
	// Address is the member's wallet address.
	Address(id population.ActorID) common.Address
}

// Window is the volatile K-block window over the confirmed point, the chain
// view the same blocks fold into and the book it names actors by.
type Window struct {
	// cfg.Book is the window's own book.
	cfg chain.Config
	// sv.Conf only moves forward; blocks are the volatile blocks above it,
	// oldest first.
	sv     Saved
	blocks []wire.Block
	view   *chain.View
}

// Saved is the window's durable state besides the view and blocks: the
// confirmed point, and what confirmed blocks paid in fees and to addresses
// outside the population.
type Saved struct {
	// Conf is the confirmed point; it only moves forward.
	Conf eventlog.ChainPoint `json:"conf"`
	// Fees is the lovelace confirmed blocks paid in fees.
	Fees uint64 `json:"fees"`
	// Foreign is the lovelace confirmed blocks paid outside the population.
	Foreign uint64 `json:"foreign"`
	// Inflow is the lovelace settled reward withdrawals took from reward
	// accounts, from outside the genesis total.
	Inflow uint64 `json:"inflow,omitzero"`
}

// Image is the window's section of a snapshot: its durable counters, the
// volatile blocks oldest first, and the view at the confirmed point.
type Image struct {
	// Saved are the durable counters, inlined into the section.
	Saved
	// Blocks are the volatile blocks above Conf, oldest first.
	Blocks []wire.Block `json:"blocks"`
	// View is the chain view at Conf.
	View wire.ViewRec `json:"view"`
}

// Cut is a rollback the window has checked: the point it unwinds to and how
// many volatile blocks stay.
type Cut struct {
	to   chain.Point
	keep int
}

// To is the point the rollback unwinds to.
func (c Cut) To() chain.Point { return c.to }

// New is an empty window for a view configured by cfg. cfg.Book is ignored:
// the window keeps its own, which Book returns.
func New(cfg chain.Config) *Window {
	cfg.Book = chain.NewBook()
	return &Window{cfg: cfg}
}

// Open builds the view from s, the state at the confirmed point sv.Conf.
func (w *Window) Open(s chain.State, sv Saved) error {
	v, err := chain.New(w.cfg, s)
	if err != nil {
		return err
	}
	w.view, w.sv = v, sv
	return nil
}

// Saved is the window's durable state besides the view and blocks.
func (w *Window) Saved() Saved { return w.sv }

// Save is the window's snapshot section.
func (w *Window) Save() (Image, error) {
	v, err := encodeView(w.view.Base())
	if err != nil {
		return Image{}, err
	}
	return Image{Saved: w.sv, Blocks: slices.Clone(w.blocks), View: v}, nil
}

// Load opens the view a Save recorded at its confirmed point. It does not
// replay img.Blocks: the planner ledger folds the same blocks, so the
// composite feeds each to both through Blocks and Push, oldest first.
func (w *Window) Load(img Image) error {
	s, err := decodeView(img.View)
	if err != nil {
		return err
	}
	if err := w.Open(s, img.Saved); err != nil {
		return fmt.Errorf("engine: snapshot view: %w", err)
	}
	return nil
}

// Book names the actors the view tracks.
func (w *Window) Book() *chain.Book { return w.cfg.Book }

// View is the chain view; nil before Open.
func (w *Window) View() *chain.View { return w.view }

// Blocks is b as the ledger and as the view read it, on top of the tip.
func (w *Window) Blocks(b wire.Block, m Members) (lb, vb chain.Block, err error) {
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
func (w *Window) Push(b wire.Block, vb chain.Block) error {
	if _, err := w.view.Apply(vb); err != nil {
		return err
	}
	w.blocks = append(w.blocks, b)
	return nil
}

// Confirm moves every block with K successors below the confirmed point,
// counting its fees and its outputs outside the population.
func (w *Window) Confirm(m Members) {
	for uint64(len(w.blocks)) > w.cfg.K {
		b := w.blocks[0]
		fees, foreign := tally(b, m)
		w.sv.Fees += fees
		w.sv.Foreign += foreign
		w.sv.Conf = b.Point
		w.blocks = slices.Delete(w.blocks, 0, 1)
	}
	check.Post(uint64(len(w.blocks)) <= w.cfg.K, "window: Confirm leaves at most K volatile blocks")
}

// Check finds the rollback to p without changing anything. A point below
// the confirmed one is refused: durable state never unwinds.
func (w *Window) Check(p eventlog.ChainPoint) (Cut, error) {
	keep := -1
	if samePoint(p, w.sv.Conf) {
		keep = 0
	}
	for i := len(w.blocks) - 1; i >= 0 && keep < 0; i-- {
		if samePoint(w.blocks[i].Point, p) {
			keep = i + 1
		}
	}
	if keep < 0 {
		if p.Slot < w.sv.Conf.Slot {
			return Cut{}, fmt.Errorf("%w: slot %d, confirmed slot %d", ErrRollbackBelowConfirmed, p.Slot, w.sv.Conf.Slot)
		}
		return Cut{}, fmt.Errorf("%w: slot %d", ErrUnknownPoint, p.Slot)
	}
	lp, _ := chain.PointFromLog(p)
	return Cut{to: lp, keep: keep}, nil
}

// Rollback unwinds the view and the window to c, which Check returned for
// the current window, and returns the blocks it removed, oldest first.
func (w *Window) Rollback(c Cut) ([]wire.Block, error) {
	check.Pre(c.keep <= len(w.blocks), "window: Rollback of a Cut from an older window")
	if _, err := w.view.Rollback(c.to); err != nil {
		return nil, err
	}
	gone := slices.Clone(w.blocks[c.keep:])
	w.blocks = w.blocks[:c.keep]
	return gone, nil
}

// Confirmed is the K-deep point durable state comes from.
func (w *Window) Confirmed() eventlog.ChainPoint { return w.sv.Conf }

// Tip is the last block applied.
func (w *Window) Tip() eventlog.ChainPoint {
	if n := len(w.blocks); n > 0 {
		return w.blocks[n-1].Point
	}
	return w.sv.Conf
}

// Points are the intersection points, oldest first: the confirmed point and
// every volatile block above it, so FindIntersect never has to fall back
// below the confirmed point.
func (w *Window) Points() []eventlog.ChainPoint {
	out := []eventlog.ChainPoint{w.sv.Conf}
	for _, b := range w.blocks {
		out = append(out, b.Point)
	}
	return out
}

// Inflow counts n lovelace a settled reward withdrawal brought in.
func (w *Window) Inflow(n uint64) { w.sv.Inflow += n }

// Reclaim takes n back out of the foreign count, not below zero: value the
// population got back from outside it after it had counted as foreign.
func (w *Window) Reclaim(n uint64) { w.sv.Foreign -= min(w.sv.Foreign, n) }

// Utxo is a lovelace-only output paying addr at r.
func Utxo(addr common.Address, r wire.Ref, lovelace uint64) common.Utxo {
	o := babbage.BabbageTransactionOutput{
		OutputAddress: addr,
		OutputAmount:  mary.MaryTransactionOutputValue{Amount: lovelace},
	}
	return common.Utxo{Id: shelley.ShelleyTransactionInput{TxId: common.Blake2b256(r.Tx), OutputIndex: r.Index}, Output: &o}
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}

// tally is what b paid in fees and to addresses outside the population.
func tally(b wire.Block, m Members) (fees, foreign uint64) {
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
func ledgerBlock(prev eventlog.ChainPoint, b wire.Block, m Members) (chain.Block, error) {
	p, ok := chain.PointFromLog(b.Point)
	if !ok {
		return chain.Block{}, fmt.Errorf("%w: slot %d, hash of %d bytes", wire.ErrBadPoint, b.Point.Slot, len(b.Point.Hash))
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
			t.Creates = append(t.Creates, Utxo(m.Address(owner), wire.Ref{Tx: tx.ID, Index: o.Index}, o.Lovelace))
		}
		out.Txs = append(out.Txs, t)
	}
	return out, nil
}

// viewBlock is b as the view reads it: every output (the book drops those
// it does not name), certificates, proposals and votes.
func viewBlock(prev eventlog.ChainPoint, b wire.Block) (chain.Block, error) {
	p, ok := chain.PointFromLog(b.Point)
	if !ok {
		return chain.Block{}, fmt.Errorf("%w: slot %d, hash of %d bytes", wire.ErrBadPoint, b.Point.Slot, len(b.Point.Hash))
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

func viewTx(d wire.TxDigest) (chain.Tx, error) {
	t := chain.Tx{Hash: chain.TxID(d.ID), Fee: d.Fee, Withdrawals: wire.Withdrawals(d.Withdrawals)}
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
		var g wire.ProposalDigest
		if int(i) < len(d.Gov) {
			g = d.Gov[i]
		}
		t.Proposals = append(t.Proposals, viewProposal(chain.GovID{Tx: t.Hash, Index: i}, g))
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

// viewProposal is proposal id as its digest g describes it; a digest
// without the proposal's details (an older log) gives only the ID.
func viewProposal(id chain.GovID, g wire.ProposalDigest) chain.Proposal {
	var prev *chain.GovID
	if g.Prev != nil {
		prev = &chain.GovID{Tx: chain.TxID(g.Prev.Tx), Index: g.Prev.Index}
	}
	return chain.Proposal{
		ID:          id,
		Type:        g.Type,
		Pools:       g.Pools,
		Deposit:     g.Deposit,
		Prev:        prev,
		GovGroup:    g.GovGroup,
		Version:     g.Version,
		Withdrawals: wire.Withdrawals(g.Withdrawals),
	}
}

// outputUtxo rebuilds a digest output. One with its CBOR keeps its assets
// and datum; one without (a test digest) is plain lovelace.
func outputUtxo(tx wire.TxID, o wire.TxOut) (common.Utxo, error) {
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
	return Utxo(addr, wire.Ref{Tx: tx, Index: o.Index}, o.Lovelace), nil
}
