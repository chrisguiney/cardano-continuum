package forger

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/check"
)

// offer is a forged block shown only to chosen peers.
type offer struct {
	hash [32]byte
	// to is empty for every peer.
	to []PeerID
}

// views is what each peer is shown of a Chain: the followed chain, then the
// own blocks, unless an offer applies to the peer. Like Chain it is guarded
// by the caller.
type views struct {
	c      *Chain
	offers []offer
	served map[PeerID]map[[32]byte]bool
}

// suffix is what follows the followed chain in peer's view. An offer
// applies while its parent is still in the view and the peer has not been
// served it, or is standing on it.
func (v *views) suffix(peer PeerID, cur Cursor) []Block {
	for i := len(v.offers) - 1; i >= 0; i-- {
		o := v.offers[i]
		if len(o.to) > 0 && !slices.Contains(o.to, peer) {
			continue
		}
		if v.served[peer][o.hash] && (cur.Origin || cur.Hash != o.hash) {
			continue
		}
		b := v.c.blocks[o.hash]
		if b.Prev == v.c.last().Hash {
			return []Block{b}
		}
		if i := slices.IndexFunc(v.c.own, func(x Block) bool { return x.Hash == b.Prev }); i >= 0 {
			return append(slices.Clone(v.c.own[:i+1]), b)
		}
	}
	return v.c.own
}

// position is where hash sits in the view made of the followed chain and
// suffix, counting from 0, or -1.
func (v *views) position(suffix []Block, hash [32]byte) int {
	if i, ok := v.c.at[hash]; ok {
		return i
	}
	if i := slices.IndexFunc(suffix, func(b Block) bool { return b.Hash == hash }); i >= 0 {
		return len(v.c.followed) + i
	}
	return -1
}

func (v *views) viewAt(suffix []Block, i int) Block {
	if i < len(v.c.followed) {
		return v.c.followed[i].Block
	}
	return suffix[i-len(v.c.followed)]
}

func (v *views) isOwn(hash [32]byte) bool {
	return slices.ContainsFunc(v.c.own, func(b Block) bool { return b.Hash == hash })
}

// ancestor is the newest ancestor of hash in the view, or the origin.
func (v *views) ancestor(suffix []Block, hash [32]byte) Cursor {
	for {
		b, ok := v.c.blocks[hash]
		if !ok {
			return Cursor{Origin: true}
		}
		if v.position(suffix, b.Prev) >= 0 {
			return Cursor{Hash: b.Prev}
		}
		hash = b.Prev
	}
}

// next is the reply to peer's request with its cursor at cur. Sending an
// offered block marks it served.
func (v *views) next(peer PeerID, cur Cursor) Step {
	suffix := v.suffix(peer, cur)
	pos := -1
	if !cur.Origin {
		if pos = v.position(suffix, cur.Hash); pos < 0 {
			return Step{Move: Backward, To: v.ancestor(suffix, cur.Hash)}
		}
	}
	if pos+1 >= len(v.c.followed)+len(suffix) {
		return Step{Move: Wait}
	}
	b := v.viewAt(suffix, pos+1)
	check.Post(cur.Origin || b.Hash != cur.Hash, "forger.Chain.Next: a forward step moves the cursor")
	if _, followed := v.c.at[b.Hash]; !followed && !v.isOwn(b.Hash) {
		if v.served[peer] == nil {
			v.served[peer] = map[[32]byte]bool{}
		}
		v.served[peer][b.Hash] = true
	}
	return Step{Move: Forward, Block: b}
}

// intersect is the first of points in peer's view, for a peer whose cursor
// is not yet set.
func (v *views) intersect(peer PeerID, points []Cursor) (Cursor, bool) {
	suffix := v.suffix(peer, Cursor{Origin: true})
	for _, pt := range points {
		if pt.Origin || v.position(suffix, pt.Hash) >= 0 {
			return pt, true
		}
	}
	return Cursor{}, false
}
