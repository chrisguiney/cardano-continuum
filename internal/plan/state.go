package plan

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// state is the ledger's content. Its methods are pure: no I/O, and the
// Ledger applies them to a clone so a failed commit discards them. state
// holds the contract's rules; its parts hold the data each rule reads.
type state struct {
	view view
	out  outputs
	att  attempts
	// paid are intents with a settled attempt. They accept no attempt.
	paid map[IntentID]struct{}
}

func newState(k uint64) *state {
	return &state{view: view{k: k}, out: newOutputs(), att: newAttempts(), paid: map[IntentID]struct{}{}}
}

// clone copies everything a step may change.
func (s *state) clone() *state {
	return &state{view: s.view.clone(), out: s.out.clone(), att: s.att.clone(), paid: maps.Clone(s.paid)}
}

func compareRefs(a, b Ref) int {
	return cmp.Or(slices.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}

// available is Offer in the model, for one actor.
func (s *state) available(a population.ActorID, id IntentID, o Opts) []Utxo {
	spent := s.view.spentAbove()
	var out []Utxo
	for r := range s.out.byOwner[a] {
		if s.att.offerable(r, id, spent) {
			out = append(out, s.out.utxos[r])
		}
	}
	out = s.att.appendChange(out, s.att.recsBy[a], id, o, spent)
	slices.SortFunc(out, func(x, y Utxo) int { return compareRefs(x.Ref, y.Ref) })
	check.PostFn(func() bool { return equalUtxos(out, s.availableScan(a, id, o)) },
		"plan: owner index agrees with a full scan")
	return out
}

// availableScan is available without the indexes or the spent cache: the
// reference that check.PostFn holds them to.
func (s *state) availableScan(a population.ActorID, id IntentID, o Opts) []Utxo {
	spent := map[Ref]struct{}{}
	for _, b := range s.view.blocks {
		for _, tx := range b.txs {
			for _, r := range tx.spends {
				spent[r] = struct{}{}
			}
		}
	}
	var out []Utxo
	for r, u := range s.out.utxos {
		if u.Owner == a && s.att.offerable(r, id, spent) {
			out = append(out, u)
		}
	}
	scan := map[TxID]*record{}
	for tx, rec := range s.att.recs {
		if rec.Actor == a {
			scan[tx] = rec
		}
	}
	out = s.att.appendChange(out, scan, id, o, spent)
	slices.SortFunc(out, func(x, y Utxo) int { return compareRefs(x.Ref, y.Ref) })
	return out
}

func equalUtxos(a, b []Utxo) bool { return slices.Equal(a, b) }

// indexConsistent reports whether the owner and actor indexes hold exactly
// what the outputs and attempts imply.
func (s *state) indexConsistent() error {
	if err := s.out.indexConsistent(); err != nil {
		return err
	}
	return s.att.indexConsistent()
}

// reserve checks attempt a of intent id against the contract's rule 3 and
// records it (Build in the model).
func (s *state) reserve(id IntentID, a Attempt) error {
	if err := s.att.wellFormed(a); err != nil {
		return err
	}
	if _, ok := s.paid[id]; ok {
		return fmt.Errorf("%w: intent %d", ErrPaid, id)
	}
	offered := s.available(a.Actor, id, Opts{Chain: true})
	for _, r := range a.Inputs {
		if !slices.ContainsFunc(offered, func(u Utxo) bool { return u.Ref == r }) {
			return fmt.Errorf("%w: %x#%d for intent %d", ErrNotAvailable, r.Tx[:4], r.Index, id)
		}
	}
	mine := s.att.ancestry(a.Inputs)
	if !coherent(a.Inputs, mine) {
		return fmt.Errorf("%w: intent %d", ErrContradictory, id)
	}
	for _, p := range s.att.sorted() {
		if p.Intent == id && !s.att.excludes(a.Inputs, mine, p) {
			return fmt.Errorf("%w: intent %d, earlier attempt %x", ErrNotExclusive, id, p.Tx[:4])
		}
	}
	s.att.add(id, a)
	return nil
}

// doomed reports whether rec can never land, judged from the settled part
// of the view only (Doomed in the model). An input is either the change of
// an unfinished attempt, judged when that attempt is, or must still be a
// settled output: a missing one was spent by another settled transaction,
// or built on an attempt already written off.
func (s *state) doomed(rec *record) bool {
	if rec.TTL != 0 && s.view.settled.Slot >= rec.TTL {
		return true
	}
	for _, r := range rec.Inputs {
		if _, ok := s.att.parent(r); ok {
			continue
		}
		if _, ok := s.out.utxos[r]; !ok {
			return true
		}
	}
	return false
}

// reap writes off every doomed attempt (the dead branch of Reap in the
// model). Seq order puts parents first, so a child sees its parent already
// gone and dies in the same pass.
func (s *state) reap() []TxID {
	var dead []TxID
	for _, rec := range s.att.sorted() {
		if s.doomed(rec) {
			s.att.finish(rec)
			dead = append(dead, rec.Tx)
		}
	}
	return dead
}

// settle folds the oldest block above the settled point into the settled
// outputs and returns the attempts it held (the settled branch of Reap in
// the model).
func (s *state) settle() []TxID {
	b := s.view.pop()
	var done []TxID
	for _, tx := range b.txs {
		for _, r := range tx.spends {
			s.out.drop(r)
		}
		for _, u := range tx.creates {
			s.out.put(u)
		}
		if rec, ok := s.att.recs[tx.id]; ok {
			s.paid[rec.Intent] = struct{}{}
			s.att.finish(rec)
			done = append(done, tx.id)
		}
	}
	return done
}

// forward is Process for a roll-forward: extend the view, settle every
// block now K deep, then write off what the new settled point dooms.
func (s *state) forward(b chain.Block, o Owners) (Result, error) {
	if err := s.view.extend(b, o); err != nil {
		return Result{}, err
	}
	var res Result
	for s.view.deep() {
		res.Settled = append(res.Settled, s.settle()...)
	}
	res.Dead = s.reap()
	return res, nil
}

// rollback is Process for a roll-backward: it cuts the view back to p and
// changes no reservation. A rolled back attempt may be included again, and
// the settled point stays, so nothing new is settled or doomed.
func (s *state) rollback(p chain.Point) error {
	return s.view.rollback(p)
}

// refused is SubmitFails in the model: tx and everything built on it are
// written off.
func (s *state) refused(tx TxID) ([]TxID, error) {
	rec, ok := s.att.recs[tx]
	if !ok {
		return nil, fmt.Errorf("%w: %x", ErrUnknownTx, tx[:4])
	}
	s.att.finish(rec)
	return append([]TxID{tx}, s.reap()...), nil
}

// consistent checks the model's store invariants. Callers run it in debug
// builds and tests.
func (s *state) consistent() error {
	if err := s.att.consistent(); err != nil {
		return err
	}
	for _, rec := range s.att.recs {
		// InputsTracked: an input is an unfinished attempt's change or a
		// settled output, never a rolled-back or written-off one.
		for _, r := range rec.Inputs {
			_, isChange := s.att.parent(r)
			if _, isSettled := s.out.utxos[r]; !isChange && !isSettled {
				return fmt.Errorf("unfinished %x spends untracked %x#%d", rec.Tx[:4], r.Tx[:4], r.Index)
			}
		}
		// The doomed half of NoLeak: reap left nothing doomed unfinished.
		if s.doomed(rec) {
			return fmt.Errorf("unfinished %x is doomed", rec.Tx[:4])
		}
	}
	return s.view.consistent()
}
