package explorer

import (
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Errors returned by the index.
var (
	// ErrNotFound: nothing in the index has that key.
	ErrNotFound = errors.New("explorer: not found")
	// ErrBadKey: the key is not a well-formed hash, height or address.
	ErrBadKey = errors.New("explorer: malformed key")
	// ErrNotConnected: the block does not extend the index's tip.
	ErrNotConnected = errors.New("explorer: block does not extend the tip")
	// ErrTooDeep: a rollback below the retained full blocks.
	ErrTooDeep = errors.New("explorer: rollback below the retained blocks")
)

// Limits are the per-block bounds a block's use is shown against.
type Limits struct {
	MaxMem   uint64 `json:"max_mem"`
	MaxSteps uint64 `json:"max_steps"`
	MaxBytes uint64 `json:"max_bytes"`
}

// PoolRef names a continuum pool.
type PoolRef struct {
	Name string `json:"name"`
	Impl string `json:"impl"`
}

// Config bounds and describes an Index.
type Config struct {
	// Keep is how many of the newest blocks stay in full. It is raised to
	// K+1 so a rollback within K is always exact.
	Keep int
	K    uint64
	// Slots dates a slot's epoch; nil puts every block in epoch 0.
	Slots  *sched.Slots
	Limits Limits
	// Pool resolves a pool id (hex) to the continuum pool running it. Nil,
	// or an unknown id, leaves the block's issuer unresolved.
	Pool func(poolID string) (PoolRef, bool)
	// Known lists the pool ids (hex) of every continuum pool, so an epoch
	// summary shows a pool that forged nothing. Nil lists none.
	Known func() []string
}

// Page selects a window of a list. A zero Limit means DefaultLimit and a
// larger one is cut to MaxLimit.
type Page struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// Paging bounds.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

func (p Page) bounds(n int) (lo, hi int) {
	l := p.Limit
	if l <= 0 {
		l = DefaultLimit
	}
	l = min(l, MaxLimit)
	lo = min(max(p.Offset, 0), n)
	return lo, min(lo+l, n)
}

func (p Page) norm() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	p.Limit = min(p.Limit, MaxLimit)
	p.Offset = max(p.Offset, 0)
	return p
}

// BlockQuery lists blocks, newest first, optionally only those a pool
// (hex pool id) issued.
type BlockQuery struct {
	Page
	Pool string
}

// BlockRow is a block as the list shows it.
type BlockRow struct {
	Height uint64 `json:"height"`
	Slot   uint64 `json:"slot"`
	Epoch  uint64 `json:"epoch"`
	// TimeMs is the slot's start in simulated milliseconds; 0 without Slots.
	TimeMs int64  `json:"time_ms"`
	Hash   string `json:"hash"`
	Prev   string `json:"prev"`
	// Issuer is the pool id (hex) that forged the block; Pool resolves it to
	// a continuum pool when it is one.
	Issuer  string   `json:"issuer"`
	Pool    *PoolRef `json:"pool,omitempty"`
	Txs     int      `json:"txs"`
	Size    uint64   `json:"size"`
	Fees    uint64   `json:"fees"`
	ExMem   uint64   `json:"ex_mem"`
	ExSteps uint64   `json:"ex_steps"`
}

// BlockPage is one page of blocks.
type BlockPage struct {
	Total  int        `json:"total"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
	Rows   []BlockRow `json:"rows"`
	Limits Limits     `json:"limits"`
}

// Header is the block header's fields.
type Header struct {
	ProtoMajor uint64 `json:"proto_major"`
	ProtoMinor uint64 `json:"proto_minor"`
	BodyHash   string `json:"body_hash"`
	BodySize   uint64 `json:"body_size"`
	IssuerVkey string `json:"issuer_vkey"`
	VRFKey     string `json:"vrf_key"`
	// OpCert*: the operational certificate that authorised the KES key.
	OpCertHotVkey string `json:"opcert_hot_vkey"`
	OpCertSeq     uint64 `json:"opcert_seq"`
	KESPeriod     uint64 `json:"kes_period"`
}

// TxRow is a transaction as a block lists it.
type TxRow struct {
	Hash string `json:"hash"`
	Fee  uint64 `json:"fee"`
}

// BlockView is one block in full. Placement is set by the API layer from the
// pools' tips.
type BlockView struct {
	BlockRow
	Header  Header  `json:"header"`
	Txs     []TxRow `json:"txs"`
	Next    string  `json:"next,omitempty"`
	Evicted bool    `json:"evicted"`
}

// Kind is what a search key names.
type Kind string

// Search kinds.
const (
	KindUnknown Kind = "unknown"
	KindBlock   Kind = "block"
	KindTx      Kind = "tx"
	KindAddress Kind = "address"
	KindPool    Kind = "pool"
	KindActor   Kind = "actor"
	// KindNode is a node's name; the caller resolves it.
	KindNode Kind = "node"
	// KindHash64 is a 64-digit hex key: a block or a transaction hash.
	KindHash64 Kind = "hash64"
	// KindNumber is a bare number: a height, or failing that a slot.
	KindNumber Kind = "number"
	KindSlot   Kind = "slot"
)

// Hit is a search result the index owns: a block, transaction or address.
type Hit struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`
}

// EpochPool is one pool's blocks in an epoch against what an equal-stake
// share would have been.
type EpochPool struct {
	Pool     string   `json:"pool"`
	Resolved *PoolRef `json:"resolved,omitempty"`
	Blocks   uint64   `json:"blocks"`
	Expected float64  `json:"expected"`
}

// EpochView summarises one epoch.
type EpochView struct {
	Epoch  uint64      `json:"epoch"`
	Blocks uint64      `json:"blocks"`
	Txs    uint64      `json:"txs"`
	Fees   uint64      `json:"fees"`
	Size   uint64      `json:"size"`
	First  uint64      `json:"first_height"`
	Last   uint64      `json:"last_height"`
	Pools  []EpochPool `json:"pools"`
}
