package feeds

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Dropper is a node that reports transactions leaving its mempool without
// a block (the fake chain's evictions). Live has no mempool monitor, so it
// is optional.
type Dropper interface {
	// Dropped returns the transactions dropped since the last call.
	Dropped() []wire.TxID
}

// Attempts finds the intent a transaction is an attempt of.
type Attempts interface {
	Of(tx wire.TxID) (uint64, bool)
}

// Drops feeds a drop notice for every dropped transaction that is one of
// the engine's attempts.
type Drops struct {
	node Dropper
	of   Attempts
}

// NewDrops returns the drop feed; a nil node drops nothing.
func NewDrops(n Dropper, of Attempts) Drops { return Drops{node: n, of: of} }

// Poll feeds the drops since the last call, then the submissions they
// decide.
func (d Drops) Poll(ctx context.Context, l Loop) error {
	if d.node == nil {
		return nil
	}
	for _, tx := range d.node.Dropped() {
		id, ok := d.of.Of(tx)
		if !ok {
			continue
		}
		body, err := eventlog.Encode(wire.SubmitResult{Intent: id, Verdict: wire.VerdictDropped, Tx: &tx})
		if err != nil {
			return err
		}
		rec := eventlog.Record{At: l.Now(), Kind: eventlog.KindSubmitResult, Body: body}
		if err := l.Feed(ctx, rec); err != nil {
			return err
		}
	}
	return l.Effects(ctx)
}
