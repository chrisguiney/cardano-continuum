package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
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
		Faults: faults, Staking: staking, Params: cfg.ChainParams,
	})
}

// FakeGenesis is the genesis identity of cfg's fake chain, as a devnet
// with network magic would answer it: what adversary.Clear compares for a
// fake run.
func FakeGenesis(cfg *Config, magic uint32) chain.Genesis {
	return chain.Genesis{
		Magic: magic, Start: time.UnixMilli(0).UTC(), EpochLength: cfg.Slots.SlotAt(cfg.Slots.EpochStart(1)),
		SecurityParam: cfg.K, SlotLength: time.Duration(cfg.Slots.SlotStart(1)) * time.Millisecond,
	}
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

// Identity implements Identifier.
func (f *FakeNode) Identity() oracle.Node { return oracle.Node{Impl: "fakechain", Pool: "fakechain"} }

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
	content, err := wire.Digest(ev.Block.Cbor)
	if err != nil {
		return ChainEvent{}, false, err
	}
	return ChainEvent{Point: chainPoint(ev.Block.Point()), Content: content}, true, nil
}
