package window

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
)

func encodeView(s chain.State) (wire.ViewRec, error) {
	r := wire.ViewRec{
		At: s.At.Log(), Height: s.Height, Stake: s.Stake, Pools: s.Pools, DReps: s.DReps, Label: s.Label,
		Frozen: s.Frozen, Mark: s.Mark, Set: s.Set, Proposals: s.Proposals, Votes: s.Votes,
	}
	for _, u := range s.Utxos {
		raw := u.Output.Cbor()
		if len(raw) == 0 {
			var err error
			if raw, err = cbor.Encode(u.Output); err != nil {
				return wire.ViewRec{}, fmt.Errorf("engine: encode view output: %w", err)
			}
		}
		r.Utxos = append(r.Utxos, wire.UtxoRec{Ref: wire.Ref{Tx: wire.TxID(u.Id.Id()), Index: u.Id.Index()}, Out: raw})
	}
	return r, nil
}

func decodeView(r wire.ViewRec) (chain.State, error) {
	at, ok := chain.PointFromLog(r.At)
	if !ok {
		return chain.State{}, fmt.Errorf("%w: view at slot %d", wire.ErrBadPoint, r.At.Slot)
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
