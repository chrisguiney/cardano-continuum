package plan

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// record is one unfinished attempt. Finished attempts are dropped: nothing
// can build on a dead one, and a settled one's change is a settled output.
type record struct {
	Intent IntentID
	// Seq orders attempts by Reserve. An attempt only spends the change of
	// attempts reserved before it, so seq order is dependency order.
	Seq uint64
	Attempt
}

// attempts are the unfinished attempts and the reservations they hold: the
// model's unfinished tx entries and res. It is the one place reservations
// change. add holds an attempt's inputs for its intent and finish releases
// each input no other unfinished attempt of that intent spends (Released in
// the model), so ReservedWhileUnfinished and the release half of NoLeak
// hold by construction; consistent checks both.
type attempts struct {
	recs map[TxID]*record
	// recsBy indexes recs by actor. Only insert and finish write it, in step
	// with recs; indexConsistent compares the two with a scan.
	recsBy  map[population.ActorID]map[TxID]*record
	held    map[Ref]IntentID
	nextSeq uint64
}

func newAttempts() attempts {
	return attempts{recs: map[TxID]*record{}, recsBy: map[population.ActorID]map[TxID]*record{}, held: map[Ref]IntentID{}}
}

// clone copies the maps. Records are never modified once made, so they
// are shared.
func (t *attempts) clone() attempts {
	c := *t
	c.recs = maps.Clone(t.recs)
	c.held = maps.Clone(t.held)
	c.recsBy = make(map[population.ActorID]map[TxID]*record, len(t.recsBy))
	for a, m := range t.recsBy {
		c.recsBy[a] = maps.Clone(m)
	}
	return c
}

// add records attempt a of intent id as the newest and holds its inputs
// for id. state.reserve has checked it.
func (t *attempts) add(id IntentID, a Attempt) {
	t.nextSeq++
	t.insert(&record{Intent: id, Seq: t.nextSeq, Attempt: a})
	for _, r := range a.Inputs {
		t.held[r] = id
	}
}

// restore adds a stored record, refusing an input another intent holds.
func (t *attempts) restore(rec *record) error {
	for _, r := range rec.Inputs {
		if h, ok := t.held[r]; ok && h != rec.Intent {
			return fmt.Errorf("%x#%d held by intents %d and %d", r.Tx[:4], r.Index, h, rec.Intent)
		}
		t.held[r] = rec.Intent
	}
	t.insert(rec)
	return nil
}

func (t *attempts) insert(rec *record) {
	t.recs[rec.Tx] = rec
	m := t.recsBy[rec.Actor]
	if m == nil {
		m = map[TxID]*record{}
		t.recsBy[rec.Actor] = m
	}
	m[rec.Tx] = rec
}

// finish drops rec and releases each input no other unfinished attempt of
// its intent spends (the contract's rule 7).
func (t *attempts) finish(rec *record) {
	delete(t.recs, rec.Tx)
	delete(t.recsBy[rec.Actor], rec.Tx)
	for _, r := range rec.Inputs {
		if !t.spends(rec.Intent, r) {
			delete(t.held, r)
		}
	}
}

// spends reports whether an unfinished attempt of intent id spends r.
func (t *attempts) spends(id IntentID, r Ref) bool {
	for _, rec := range t.recs {
		if rec.Intent == id && slices.Contains(rec.Inputs, r) {
			return true
		}
	}
	return false
}

// offerable reports whether r, unless in spent, is free or held by id.
func (t *attempts) offerable(r Ref, id IntentID, spent map[Ref]struct{}) bool {
	if _, ok := spent[r]; ok {
		return false
	}
	h, ok := t.held[r]
	return !ok || h == id
}

// appendChange adds the offerable change of recs, within o.MaxDepth.
func (t *attempts) appendChange(out []Utxo, recs map[TxID]*record, id IntentID, o Opts, spent map[Ref]struct{}) []Utxo {
	if !o.Chain {
		return out
	}
	for _, rec := range recs {
		if o.MaxDepth > 0 && t.depth(rec)+1 > o.MaxDepth {
			continue
		}
		for _, c := range rec.Change {
			if t.offerable(c.Ref, id, spent) {
				out = append(out, c)
			}
		}
	}
	return out
}

// sorted returns the unfinished records in seq order.
func (t *attempts) sorted() []*record {
	out := slices.Collect(maps.Values(t.recs))
	slices.SortFunc(out, func(a, b *record) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// parent returns the unfinished attempt whose change r is.
func (t *attempts) parent(r Ref) (*record, bool) {
	p, ok := t.recs[r.Tx]
	return p, ok
}

// depth counts the unfinished attempts in rec's ancestry, itself included.
func (t *attempts) depth(rec *record) int {
	d := 0
	for _, r := range rec.Inputs {
		if p, ok := t.parent(r); ok {
			d = max(d, t.depth(p))
		}
	}
	return d + 1
}

// ancestry is the unfinished attempts the inputs ins build on, directly or
// through others, in seq order (Anc in the model, less the new attempt).
func (t *attempts) ancestry(ins []Ref) []*record {
	seen := map[TxID]*record{}
	var walk func(refs []Ref)
	walk = func(refs []Ref) {
		for _, r := range refs {
			p, ok := t.parent(r)
			if !ok || seen[p.Tx] != nil {
				continue
			}
			seen[p.Tx] = p
			walk(p.Inputs)
		}
	}
	walk(ins)
	out := slices.Collect(maps.Values(seen))
	slices.SortFunc(out, func(a, b *record) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

func overlap(a, b []Ref) bool {
	for _, r := range a {
		if slices.Contains(b, r) {
			return true
		}
	}
	return false
}

// coherent reports whether a transaction spending ins could land: no two
// of it and its ancestors share an input (Coherent in the model).
func coherent(ins []Ref, mine []*record) bool {
	for i, x := range mine {
		if overlap(ins, x.Inputs) {
			return false
		}
		for _, y := range mine[i+1:] {
			if overlap(x.Inputs, y.Inputs) {
				return false
			}
		}
	}
	return true
}

// excludes reports whether a transaction spending ins, built on mine, can
// never be on one chain with p: it or an ancestor shares an input with p or
// an ancestor of p (Excludes in the model). The ledger includes at most one
// of two transactions sharing an input.
func (t *attempts) excludes(ins []Ref, mine []*record, p *record) bool {
	for _, y := range append(t.ancestry(p.Inputs), p) {
		if overlap(ins, y.Inputs) {
			return true
		}
		for _, x := range mine {
			if x != y && overlap(x.Inputs, y.Inputs) {
				return true
			}
		}
	}
	return false
}

func (t *attempts) wellFormed(a Attempt) error {
	if len(a.Inputs) == 0 {
		return fmt.Errorf("%w: no inputs", ErrBadAttempt)
	}
	for i, r := range a.Inputs {
		if slices.Contains(a.Inputs[i+1:], r) {
			return fmt.Errorf("%w: input %x#%d repeated", ErrBadAttempt, r.Tx[:4], r.Index)
		}
	}
	if _, ok := t.recs[a.Tx]; ok {
		return fmt.Errorf("%w: tx %x already reserved", ErrBadAttempt, a.Tx[:4])
	}
	for _, c := range a.Change {
		if c.Owner != a.Actor || c.Ref.Tx != a.Tx {
			return fmt.Errorf("%w: change %x#%d is not the actor's output of this tx", ErrBadAttempt, c.Ref.Tx[:4], c.Ref.Index)
		}
	}
	return nil
}

// consistent checks ReservedWhileUnfinished and the release half of NoLeak:
// the inputs of every unfinished attempt are held by its intent, and every
// held output is spent by an unfinished attempt of the holding intent.
func (t *attempts) consistent() error {
	for _, rec := range t.recs {
		for _, r := range rec.Inputs {
			if h, ok := t.held[r]; !ok || h != rec.Intent {
				return fmt.Errorf("input %x#%d of unfinished %x not held by intent %d", r.Tx[:4], r.Index, rec.Tx[:4], rec.Intent)
			}
		}
	}
	for r, id := range t.held {
		if !t.spends(id, r) {
			return fmt.Errorf("%x#%d held by intent %d for no unfinished attempt", r.Tx[:4], r.Index, id)
		}
	}
	return nil
}

// indexConsistent reports whether recsBy holds exactly what recs implies.
func (t *attempts) indexConsistent() error {
	n := 0
	for a, m := range t.recsBy {
		for tx, rec := range m {
			if t.recs[tx] != rec || rec.Actor != a {
				return fmt.Errorf("actor index lists %x under actor %d", tx[:4], a)
			}
			n++
		}
	}
	if n != len(t.recs) {
		return fmt.Errorf("actor index has %d attempts, recs %d", n, len(t.recs))
	}
	return nil
}
