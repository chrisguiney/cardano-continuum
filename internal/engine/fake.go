package engine

import (
	"context"
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// NewFakeChain returns a fake chain holding cfg's genesis funds, with the
// given faults scheduled.
func NewFakeChain(cfg *Config, faults ...fakechain.Scheduled) (*fakechain.Chain, error) {
	funds, err := cfg.Genesis()
	if err != nil {
		return nil, err
	}
	gs := make([]fakechain.GenesisOutput, len(funds))
	for i, f := range funds {
		gs[i] = fakechain.GenesisOutput{Address: f.Address, Lovelace: f.Lovelace}
	}
	owners, staking, err := GenesisStaking(cfg)
	if err != nil {
		return nil, err
	}
	root, err := keys.New(cfg.Seed, cfg.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	var seats []fakechain.CommitteeSeat
	for i := range cfg.Committee {
		cold, _ := root.Committee(uint32(i))
		seats = append(seats, fakechain.CommitteeSeat{Cold: common.Blake2b224(cold), Expiry: 1 << 20})
	}
	return fakechain.New(fakechain.Config{
		Committee: seats,
		Slots:     cfg.Slots, NetworkID: cfg.NetworkID, K: cfg.K, BlockEvery: cfg.BlockEvery, Genesis: append(gs, owners...),
		Faults: faults, Staking: staking,
	})
}

// FakeNode adapts a fake chain to Node and Pacer for `continuum run --fake`
// and tests.
type FakeNode struct {
	Chain *fakechain.Chain
	Slots sched.Slots
	// BlockEvery is the chain's block interval; the pacer wakes at each
	// block slot.
	BlockEvery uint64
	// evicted is how many of the chain's evictions Dropped has reported.
	evicted int
}

// Dropped implements Dropper: the fake chain's evictions since the last
// call.
func (f *FakeNode) Dropped() []TxID {
	ev := f.Chain.Evictions()
	var out []TxID
	for _, x := range ev[min(f.evicted, len(ev)):] {
		out = append(out, TxID(x.Tx))
	}
	f.evicted = len(ev)
	return out
}

// Submit implements Node.
func (f *FakeNode) Submit(ctx context.Context, tx []byte) error {
	return f.Chain.Submit(ctx, fakechain.EraConway, tx)
}

// HasTx implements Node.
func (f *FakeNode) HasTx(id TxID) bool { return f.Chain.HasTx(fakechain.TxID(id)) }

// Intersect implements Node.
func (f *FakeNode) Intersect(points []eventlog.ChainPoint) (Follower, eventlog.ChainPoint, error) {
	ps := make([]ocommon.Point, len(points))
	for i, p := range points {
		if p.Slot == 0 && len(p.Hash) == 0 {
			ps[i] = ocommon.NewPointOrigin()
		} else {
			ps[i] = ocommon.NewPoint(p.Slot, p.Hash)
		}
	}
	fl, at, err := f.Chain.FollowFrom(ps, 0)
	if err != nil {
		return nil, eventlog.ChainPoint{}, err
	}
	return fakeFollower{fl}, chainPoint(at), nil
}

// Advance implements Pacer: the fake chain's clock follows the run's.
func (f *FakeNode) Advance(t sched.Time) error { return f.Chain.AdvanceToTime(t) }

// NextChange implements Pacer: the start of the next block slot after now.
func (f *FakeNode) NextChange(now sched.Time) sched.Time {
	slot := f.Slots.SlotAt(now)
	return f.Slots.SlotStart((slot/f.BlockEvery + 1) * f.BlockEvery)
}

func chainPoint(p ocommon.Point) eventlog.ChainPoint {
	if len(p.Hash) == 0 {
		return eventlog.ChainPoint{Slot: p.Slot}
	}
	return eventlog.ChainPoint{Slot: p.Slot, Hash: append([]byte(nil), p.Hash...)}
}

type fakeFollower struct{ f *fakechain.Follower }

func (ff fakeFollower) Next() (ChainEvent, bool, error) {
	ev, ok := ff.f.Next()
	if !ok {
		return ChainEvent{}, false, nil
	}
	if ev.Kind == fakechain.RollBackward {
		return ChainEvent{Rollback: true, Point: chainPoint(ev.Point)}, true, nil
	}
	content, err := Digest(ev.Block.Cbor)
	if err != nil {
		return ChainEvent{}, false, err
	}
	return ChainEvent{Point: chainPoint(ev.Block.Point()), Content: content}, true, nil
}

// Digest reads a Conway block into BlockContent and encodes it. It is the
// I/O-side half of a BlockApplied input: the engine sees only the digest.
func Digest(blockCbor []byte) ([]byte, error) {
	blk, err := conway.NewConwayBlockFromCbor(blockCbor)
	if err != nil {
		return nil, fmt.Errorf("engine: decode block: %w", err)
	}
	return digestOf(chain.BlockFrom(blk))
}

// digestOf encodes a block as chain-sync delivered it. Consumed, Produced
// and the fee already follow the isValid rule (chain.TxFrom).
func digestOf(b chain.Block) ([]byte, error) {
	c := BlockContent{Number: b.Number}
	for _, tx := range b.Txs {
		d := TxDigest{ID: TxID(tx.Hash), Fee: tx.Fee, Proposals: uint32(len(tx.Proposals))}
		for _, in := range tx.Spends {
			d.Inputs = append(d.Inputs, Ref{Tx: TxID(in.Tx), Index: in.Index})
		}
		for _, u := range tx.Creates {
			addr, err := u.Output.Address().Bytes()
			if err != nil {
				return nil, fmt.Errorf("engine: output address: %w", err)
			}
			d.Outputs = append(d.Outputs, TxOut{Index: u.Id.Index(), Address: addr, Lovelace: u.Output.Amount().Uint64(), Cbor: u.Output.Cbor()})
		}
		for _, cert := range tx.Certs {
			raw := cert.Cbor()
			if len(raw) == 0 {
				var err error
				if raw, err = cbor.Encode(cert); err != nil {
					return nil, fmt.Errorf("engine: encode certificate: %w", err)
				}
			}
			d.Certs = append(d.Certs, raw)
		}
		for _, v := range tx.Votes {
			d.Votes = append(d.Votes, VoteDigest{
				Kind: v.Voter.Kind, Hash: v.Voter.Hash, Tx: TxID(v.Action.Tx), Index: v.Action.Index, Choice: v.Choice,
			})
		}
		c.Txs = append(c.Txs, d)
	}
	return eventlog.Encode(c)
}
