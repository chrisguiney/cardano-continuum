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

// epochState is what the epoch transition owns. label is the epoch of the
// newest block applied; frozen means an epoch passed without a block, after
// which the ledger never runs another transition (models/specs/stake-floor
// README, "Freezing"). Snapshots are immutable once taken, so an undo record
// holds the old slices without copying them.
type epochState struct {
	label     uint64
	frozen    bool
	mark, set Snapshot
}

// newEpoch runs the transition b triggers, if any, before b's
// transactions: the first block of an epoch rotates the snapshots (set takes
// mark, mark takes the delegation as it stands) and then retires the pools
// whose epoch has come, undelegating their wallets. SNAP runs before
// POOLREAP, so a retiring pool's stake is still in the new mark. The shape
// follows cardano-ledger's NEWEPOCH rule (Shelley/Rules/NewEpoch.hs); the
// leader schedule of epoch e is set, not go, as in dingo
// consensus/praos/snapshot.go.
func (v *View) newEpoch(b Block, u *undo) {
	ep := v.cfg.Slots.EpochOfSlot(b.Point.Slot)
	if v.ep.frozen || ep == v.ep.label {
		return
	}
	old := v.ep
	switch {
	case v.tip.IsOrigin():
		// The first block of a chain has no earlier epoch to close.
		v.ep.label = ep
	case ep > v.ep.label+1:
		v.ep.frozen = true
	default:
		v.ep.set, v.ep.mark = v.ep.mark, v.distribution(v.credStake())
		v.ep.label = ep
		v.reap(ep, &v.accounts, u)
	}
	u.epoch = &old
}

func (v *View) restoreEpoch(u undo) {
	if u.epoch != nil {
		v.ep = *u.epoch
	}
}

// poolStake is the lovelace delegated to each pool now, and the total.
func (v *View) poolStake() (map[common.Blake2b224]uint64, uint64) {
	per := v.distribution(v.credStake()).ByPool()
	var total uint64
	for _, n := range per {
		total += n
	}
	return per, total
}

func compareHash(a, b common.Blake2b224) int { return bytes.Compare(a[:], b[:]) }
