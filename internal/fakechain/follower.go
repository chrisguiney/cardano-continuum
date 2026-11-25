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

// Follow returns a follower starting at the origin that stays lag blocks
// behind the tip.
func (c *Chain) Follow(lag int) *Follower { return &Follower{c: c, lag: max(lag, 0)} }

// Next returns the next event, or false when the follower is caught up. A
// follower that has read a block a rollback removed is first rolled back to
// the deepest block it shares with the chain.
func (f *Follower) Next() (Event, bool) {
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	visible := max(len(c.blocks)-f.lag, 0)
	tip := c.tip(visible)
	if j := f.intersect(); j < len(f.read) {
		f.read = f.read[:j]
		return Event{Kind: RollBackward, Point: c.tip(j), Tip: tip}, true
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

// FollowFrom is chain-sync's FindIntersect: it returns a follower positioned
// after the first of points that is on the chain, and that point. Clients
// offer points newest first, so the answer can be older than their cursor
// when the chain rolled back while they were away.
func (c *Chain) FollowFrom(points []ocommon.Point, lag int) (*Follower, ocommon.Point, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range points {
		n, ok := c.height(p)
		if !ok {
			continue
		}
		f := &Follower{c: c, lag: max(lag, 0)}
		for _, r := range c.blocks[:n] {
			f.read = append(f.read, r.Hash)
		}
		return f, c.tip(n), nil
	}
	return nil, ocommon.Point{}, ErrNoIntersect
}

// height returns the height of the block at p (0 for the origin). The caller
// holds c.mu.
func (c *Chain) height(p ocommon.Point) (int, bool) {
	if p.Slot == 0 && len(p.Hash) == 0 {
		return 0, true
	}
	for i, r := range c.blocks {
		if r.Slot == p.Slot && bytes.Equal(r.Hash[:], p.Hash) {
			return i + 1, true
		}
	}
	return 0, false
}
