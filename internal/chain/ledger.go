package chain

import (
	"bytes"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// StakeEntry is one stake credential's share of a snapshot: the pool it was
// delegated to and the lovelace behind it.
type StakeEntry struct {
	Cred  common.Blake2b224
	Pool  common.Blake2b224
	Stake uint64
}

// Snapshot is a stake distribution as an epoch transition took it: only
// registered, delegated credentials with stake, sorted by credential. The
// view builds it from the outputs the book names, so it covers the engine's
// own population, not the whole chain, and counts no reward balances.
type Snapshot []StakeEntry

// Total is the lovelace in the snapshot.
func (s Snapshot) Total() uint64 {
	var sum uint64
	for _, e := range s {
		sum += e.Stake
	}
	return sum
}

// ByPool sums the snapshot per pool.
func (s Snapshot) ByPool() map[common.Blake2b224]uint64 {
	out := map[common.Blake2b224]uint64{}
	for _, e := range s {
		out[e.Pool] += e.Stake
	}
	return out
}

// epochs is what the epoch transition owns. label is the epoch of the
// newest block applied; frozen means an epoch passed without a block, after
// which the ledger never runs another transition (models/specs/stake-floor
// README, "Freezing"). Snapshots are immutable once taken, so an undo record
// holds the old slices without copying them.
//
// The transition keeps the order models/specs/stake-floor fixes: "The
// transition runs at the first block of an epoch: it rotates the snapshots
// (`set := mark`, `mark := deleg`), then retires the pools whose epoch has
// come and undelegates their wallets (SNAP before POOLREAP, so the retired
// pool's stake is in the new `mark`)."
type epochs struct {
	label     uint64
	frozen    bool
	mark, set Snapshot
}

// next runs the transition a block of epoch ep triggers, if any, before
// the block's transactions; first means the block is the chain's first.
// The first block of an epoch rotates the snapshots (set takes mark, mark
// takes the delegation as it stands) and then retires the pools whose
// epoch has come, undelegating their wallets. SNAP runs before POOLREAP,
// so a retiring pool's stake is still in the new mark. The shape follows
// cardano-ledger's NEWEPOCH rule (Shelley/Rules/NewEpoch.hs); the leader
// schedule of epoch e is set, not go, as in dingo
// consensus/praos/snapshot.go.
func (e *epochs) next(ep uint64, first bool, src *utxos, a *accounts, r *registry, u *undo) {
	if e.frozen || ep == e.label {
		return
	}
	old := *e
	switch {
	case first:
		// The first block of a chain has no earlier epoch to close.
		e.label = ep
	case ep > e.label+1:
		e.frozen = true
	default:
		e.set, e.mark = e.mark, a.distribution(src.credStake())
		e.label = ep
		r.reap(ep, a, u)
	}
	u.epoch = &old
}

func (e *epochs) restore(u undo) {
	if u.epoch != nil {
		*e = *u.epoch
	}
}

// poolStake is the lovelace delegated to each pool in d, and the total.
func poolStake(d Snapshot) (map[common.Blake2b224]uint64, uint64) {
	per := d.ByPool()
	var total uint64
	for _, n := range per {
		total += n
	}
	return per, total
}

func compareHash(a, b common.Blake2b224) int { return bytes.Compare(a[:], b[:]) }
