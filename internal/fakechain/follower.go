package fakechain

import (
	"bytes"
	"errors"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// EventKind is a chain-sync instruction.
type EventKind int

// Chain-sync instructions.
const (
	RollForward EventKind = iota + 1
	RollBackward
)

// Event is one chain-sync message: a block to apply, or a point to roll back
// to. Tip is the chain tip as this follower may see it.
type Event struct {
	Kind  EventKind
	Block Block
	Point ocommon.Point
	Tip   ocommon.Point
}

// Follower reads the chain as a chain-sync client would, optionally behind
// the tip by a fixed number of blocks, as a slow peer is.
type Follower struct {
	c   *Chain
	lag int
	// read holds the hash of each block delivered, by height. It is always
	// a prefix of the chain as it was when delivered.
	read []common.Blake2b256
}

// Next returns the next event, or false when the follower is caught up. A
// follower that has read a block a rollback removed is first rolled back to
// the deepest block it shares with the chain.
func (f *Follower) Next() (Event, bool) {
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	visible := max(len(c.blocks)-f.lag, 0)
	fl := follow{c.world}
	tip := fl.tip(visible)
	if j := f.intersect(); j < len(f.read) {
		f.read = f.read[:j]
		return Event{Kind: RollBackward, Point: fl.tip(j), Tip: tip}, true
	}
	n := len(f.read)
	if n >= visible {
		return Event{}, false
	}
	b := c.blocks[n].Block
	f.read = append(f.read, b.Hash)
	return Event{Kind: RollForward, Block: b, Tip: tip}, true
}

// intersect returns the height of the deepest block the follower has read
// that is still on the chain. The caller holds f.c.mu.
func (f *Follower) intersect() int {
	blocks := f.c.blocks
	j := min(len(f.read), len(blocks))
	// Rollbacks only shorten the chain from the tip, so the common prefix
	// ends at the last height where the hashes still agree.
	for j > 0 && blocks[j-1].Hash != f.read[j-1] {
		j--
	}
	return j
}

// ErrNoIntersect: none of the offered points is on the chain.
var ErrNoIntersect = errors.New("fakechain: no intersection")

// follow resolves chain-sync positions over the world. Its methods assume the
// caller holds world.mu.
type follow struct{ w *world }

// from is Chain.FollowFrom's body: a follower for c positioned after the
// first of points that is on the chain.
func (f follow) from(c *Chain, points []ocommon.Point, lag int) (*Follower, ocommon.Point, error) {
	for _, p := range points {
		n, ok := f.height(p)
		if !ok {
			continue
		}
		fl := &Follower{c: c, lag: max(lag, 0)}
		for _, r := range f.w.blocks[:n] {
			fl.read = append(fl.read, r.Hash)
		}
		return fl, f.tip(n), nil
	}
	return nil, ocommon.Point{}, ErrNoIntersect
}

// tip returns the point of the block at height n (origin for 0).
func (f follow) tip(n int) ocommon.Point {
	if n == 0 {
		return ocommon.NewPointOrigin()
	}
	return f.w.blocks[n-1].Point()
}

// height returns the height of the block at p (0 for the origin).
func (f follow) height(p ocommon.Point) (int, bool) {
	if p.Slot == 0 && len(p.Hash) == 0 {
		return 0, true
	}
	for i, r := range f.w.blocks {
		if r.Slot == p.Slot && bytes.Equal(r.Hash[:], p.Hash) {
			return i + 1, true
		}
	}
	return 0, false
}
