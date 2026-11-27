package store

import (
	"bytes"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Blob is an opaque, versioned value owned by another component. The store
// never interprets Data; the owner reads Version to migrate it.
type Blob struct {
	Version uint32
	Data    []byte
}

// Checkpoint is everything recovery loads before replaying the log tail.
type Checkpoint struct {
	// Seq is the last log seq the checkpoint reflects; 0 means none.
	Seq uint64
	// Points are the intersection points, oldest first. They must reach
	// down to Confirmed.
	Points []eventlog.ChainPoint
	// Confirmed is the K-deep point durable actor state comes from.
	Confirmed eventlog.ChainPoint
	// Actors is the engine snapshot, in the engine's canonical encoding.
	Actors []byte
	// ActorRows is the read model the UI pages through.
	ActorRows []ActorRow
	// Intents and Reservations are opaque to the store. Reservations holds
	// the planner's ledger (internal/plan, version plan.BlobVersion); it is
	// saved in the same transaction as Confirmed, so a release on disk is
	// never ahead of the settled point it came from.
	Intents      Blob
	Reservations Blob
}

func containsPoint(pts []eventlog.ChainPoint, p eventlog.ChainPoint) bool {
	for _, q := range pts {
		if q.Slot == p.Slot && bytes.Equal(q.Hash, p.Hash) {
			return true
		}
	}
	return false
}

// nonNil keeps NULL out of NOT NULL blob columns.
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Recovery is what the engine needs to resume.
type Recovery struct {
	// Checkpoint is the stored checkpoint, zero if none was saved.
	Checkpoint Checkpoint
	// Points are the intersection points after folding in the tail, oldest
	// first; they are what the restart's FindIntersect offers.
	Points []eventlog.ChainPoint
	// Tail is every log record after Checkpoint.Seq, in order. Feed it to
	// the engine built from Checkpoint.Actors.
	Tail []eventlog.Record
}
