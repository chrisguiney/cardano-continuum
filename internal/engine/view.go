package engine

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

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

func (e *Engine) viewConfig() chain.Config {
	return chain.Config{K: e.cfg.K, Slots: e.cfg.Slots, Book: e.book, NOpt: e.cfg.NOpt, GovLifetime: e.cfg.GovLifetime}
}

// book adds an actor's keys to the view's book: its address and stake
// credential, and its pool or DRep key by profile kind.
func (e *Engine) bookActor(id population.ActorID, p profile.Profile, st *actor) {
	_ = e.book.AddAddress(st.wallet.Address(), id)
	e.book.AddStake(common.Blake2b224(st.wallet.StakeKeyHash()), id)
	switch p.Kind {
	case profile.Pool:
		if g, ok := e.genesisOf(id); ok {
			e.book.AddPool(common.Blake2b224(g.ID), id)
			e.pools[id] = g.ID
		} else if pool, ok := e.poolID(id); ok {
			e.book.AddPool(pool, id)
			e.pools[id] = pool
		}
	case profile.DRep:
		e.book.AddDRep(common.Blake2b224(e.keys.DRep(keysID(id))), id)
	}
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
func (v view) Pools() []agenda.PoolInfo { return v.e.view.Pools() }
func (v view) Epoch() uint64            { return v.e.cfg.Slots.EpochAt(v.e.now) }
func (v view) StakeRegistered(id population.ActorID) bool {
	return v.e.view.StakeRegistered(id)
}
func (v view) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	return v.e.view.DelegatedTo(id)
}
func (v view) PoolRegistered(id population.ActorID) bool { return v.e.view.PoolRegistered(id) }

// OpenProposals implements agenda.Governance.
func (v view) OpenProposals() []agenda.ProposalInfo { return v.e.view.OpenProposals() }

// Voted implements agenda.Governance.
func (v view) Voted(a population.ActorID, p agenda.ProposalID) bool { return v.e.view.Voted(a, p) }

// DRepRegistered implements agenda.Governance.
func (v view) DRepRegistered(a population.ActorID) bool { return v.e.view.DRepRegistered(a) }

// DReps implements agenda.Governance.
func (v view) DReps() []population.ActorID { return v.e.view.DReps() }

// VoteDelegated implements agenda.Governance.
func (v view) VoteDelegated(a population.ActorID) bool { return v.e.view.VoteDelegated(a) }

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
