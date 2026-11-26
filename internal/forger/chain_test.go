package forger

import (
	"testing"
)

// mkBlock is a block with only what Chain reads.
func mkBlock(id byte, parent Tip, slot uint64) Block {
	b := Block{Slot: slot, Number: parent.Number + 1, Hash: [32]byte{id}, Prev: parent.Hash, VRFOutput: make([]byte, 64)}
	if parent.Origin {
		b.Number = 0
	}
	return b
}

func follow(t *testing.T, c *Chain, n int) []Block {
	t.Helper()
	var out []Block
	for i := 0; i < n; i++ {
		b := mkBlock(byte(len(c.followed)+10), c.Tip(), c.Tip().Slot+5)
		if err := c.Extend(b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// drain sends a peer everything its view holds and returns the hashes sent
// forward, with a rollback written as 0xff followed by its target.
func drain(c *Chain, peer PeerID, cur *Cursor) []byte {
	var out []byte
	for i := 0; i < 100; i++ {
		s := c.Next(peer, *cur)
		switch s.Move {
		case Wait:
			return out
		case Forward:
			*cur = Cursor{Hash: s.Block.Hash}
			out = append(out, s.Block.Hash[0])
		case Backward:
			*cur = s.To
			out = append(out, 0xff, s.To.Hash[0])
		}
	}
	return append(out, 0xee)
}

func TestChainServesFollowedAndOwn(t *testing.T) {
	c := NewChain(testParams(), [32]byte{1})
	bs := follow(t, c, 3)
	cur := Cursor{Origin: true}
	if got := drain(c, "a", &cur); string(got) != string([]byte{10, 11, 12}) {
		t.Fatalf("sent %v", got)
	}
	own := mkBlock(0x50, c.Tip(), 40)
	c.Add(own, Forge{Kind: Honest, Expect: Adopt})
	if c.Tip().Hash != own.Hash {
		t.Fatal("an honest block must become the selected tip")
	}
	if got := drain(c, "a", &cur); string(got) != string([]byte{0x50}) {
		t.Fatalf("sent %v, want the own block", got)
	}
	// The upstream picks another block: the peer is rolled back onto it.
	other := mkBlock(0x60, bs[2].Tip(), 41)
	if err := c.Extend(other); err != nil {
		t.Fatal(err)
	}
	if got := drain(c, "a", &cur); string(got) != string([]byte{0xff, 12, 0x60}) {
		t.Fatalf("sent %v, want a rollback to 12 and then 0x60", got)
	}
	if blocks, ok := c.Range(bs[1].Hash, own.Hash); !ok || len(blocks) != 3 || blocks[2].Hash != own.Hash {
		t.Fatalf("range to a dropped own block = %v, %v", blocks, ok)
	}
}

func TestChainAdoptedOwnBlockStays(t *testing.T) {
	c := NewChain(testParams(), [32]byte{1})
	follow(t, c, 2)
	own := mkBlock(0x50, c.Tip(), 40)
	own.Issuer, own.Counter = [32]byte{1}, 3
	c.Add(own, Forge{Kind: Honest, Expect: Adopt})
	cur := Cursor{Origin: true}
	drain(c, "a", &cur)
	if err := c.Extend(own); err != nil {
		t.Fatal(err)
	}
	if got := drain(c, "a", &cur); len(got) != 0 {
		t.Fatalf("adopting the own block moved the peer: %v", got)
	}
	if n, ok := c.Counter(); !ok || n != 3 {
		t.Fatalf("counter = %d, %v", n, ok)
	}
}

// TestChainOffersInvalidOnce: an invalid block reaches each peer once, and
// a peer that reconnects is not shown it again.
func TestChainOffersInvalidOnce(t *testing.T) {
	c := NewChain(testParams(), [32]byte{1})
	bs := follow(t, c, 2)
	bad := mkBlock(0x70, c.Tip(), 40)
	c.Add(bad, Forge{Kind: BodyHash, Expect: Reject})
	if c.Tip().Hash != bs[1].Hash {
		t.Fatal("an invalid block must not become the forger's own tip")
	}
	cur := Cursor{Origin: true}
	if got := drain(c, "a", &cur); string(got) != string([]byte{10, 11, 0x70}) {
		t.Fatalf("sent %v", got)
	}
	// The peer dropped the connection and intersects at its real tip.
	re, ok := c.Intersect("a", []Cursor{{Hash: bad.Hash}, {Hash: bs[1].Hash}})
	if !ok || re.Hash != bs[1].Hash {
		t.Fatalf("intersect = %v, %v", re, ok)
	}
	if got := drain(c, "a", &re); len(got) != 0 {
		t.Fatalf("the invalid block was sent again: %v", got)
	}
	other := Cursor{Hash: bs[1].Hash}
	if got := drain(c, "b", &other); string(got) != string([]byte{0x70}) {
		t.Fatalf("peer b was sent %v", got)
	}
}

func TestChainEquivocation(t *testing.T) {
	c := NewChain(testParams(), [32]byte{1})
	bs := follow(t, c, 2)
	a, b := mkBlock(0x80, c.Tip(), 40), mkBlock(0x81, c.Tip(), 40)
	c.Add(a, Forge{Kind: Equivocate, Expect: Adopt, SendTo: []PeerID{"a"}})
	c.Add(b, Forge{Kind: Equivocate, Expect: Adopt, SendTo: []PeerID{"b"}})
	ca, cb := Cursor{Hash: bs[1].Hash}, Cursor{Hash: bs[1].Hash}
	if got := drain(c, "a", &ca); string(got) != string([]byte{0x80}) {
		t.Fatalf("peer a was sent %v", got)
	}
	if got := drain(c, "b", &cb); string(got) != string([]byte{0x81}) {
		t.Fatalf("peer b was sent %v", got)
	}
	// The upstream adopted a: b's peer is rolled onto it.
	if err := c.Extend(a); err != nil {
		t.Fatal(err)
	}
	if got := drain(c, "a", &ca); len(got) != 0 {
		t.Fatalf("peer a moved: %v", got)
	}
	if got := drain(c, "b", &cb); string(got) != string([]byte{0xff, 11, 0x80}) {
		t.Fatalf("peer b was sent %v", got)
	}
}

func TestChainRollback(t *testing.T) {
	c := NewChain(testParams(), [32]byte{1})
	bs := follow(t, c, 4)
	before := c.followed[1].nonces
	if !c.Rollback(false, bs[1].Hash) {
		t.Fatal("rollback to a followed block failed")
	}
	if c.Tip().Hash != bs[1].Hash || c.Nonces() != before {
		t.Fatal("rollback did not restore the tip and the nonces")
	}
	if c.Rollback(false, bs[3].Hash) {
		t.Fatal("rollback to a block no longer followed succeeded")
	}
	cur := Cursor{Hash: bs[3].Hash}
	if got := drain(c, "a", &cur); string(got) != string([]byte{0xff, 11}) {
		t.Fatalf("sent %v", got)
	}
}

func TestNonces(t *testing.T) {
	p := testParams()
	g := GenesisNonces(p)
	if g.EpochNonce() != p.GenesisHash {
		t.Fatal("the first epoch's nonce is the genesis hash")
	}
	out := make([]byte, 64)
	// p has a 160-slot stability window in 200-slot epochs: a block at slot
	// 39 moves the candidate, one at slot 40 does not.
	early := g.Apply(p, 39, [32]byte{}, out)
	late := early.Apply(p, 40, [32]byte{9}, out)
	if early.candidate != early.evolving || late.candidate != early.candidate || late.evolving == early.evolving {
		t.Fatal("the candidate nonce must freeze 4k/f slots before the epoch ends")
	}
	// The first boundary has no previous-epoch block to mix in.
	next := late.Tick(p, 200)
	if next.EpochNonce() != late.candidate.hash || next.Epoch != 1 {
		t.Fatalf("epoch 1 nonce = %x, want the candidate %x", next.EpochNonce(), late.candidate.hash)
	}
	// The second mixes the previous-hash nonce held at the first.
	third := next.Tick(p, 400)
	if want := combine(next.candidate, hashNonce([32]byte{9})); third.epoch != want {
		t.Fatalf("epoch 2 nonce = %x, want %x", third.epoch.hash, want.hash)
	}
	if again := third.Tick(p, 401); again != third {
		t.Fatal("a tick inside the epoch changed the state")
	}
}
