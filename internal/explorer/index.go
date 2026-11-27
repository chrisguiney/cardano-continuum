package explorer

import (
	"fmt"
	"sync"
)

// maxScan bounds the outputs one address request decodes.
const maxScan = 20_000

// Index is the explorer's view of the chain. It is safe for concurrent use.
type Index struct {
	mu sync.Mutex // protects st
	st *state
}

// New returns an empty index.
func New(cfg Config) *Index {
	cfg.Keep = max(cfg.Keep, int(cfg.K)+1, 1)
	return &Index{st: newState(cfg)}
}

// FullBlocks is how many blocks still hold their full contents.
func (x *Index) FullBlocks() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.st.metas) - x.st.oldest
}

// Tip is the number of blocks indexed and the tip's height, 0 when empty.
func (x *Index) Tip() (blocks int, height uint64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if n := len(x.st.metas); n > 0 {
		return n, x.st.metas[n-1].height
	}
	return 0, 0
}

// Add appends the block with this CBOR. A block that is already the tip is
// ignored, so replaying a log is harmless; one that does not extend the tip
// is ErrNotConnected.
func (x *Index) Add(raw []byte) error {
	blk, err := decodeBlock(raw)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.st.add(blk, raw)
}

// Rollback removes every block above the one at (slot, hash). It is exact
// within the retained blocks and leaves the index unchanged when it fails:
// ErrNotFound for a point not on the chain, ErrTooDeep below the retained
// blocks.
func (x *Index) Rollback(slot uint64, hash string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.st.rollback(slot, hash)
}

// Blocks lists blocks newest first.
func (x *Index) Blocks(q BlockQuery) BlockPage {
	x.mu.Lock()
	defer x.mu.Unlock()
	return blocksOf(x.st, q)
}

// Block returns the block with this hash (64 hex digits) or height.
func (x *Index) Block(key string) (BlockView, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return blockOf(x.st, key)
}

// Tx returns a transaction. When its block is evicted only the header
// fields are known.
func (x *Index) Tx(hash string) (TxView, error) {
	h, err := parseHash(hash)
	if err != nil {
		return TxView{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return txOf(x.st, h, hash)
}

// Address returns an address's unspent outputs, balance and history. p
// pages the outputs and the history alike.
func (x *Index) Address(s string, p Page) (AddressView, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return addressOf(x.st, s, p)
}

// Epoch summarises epoch n.
func (x *Index) Epoch(n uint64) (EpochView, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var known []string
	if x.st.cfg.Known != nil {
		known = x.st.cfg.Known()
	}
	v, ok := x.st.eps.view(n, known, x.st.pool)
	if !ok {
		return EpochView{}, fmt.Errorf("%w: epoch %d", ErrNotFound, n)
	}
	return v, nil
}

// Find resolves a key to a block, transaction or address the index holds.
// Pools and actors are not the index's to resolve; the caller handles
// KindPool and KindActor.
func (x *Index) Find(q string) (Hit, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return find(x.st, q)
}

// GovAction returns the governance action proposed at index idx of
// transaction tx, or ErrNotFound. Unlike a transaction, it is available after
// its block is evicted.
func (x *Index) GovAction(tx string, idx uint32) (GovAction, error) {
	h, err := parseHash(tx)
	if err != nil {
		return GovAction{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	g, ok := x.st.gov.acts[govKey{h, idx}]
	if !ok {
		return GovAction{}, fmt.Errorf("%w: governance action %s#%d", ErrNotFound, tx, idx)
	}
	return g, nil
}

// GovVotes lists the anchors of the votes cast on an action, oldest first.
func (x *Index) GovVotes(tx string, idx uint32) []VoteAnchor {
	h, err := parseHash(tx)
	if err != nil {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]VoteAnchor(nil), x.st.gov.notes[govKey{h, idx}]...)
}
