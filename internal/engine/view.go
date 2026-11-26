package engine

import (
	"fmt"

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
