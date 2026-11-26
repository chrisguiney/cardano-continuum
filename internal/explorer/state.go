package explorer

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// state is the index's data. Index holds its lock around every call, so
// nothing here locks.
type state struct {
	cfg Config
	// metas is the chain, oldest first; the other maps hold indexes into it.
	metas    []*meta
	byHash   map[[32]byte]int
	byHeight map[uint64]int
	byIssuer map[[28]byte][]int
	txs      map[[32]byte]txLoc
	// oldest is the first block that still has its full contents.
	oldest int
	utxos  *utxoSet
	hist   *history
	eps    *epochs
}

// txLoc is where a transaction sits: its block's position and its index.
type txLoc struct {
	block, idx int
}

func newState(cfg Config) *state {
	return &state{
		cfg: cfg, byHash: map[[32]byte]int{}, byHeight: map[uint64]int{}, byIssuer: map[[28]byte][]int{},
		txs: map[[32]byte]txLoc{}, utxos: newUtxoSet(), hist: newHistory(), eps: newEpochs(),
	}
}

// add appends a decoded block; see Index.Add.
func (st *state) add(blk *conway.ConwayBlock, raw []byte) error {
	hash := [32]byte(blk.Hash())
	if n := len(st.metas); n > 0 {
		tip := st.metas[n-1]
		if tip.hash == hash {
			return nil
		}
		if tip.hash != [32]byte(blk.PrevHash()) {
			return fmt.Errorf("%w: block %d at slot %d", ErrNotConnected, blk.BlockNumber(), blk.SlotNumber())
		}
	}
	m := &meta{
		hash: hash, prev: [32]byte(blk.PrevHash()), height: blk.BlockNumber(), slot: blk.SlotNumber(),
		issuer: [28]byte(blk.IssuerVkey().Hash()), size: uint64(len(raw)),
		full: &fullBlock{raw: append([]byte(nil), raw...), inputs: map[ref][]byte{}, born: map[ref]struct{}{}},
	}
	a := applier{st: st, m: m}
	for _, tx := range blk.Transactions() {
		a.tx(tx)
	}
	pos := len(st.metas)
	st.metas = append(st.metas, m)
	st.byHash[hash] = pos
	st.byHeight[m.height] = pos
	st.byIssuer[m.issuer] = append(st.byIssuer[m.issuer], pos)
	for i, t := range m.txs {
		st.txs[t.id] = txLoc{block: pos, idx: i}
	}
	st.eps.add(st.epochOf(m.slot), m)
	st.evict()
	return nil
}

func (st *state) epochOf(slot uint64) uint64 {
	if st.cfg.Slots == nil {
		return 0
	}
	return st.cfg.Slots.EpochOfSlot(slot)
}

func (st *state) evict() {
	for len(st.metas)-st.oldest > st.cfg.Keep {
		st.metas[st.oldest].full = nil
		st.oldest++
	}
}

// rollback removes every block above the one at (slot, hash); see
// Index.Rollback.
func (st *state) rollback(slot uint64, hash string) error {
	h, err := parseHash(hash)
	if err != nil {
		return err
	}
	pos, ok := st.byHash[h]
	if !ok || st.metas[pos].slot != slot {
		return fmt.Errorf("%w: rollback point %s", ErrNotFound, hash)
	}
	if pos+1 < st.oldest {
		return fmt.Errorf("%w: point at height %d, oldest retained %d", ErrTooDeep, st.metas[pos].height, st.metas[st.oldest].height)
	}
	for len(st.metas)-1 > pos {
		st.pop()
	}
	return nil
}

// pop removes the tip block.
func (st *state) pop() {
	n := len(st.metas) - 1
	m := st.metas[n]
	for r := range m.full.born {
		st.utxos.take(r)
	}
	for r, raw := range m.full.inputs {
		if _, own := m.full.born[r]; own {
			continue
		}
		if o, err := decodeOut(raw); err == nil {
			if a, aerr := o.Address().Bytes(); aerr == nil {
				st.utxos.put(r, outRec{cbor: raw, addr: string(a)})
			}
		}
	}
	for i := len(m.full.touched) - 1; i >= 0; i-- {
		st.hist.drop(m.full.touched[i].addr, m.full.touched[i].tx)
	}
	for _, t := range m.txs {
		delete(st.txs, t.id)
	}
	delete(st.byHash, m.hash)
	delete(st.byHeight, m.height)
	if l := st.byIssuer[m.issuer]; len(l) <= 1 {
		delete(st.byIssuer, m.issuer)
	} else {
		st.byIssuer[m.issuer] = l[:len(l)-1]
	}
	st.eps.sub(st.epochOf(m.slot), m)
	st.metas = st.metas[:n]
	st.oldest = min(st.oldest, n)
}

func (st *state) row(m *meta) BlockRow {
	r := BlockRow{
		Height: m.height, Slot: m.slot, Epoch: st.epochOf(m.slot), Hash: m.hashHex(), Prev: hex.EncodeToString(m.prev[:]),
		Issuer: hex.EncodeToString(m.issuer[:]), Txs: len(m.txs), Size: m.size, Fees: m.fees, ExMem: m.exMem, ExSteps: m.exSteps,
	}
	if st.cfg.Slots != nil {
		r.TimeMs = int64(st.cfg.Slots.SlotStart(m.slot))
	}
	r.Pool = st.pool(r.Issuer)
	return r
}

func (st *state) pool(id string) *PoolRef {
	if st.cfg.Pool == nil {
		return nil
	}
	if p, ok := st.cfg.Pool(id); ok {
		return &p
	}
	return nil
}

// locate finds a block position by hash or decimal height.
func (st *state) locate(key string) (int, error) {
	key = strings.TrimSpace(key)
	if len(key) == 64 {
		h, err := parseHash(key)
		if err != nil {
			return 0, err
		}
		if pos, ok := st.byHash[h]; ok {
			return pos, nil
		}
		return 0, fmt.Errorf("%w: block %s", ErrNotFound, key)
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: block %q is neither a 64-digit hash nor a height", ErrBadKey, key)
	}
	if pos, ok := st.byHeight[n]; ok {
		return pos, nil
	}
	return 0, fmt.Errorf("%w: height %d", ErrNotFound, n)
}

func parseHash(s string) ([32]byte, error) {
	var h [32]byte
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(h) {
		return h, fmt.Errorf("%w: %q is not a 64-digit hash", ErrBadKey, s)
	}
	copy(h[:], b)
	return h, nil
}

func parsePool(s string) ([28]byte, error) {
	var h [28]byte
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(h) {
		return h, fmt.Errorf("%w: %q is not a 56-digit pool id", ErrBadKey, s)
	}
	copy(h[:], b)
	return h, nil
}
