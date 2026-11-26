package plan

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Ledger is the reservation store. It is not safe for concurrent use: the
// engine loop is its only caller.
type Ledger struct {
	cfg Config
	st  *state
	// hints are drop notices. They live in memory only and change nothing
	// but Dropped.
	hints map[TxID]struct{}
	// inPlace is true when the Committer discards: a step then edits the
	// current state, since no commit can fail and force it back.
	inPlace bool
}

func newLedger(cfg Config, s *state) *Ledger {
	d, ok := cfg.Store.(Discarder)
	return &Ledger{cfg: cfg, st: s, hints: map[TxID]struct{}{}, inPlace: ok && d.Discards()}
}

// next is the state a step applies itself to: the current one when a
// failed commit is impossible, else a copy to adopt only once committed.
func (l *Ledger) next() *state {
	if l.inPlace {
		return l.st
	}
	return l.st.clone()
}

// New returns a ledger at the origin holding genesis as settled outputs.
func New(cfg Config, genesis []Utxo) (*Ledger, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	s := newState(cfg.K)
	for _, u := range genesis {
		if _, dup := s.utxos[u.Ref]; dup {
			return nil, fmt.Errorf("plan: genesis output %x#%d twice", u.Ref.Tx[:4], u.Ref.Index)
		}
		s.putUtxo(u)
	}
	return newLedger(cfg, s), nil
}

// Open returns the ledger committed as b. Its view resumes at the settled
// point; chain-sync must intersect at ResumePoint. Every unfinished attempt
// may have been sent before the restart.
func Open(cfg Config, b store.Blob) (*Ledger, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	s, err := decode(cfg.K, b)
	if err != nil {
		return nil, err
	}
	return newLedger(cfg, s), nil
}

// commit makes next durable and adopts it. On failure the ledger keeps its
// current state, so nothing a caller acts on is lost to a crash.
func (l *Ledger) commit(ctx context.Context, next *state) error {
	if check.Slow {
		if err := next.consistent(); err != nil {
			check.Post(false, "plan: "+err.Error())
		}
	}
	if l.inPlace {
		return nil
	}
	b, err := encode(next)
	if err != nil {
		return err
	}
	if err := l.cfg.Store.Commit(ctx, b); err != nil {
		return fmt.Errorf("plan: commit: %w", err)
	}
	l.st = next
	return nil
}

// Available is what Reserve accepts for intent id of actor a, sorted by Ref:
// a's outputs unspent at the settled point and not spent above it, and with
// o.Chain the change of a's unfinished attempts not spent in the view; in
// either case free or already held by id. Outputs paid to a by blocks above
// the settled point are offered once they settle.
func (l *Ledger) Available(a population.ActorID, id IntentID, o Opts) []Utxo {
	return l.st.available(a, id, o)
}

// Reserve records attempt a of intent id and reserves its inputs for id.
// It returns only after the record and the reservations are durable, so
// the transaction may be sent once it returns nil. It refuses an input
// outside Available, an intent already paid, an attempt that could land
// beside an unfinished attempt of the same intent, and inputs that
// contradict each other.
func (l *Ledger) Reserve(ctx context.Context, id IntentID, a Attempt) error {
	next := l.next()
	if err := next.reserve(id, a); err != nil {
		return err
	}
	return l.commit(ctx, next)
}

// Advance applies one chain-sync event (a block, a rollback or an
// intersection answer) and returns what it finished. It is the only place
// the chain releases a reservation. On error nothing changes and the event
// must be delivered again.
func (l *Ledger) Advance(ctx context.Context, ev chain.Event) (Result, error) {
	was := l.st.settled
	next := l.next()
	var res Result
	switch ev.Kind {
	case chain.Forward:
		r, err := next.forward(ev.Block, l.cfg.Owners)
		if err != nil {
			return Result{}, err
		}
		res = r
	case chain.Backward, chain.Intersected:
		if err := next.rollback(ev.Point); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, fmt.Errorf("plan: unknown chain event %d", ev.Kind)
	}
	if next.settled == was && len(res.Dead) == 0 {
		// Only the volatile window moved, and it is not stored.
		l.st = next
		return res, nil
	}
	if err := l.commit(ctx, next); err != nil {
		return Result{}, err
	}
	l.forget(res.Settled)
	l.forget(res.Dead)
	return res, nil
}

// Refused writes off an attempt a node refused before it reached any node,
// with everything built on it, and returns them all. Call it only for the
// first submission of a transaction: a rejection after an earlier send that
// may have arrived (a timeout, a broken connection, a previous process)
// proves nothing, since the inputs may be spent by the attempt itself.
func (l *Ledger) Refused(ctx context.Context, tx TxID) ([]TxID, error) {
	next := l.next()
	dead, err := next.refused(tx)
	if err != nil {
		return nil, err
	}
	if err := l.commit(ctx, next); err != nil {
		return nil, err
	}
	l.forget(dead)
	return dead, nil
}

func (l *Ledger) forget(txs []TxID) {
	for _, tx := range txs {
		delete(l.hints, tx)
	}
}

// DropNotice records that a sent transaction is absent from the mempool.
// That is also what a block in flight looks like, so it releases nothing;
// it may prompt another attempt, which Reserve keeps exclusive.
func (l *Ledger) DropNotice(tx TxID) {
	if _, ok := l.st.recs[tx]; ok {
		l.hints[tx] = struct{}{}
	}
}

// Dropped reports whether tx had a drop notice and is still unfinished.
func (l *Ledger) Dropped(tx TxID) bool {
	_, ok := l.hints[tx]
	return ok
}

// ResumePoint is where chain-sync must intersect after a restart: the
// settled point, which never moves back.
func (l *Ledger) ResumePoint() chain.Point { return l.st.settled }

// Unfinished returns every unfinished attempt in the order they were
// reserved. After a restart each one must be treated as sent.
func (l *Ledger) Unfinished() []Pending {
	recs := l.st.sorted()
	out := make([]Pending, len(recs))
	for i, r := range recs {
		out[i] = Pending{Intent: r.Intent, Attempt: r.Attempt}
	}
	return out
}

// Holder returns the intent holding r, if any.
func (l *Ledger) Holder(r Ref) (IntentID, bool) {
	id, ok := l.st.held[r]
	return id, ok
}

// Settled returns the owned outputs unspent at the settled point, reserved
// or not, sorted by Ref. Blocks above the settled point are not reflected.
func (l *Ledger) Settled() []Utxo {
	return slices.SortedFunc(maps.Values(l.st.utxos), func(a, b Utxo) int { return compareRefs(a.Ref, b.Ref) })
}

// Blob returns the encoding a Commit of the current state would write. A
// caller that keeps durability elsewhere (the engine, whose event log
// replays every Reserve) stores it in its own checkpoint.
func (l *Ledger) Blob() (store.Blob, error) { return encode(l.st) }
