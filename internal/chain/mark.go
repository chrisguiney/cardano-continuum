package chain

import (
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Mark is a Point with its block number: the height a node reports for the
// block. Two chains are compared at a common height, so a fork shows even
// when the branches' slots differ.
type Mark struct {
	Height uint64
	Point
	// Issuer is the hash of the cold key that signed the block, the PoolID
	// of the pool that forged it. It is zero where only a point is known,
	// such as the tip a node reports when a chain-sync starts.
	Issuer common.Blake2b224
}

// RecentRollbacks is how many rollbacks a RollbackLog keeps in detail.
const RecentRollbacks = 10

// Rollback is one time a node moved its selected chain back to a point
// behind its tip.
type Rollback struct {
	// From is the tip the node held when it rolled back.
	From Mark
	// To is the point it rolled back to.
	To Point
	// Blocks is how many blocks the node dropped: the blocks above To.
	Blocks uint64
	// Slots is From's slot minus To's, saturating at zero.
	Slots uint64
	// Beyond reports To older than the blocks the follower keeps, so Blocks
	// is a lower bound: the blocks it kept, all dropped.
	Beyond bool
}

// RollbackLog is what one node's follower saw of its rollbacks since it
// began following.
type RollbackLog struct {
	// Count is the number of rollbacks.
	Count uint64
	// Deepest is the rollback that dropped the most blocks; the first of
	// equals. Zero when Count is zero.
	Deepest Rollback
	// Last is the newest rollback.
	Last Rollback
	// Recent is the newest RecentRollbacks rollbacks, newest first.
	Recent []Rollback
}

// Add records r.
func (l *RollbackLog) Add(r Rollback) {
	l.Count++
	if l.Count == 1 || r.Blocks > l.Deepest.Blocks {
		l.Deepest = r
	}
	l.Last = r
	l.Recent = slices.Insert(l.Recent, 0, r)
	if len(l.Recent) > RecentRollbacks {
		l.Recent = l.Recent[:RecentRollbacks]
	}
}

// Clone returns a copy that shares no memory with l.
func (l RollbackLog) Clone() RollbackLog {
	l.Recent = slices.Clone(l.Recent)
	return l
}
