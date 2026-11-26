package forger

import (
	"fmt"
	"slices"
)

// entry is a block of the followed chain with the state after it.
type entry struct {
	Block
	nonces Nonces
	// counter is the pool's highest operational certificate counter up to
	// this block; forged is false until the pool has a block.
	counter uint64
	forged  bool
}

// offer is a forged block shown only to chosen peers.
type offer struct {
	hash [32]byte
	// to is empty for every peer.
	to []PeerID
}

// Chain is what the forger knows and what each peer is shown. The followed
// chain is the authority: it is the upstream node's selection. On top of it
// sit the forger's own honest blocks the upstream has not adopted yet, and
// offers, the blocks a misbehaviour shows to some peers in place of those.
//
// A peer's view is the followed chain, then the own blocks, unless an offer
// applies to the peer: then the view ends at the offered block. An invalid
// block is offered to a peer once. A node drops the connection on it, and
// would be dropped again at every reconnect if it met the block again.
//
// Chain is not safe for concurrent use; Node guards it.
type Chain struct {
	p    Params
	pool [32]byte

	blocks map[[32]byte]Block
	// followed is the upstream chain, oldest first, and at its index.
	followed []entry
	at       map[[32]byte]int
	own      []Block
	offers   []offer
	served   map[PeerID]map[[32]byte]bool
}

// NewChain is an empty chain for the pool with cold key issuer.
func NewChain(p Params, issuer [32]byte) *Chain {
	return &Chain{
		p: p, pool: issuer,
		blocks: map[[32]byte]Block{}, at: map[[32]byte]int{},
		served: map[PeerID]map[[32]byte]bool{},
	}
}

// Extend adds the upstream's next block. An own block the upstream adopted
// becomes part of the followed chain; any other block there means the
// upstream chose differently, and the own blocks and offers are dropped.
func (c *Chain) Extend(b Block) error {
	last := c.last()
	if !last.Origin && b.Prev != last.Hash {
		return fmt.Errorf("block %x at slot %d does not extend the followed tip %x", b.Hash[:8], b.Slot, last.Hash[:8])
	}
	e := entry{Block: b, nonces: c.followedNonces().Apply(c.p, b.Slot, b.Prev, b.VRFOutput)}
	if n := len(c.followed); n > 0 {
		e.counter, e.forged = c.followed[n-1].counter, c.followed[n-1].forged
	}
	if b.Issuer == c.pool {
		e.counter, e.forged = max(e.counter, b.Counter), true
	}
	c.blocks[b.Hash] = b
	c.at[b.Hash] = len(c.followed)
	c.followed = append(c.followed, e)
	if len(c.own) > 0 && c.own[0].Hash == b.Hash {
		c.own = c.own[1:]
	} else {
		c.own = nil
	}
	c.offers = nil
	return nil
}

// Rollback cuts the followed chain back to the block with hash, or to the
// origin. It reports false for a block that is not on it.
func (c *Chain) Rollback(origin bool, hash [32]byte) bool {
	keep := 0
	if !origin {
		i, ok := c.at[hash]
		if !ok {
			return false
		}
		keep = i + 1
	}
	for _, e := range c.followed[keep:] {
		delete(c.at, e.Hash)
	}
	c.followed = c.followed[:keep]
	c.own, c.offers = nil, nil
	return true
}

func (c *Chain) last() Tip {
	if len(c.followed) == 0 {
		return Tip{Origin: true}
	}
	return c.followed[len(c.followed)-1].Tip()
}

func (c *Chain) followedNonces() Nonces {
	if len(c.followed) == 0 {
		return GenesisNonces(c.p)
	}
	return c.followed[len(c.followed)-1].nonces
}

// Tip is the forger's selected tip: the followed chain and its own blocks.
func (c *Chain) Tip() Tip {
	if n := len(c.own); n > 0 {
		return c.own[n-1].Tip()
	}
	return c.last()
}

// Nonces is the nonce state at the selected tip.
func (c *Chain) Nonces() Nonces {
	n := c.followedNonces()
	for _, b := range c.own {
		n = n.Apply(c.p, b.Slot, b.Prev, b.VRFOutput)
	}
	return n
}

// Counter is the pool's highest operational certificate counter on the
// selected chain, and whether it has forged at all.
func (c *Chain) Counter() (uint64, bool) {
	var counter uint64
	forged := false
	if n := len(c.followed); n > 0 {
		counter, forged = c.followed[n-1].counter, c.followed[n-1].forged
	}
	for _, b := range c.own {
		counter, forged = max(counter, b.Counter), true
	}
	return counter, forged
}

// Add records a forged block. A valid block for every peer joins the own
// chain when it extends the selected tip; anything else is an offer.
func (c *Chain) Add(b Block, f Forge) {
	tip := c.Tip()
	c.blocks[b.Hash] = b
	if f.Expect == Adopt && len(f.SendTo) == 0 && b.Prev == tip.Hash && (tip.Origin || b.Slot > tip.Slot) {
		c.own = append(c.own, b)
		return
	}
	c.offers = append(c.offers, offer{hash: b.Hash, to: slices.Clone(f.SendTo)})
}

// Cursor is the last point a peer was sent.
type Cursor struct {
	Origin bool
	Hash   [32]byte
}

// Move is what chain-sync sends a peer next.
type Move uint8

// Moves.
const (
	// Wait: the peer is at the tip of its view.
	Wait Move = iota
	// Forward: send Step.Block.
	Forward
	// Backward: roll the peer back to Step.To.
	Backward
)

// Step is one chain-sync reply.
type Step struct {
	Move  Move
	Block Block
	To    Cursor
}

// suffix is what follows the followed chain in peer's view. An offer
// applies while its parent is still in the view and the peer has not been
// served it, or is standing on it.
func (c *Chain) suffix(peer PeerID, cur Cursor) []Block {
	for i := len(c.offers) - 1; i >= 0; i-- {
		o := c.offers[i]
		if len(o.to) > 0 && !slices.Contains(o.to, peer) {
			continue
		}
		if c.served[peer][o.hash] && (cur.Origin || cur.Hash != o.hash) {
			continue
		}
		b := c.blocks[o.hash]
		if b.Prev == c.last().Hash {
			return []Block{b}
		}
		if i := slices.IndexFunc(c.own, func(x Block) bool { return x.Hash == b.Prev }); i >= 0 {
			return append(slices.Clone(c.own[:i+1]), b)
		}
	}
	return c.own
}

// position is where hash sits in the view made of the followed chain and
// suffix, counting from 0, or -1.
func (c *Chain) position(suffix []Block, hash [32]byte) int {
	if i, ok := c.at[hash]; ok {
		return i
	}
	if i := slices.IndexFunc(suffix, func(b Block) bool { return b.Hash == hash }); i >= 0 {
		return len(c.followed) + i
	}
	return -1
}

func (c *Chain) viewAt(suffix []Block, i int) Block {
	if i < len(c.followed) {
		return c.followed[i].Block
	}
	return suffix[i-len(c.followed)]
}

// Next is the reply to peer's request with its cursor at cur. Sending an
// offered block marks it served.
func (c *Chain) Next(peer PeerID, cur Cursor) Step {
	suffix := c.suffix(peer, cur)
	pos := -1
	if !cur.Origin {
		if pos = c.position(suffix, cur.Hash); pos < 0 {
			return Step{Move: Backward, To: c.ancestor(suffix, cur.Hash)}
		}
	}
	if pos+1 >= len(c.followed)+len(suffix) {
		return Step{Move: Wait}
	}
	b := c.viewAt(suffix, pos+1)
	if _, followed := c.at[b.Hash]; !followed && !c.isOwn(b.Hash) {
		if c.served[peer] == nil {
			c.served[peer] = map[[32]byte]bool{}
		}
		c.served[peer][b.Hash] = true
	}
	return Step{Move: Forward, Block: b}
}

func (c *Chain) isOwn(hash [32]byte) bool {
	return slices.ContainsFunc(c.own, func(b Block) bool { return b.Hash == hash })
}

// ancestor is the newest ancestor of hash in the view, or the origin.
func (c *Chain) ancestor(suffix []Block, hash [32]byte) Cursor {
	for {
		b, ok := c.blocks[hash]
		if !ok {
			return Cursor{Origin: true}
		}
		if c.position(suffix, b.Prev) >= 0 {
			return Cursor{Hash: b.Prev}
		}
		hash = b.Prev
	}
}

// Intersect is the first of points in peer's view, for a peer whose cursor
// is not yet set.
func (c *Chain) Intersect(peer PeerID, points []Cursor) (Cursor, bool) {
	suffix := c.suffix(peer, Cursor{Origin: true})
	for _, pt := range points {
		if pt.Origin || c.position(suffix, pt.Hash) >= 0 {
			return pt, true
		}
	}
	return Cursor{}, false
}

// maxRange bounds one block-fetch reply.
const maxRange = 2000

// Range is the blocks from one hash to another along their own ancestry,
// oldest first. It serves any block the forger knows, on whichever fork: a
// peer fetches what a header it was sent points at.
func (c *Chain) Range(from, to [32]byte) ([]Block, bool) {
	var out []Block
	for hash := to; len(out) < maxRange; {
		b, ok := c.blocks[hash]
		if !ok {
			return nil, false
		}
		out = append(out, b)
		if hash == from {
			slices.Reverse(out)
			return out, true
		}
		hash = b.Prev
	}
	return nil, false
}

// Block looks a known block up by hash.
func (c *Chain) Block(hash [32]byte) (Block, bool) {
	b, ok := c.blocks[hash]
	return b, ok
}
