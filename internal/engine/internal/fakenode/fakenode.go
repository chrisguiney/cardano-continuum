// Package fakenode adapts the fake chain to the engine's node and pacer
// interfaces for `continuum run --fake` and tests.
package fakenode

import (
	"context"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Node adapts a fake chain to the engine's Node and Pacer for `continuum run --fake`
// and tests.
type Node struct {
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
func (f *Node) Dropped() []wire.TxID {
	ev := f.Chain.Evictions()
	var out []wire.TxID
	for _, x := range ev[min(f.evicted, len(ev)):] {
		out = append(out, wire.TxID(x.Tx))
	}
	f.evicted = len(ev)
	return out
}

// Identity implements Identifier.
func (f *Node) Identity() oracle.Node { return oracle.Node{Impl: "fakechain", Pool: "fakechain"} }

// Submit implements Node.
func (f *Node) Submit(ctx context.Context, tx []byte) error {
	return f.Chain.Submit(ctx, fakechain.EraConway, tx)
}

// HasTx implements Node.
func (f *Node) HasTx(id wire.TxID) bool { return f.Chain.HasTx(fakechain.TxID(id)) }

// Intersect implements Node.
func (f *Node) Intersect(points []eventlog.ChainPoint) (wire.Follower, eventlog.ChainPoint, error) {
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
	return fakeFollower{fl}, ChainPoint(at), nil
}

// Advance implements Pacer: the fake chain's clock follows the run's.
func (f *Node) Advance(t sched.Time) error { return f.Chain.AdvanceToTime(t) }

// NextChange implements Pacer: the start of the next block slot after now.
func (f *Node) NextChange(now sched.Time) sched.Time {
	slot := f.Slots.SlotAt(now)
	return f.Slots.SlotStart((slot/f.BlockEvery + 1) * f.BlockEvery)
}

// ChainPoint converts a chain-sync point to a log point.
func ChainPoint(p ocommon.Point) eventlog.ChainPoint {
	if len(p.Hash) == 0 {
		return eventlog.ChainPoint{Slot: p.Slot}
	}
	return eventlog.ChainPoint{Slot: p.Slot, Hash: append([]byte(nil), p.Hash...)}
}

type fakeFollower struct{ f *fakechain.Follower }

func (ff fakeFollower) Next() (wire.ChainEvent, bool, error) {
	ev, ok := ff.f.Next()
	if !ok {
		return wire.ChainEvent{}, false, nil
	}
	if ev.Kind == fakechain.RollBackward {
		return wire.ChainEvent{Rollback: true, Point: ChainPoint(ev.Point)}, true, nil
	}
	content, err := wire.Digest(ev.Block.Cbor)
	if err != nil {
		return wire.ChainEvent{}, false, err
	}
	return wire.ChainEvent{Point: ChainPoint(ev.Block.Point()), Content: content}, true, nil
}
