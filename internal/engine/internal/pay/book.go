package pay

import (
	"iter"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Submission is a payment waiting for the node's answer. Twice asks for the
// bytes to be sent a second time (a DoubleSubmit mistake).
type Submission struct {
	Intent uint64
	Actor  uint64
	ID     wire.TxID
	Tx     []byte
	Twice  bool
	// Rivals are sent right after Tx: a double-spend race.
	Rivals []wire.Rival
}

// Book is the unfinished intents: by ID, by unfinished attempt, and the
// ones waiting for a node's answer.
type Book struct {
	pending  map[uint64]*wire.Pending
	byTx     map[wire.TxID]uint64
	awaiting []uint64 // ascending intent IDs without a node answer
}

func newBook() Book {
	return Book{pending: map[uint64]*wire.Pending{}, byTx: map[wire.TxID]uint64{}}
}

// Add records an unfinished intent and its attempts.
func (b *Book) Add(p *wire.Pending) {
	b.pending[p.ID] = p
	for _, tx := range p.Txs {
		b.byTx[tx] = p.ID
	}
	if p.Awaiting {
		b.awaiting = append(b.awaiting, p.ID)
	}
}

// drop forgets an intent and its attempts.
func (b *Book) drop(id uint64) {
	if p, ok := b.pending[id]; ok {
		for _, tx := range p.Txs {
			delete(b.byTx, tx)
		}
		delete(b.pending, id)
	}
	b.Unawait(id)
}

// Unawait takes an intent off the queue of submissions to make.
func (b *Book) Unawait(id uint64) {
	if i, ok := slices.BinarySearch(b.awaiting, id); ok {
		b.awaiting = slices.Delete(b.awaiting, i, i+1)
	}
}

// Get is the unfinished intent id, nil when there is none.
func (b *Book) Get(id uint64) *wire.Pending { return b.pending[id] }

// Of is the intent an unfinished attempt belongs to.
func (b *Book) Of(tx wire.TxID) (uint64, bool) {
	id, ok := b.byTx[tx]
	return id, ok
}

// Len is the number of unfinished intents.
func (b *Book) Len() int { return len(b.pending) }

// IDs are the unfinished intents, ascending.
func (b *Book) IDs() []uint64 {
	ids := make([]uint64, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// All yields the unfinished intents in no particular order; callers that
// emit or draw in the loop use IDs instead.
func (b *Book) All() iter.Seq[*wire.Pending] { return maps.Values(b.pending) }

// Has reports whether an unfinished intent of the actor has kind.
func (b *Book) Has(id population.ActorID, kind string) bool {
	for _, q := range b.pending {
		if q.Actor == uint64(id) && q.Kind == kind {
			return true
		}
	}
	return false
}

// InFlight returns the payments decided but not yet answered by the node,
// oldest first, leaving out those still backing off at now; a front-run
// goes ahead of all of them.
func (b *Book) InFlight(now sched.Time) []Submission {
	out := make([]Submission, 0, len(b.awaiting))
	for _, jump := range []bool{true, false} {
		for _, id := range b.awaiting {
			p := b.pending[id]
			if p.NotBefore > now || p.Jump != jump {
				continue
			}
			out = append(out, Submission{Intent: id, Actor: p.Actor, ID: p.Tx, Tx: p.Cbor, Twice: p.Carried.Has(mistake.DoubleSubmit), Rivals: p.Rivals})
		}
	}
	return out
}

// await marks the payment's current attempt as waiting for a send.
func (b *Book) await(p *wire.Pending) {
	p.Awaiting = true
	if _, ok := slices.BinarySearch(b.awaiting, p.ID); !ok {
		b.awaiting = append(b.awaiting, p.ID)
		slices.Sort(b.awaiting)
	}
}

// forget removes a finished attempt from its payment's list and the index.
func (b *Book) forget(tx wire.TxID) {
	id, ok := b.byTx[tx]
	if !ok {
		return
	}
	delete(b.byTx, tx)
	if p, ok := b.pending[id]; ok {
		p.Txs = slices.DeleteFunc(p.Txs, func(t wire.TxID) bool { return t == tx })
	}
}
