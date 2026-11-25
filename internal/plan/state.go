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

// record is one unfinished attempt. Finished attempts are dropped: nothing
// can build on a dead one, and a settled one's change is a settled output.
type record struct {
	Intent IntentID
	// Seq orders attempts by Reserve. An attempt only spends the change of
	// attempts reserved before it, so seq order is dependency order.
	Seq uint64
	Attempt
}

// vtx is a transaction in a block above the settled point. Creates holds
// only outputs whose address has an owner.
type vtx struct {
	id      TxID
	spends  []Ref
	creates []Utxo
}

type vblock struct {
	point chain.Point
	txs   []vtx
}

// state is the ledger's content. Its methods are pure: no I/O, and the
// Ledger applies them to a clone so a failed commit discards them.
type state struct {
	k       uint64
	settled chain.Point
	// utxos are the owned outputs unspent at the settled point.
	utxos  map[Ref]Utxo
	window []vblock // blocks above the settled point, oldest first
	recs   map[TxID]*record
	held   map[Ref]IntentID
	// paid are intents with a settled attempt. They accept no attempt.
	paid    map[IntentID]struct{}
	nextSeq uint64

	// byOwner indexes utxos by owner and recsBy indexes recs by actor, so a
	// balance query reads one actor's outputs instead of scanning all of
	// them. Only putUtxo, dropUtxo, addRec and delRec write them, in step
	// with utxos and recs; indexConsistent compares them with a scan.
	byOwner map[population.ActorID]map[Ref]struct{}
	recsBy  map[population.ActorID]map[TxID]*record
	// spent caches spentAbove. It is never modified once built, so clones
	// share it; nil means the window changed since. Rebuilding it for each
	// query cost a scan of every window transaction.
	spent map[Ref]struct{}
}

func newState(k uint64) *state {
	return &state{k: k, utxos: map[Ref]Utxo{}, recs: map[TxID]*record{}, held: map[Ref]IntentID{},
		paid: map[IntentID]struct{}{}, byOwner: map[population.ActorID]map[Ref]struct{}{},
		recsBy: map[population.ActorID]map[TxID]*record{}}
}

func (s *state) putUtxo(u Utxo) {
	s.utxos[u.Ref] = u
	m := s.byOwner[u.Owner]
	if m == nil {
		m = map[Ref]struct{}{}
		s.byOwner[u.Owner] = m
	}
	m[u.Ref] = struct{}{}
}

func (s *state) dropUtxo(r Ref) {
	u, ok := s.utxos[r]
	if !ok {
		return
	}
	delete(s.utxos, r)
	delete(s.byOwner[u.Owner], r)
}

func (s *state) addRec(rec *record) {
	s.recs[rec.Tx] = rec
	m := s.recsBy[rec.Actor]
	if m == nil {
		m = map[TxID]*record{}
		s.recsBy[rec.Actor] = m
	}
	m[rec.Tx] = rec
}

func (s *state) delRec(rec *record) {
	delete(s.recs, rec.Tx)
	delete(s.recsBy[rec.Actor], rec.Tx)
}

// clone copies everything a step may change. Records and blocks are never
// modified once made, so they are shared.
func (s *state) clone() *state {
	c := *s
	c.utxos = maps.Clone(s.utxos)
	c.window = slices.Clone(s.window)
	c.recs = maps.Clone(s.recs)
	c.held = maps.Clone(s.held)
	c.paid = maps.Clone(s.paid)
	c.byOwner = make(map[population.ActorID]map[Ref]struct{}, len(s.byOwner))
	for a, m := range s.byOwner {
		c.byOwner[a] = maps.Clone(m)
	}
	c.recsBy = make(map[population.ActorID]map[TxID]*record, len(s.recsBy))
	for a, m := range s.recsBy {
		c.recsBy[a] = maps.Clone(m)
	}
	return &c
}

func compareRefs(a, b Ref) int {
	return cmp.Or(slices.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}

func (s *state) tip() chain.Point {
	if n := len(s.window); n > 0 {
		return s.window[n-1].point
	}
	return s.settled
}

// spentAbove is every output a block above the settled point spends.
func (s *state) spentAbove() map[Ref]struct{} {
	if s.spent != nil {
		return s.spent
	}
	out := map[Ref]struct{}{}
	for _, b := range s.window {
		for _, tx := range b.txs {
			for _, r := range tx.spends {
				out[r] = struct{}{}
			}
		}
	}
	s.spent = out
	return out
}

// sorted returns the unfinished records in seq order.
func (s *state) sorted() []*record {
	out := slices.Collect(maps.Values(s.recs))
	slices.SortFunc(out, func(a, b *record) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// parent returns the unfinished attempt whose change r is.
func (s *state) parent(r Ref) (*record, bool) {
	p, ok := s.recs[r.Tx]
	return p, ok
}

// depth counts the unfinished attempts in rec's ancestry, itself included.
func (s *state) depth(rec *record) int {
	d := 0
	for _, r := range rec.Inputs {
		if p, ok := s.parent(r); ok {
			d = max(d, s.depth(p))
		}
	}
	return d + 1
}

func (s *state) offerable(r Ref, id IntentID, spent map[Ref]struct{}) bool {
	if _, ok := spent[r]; ok {
		return false
	}
	h, ok := s.held[r]
	return !ok || h == id
}

func (s *state) available(a population.ActorID, id IntentID, o Opts) []Utxo {
	spent := s.spentAbove()
	var out []Utxo
	for r := range s.byOwner[a] {
		if s.offerable(r, id, spent) {
			out = append(out, s.utxos[r])
		}
	}
	out = s.appendChange(out, s.recsBy[a], id, o, spent)
	slices.SortFunc(out, func(x, y Utxo) int { return compareRefs(x.Ref, y.Ref) })
	check.PostFn(func() bool { return equalUtxos(out, s.availableScan(a, id, o)) },
		"plan: owner index agrees with a full scan")
	return out
}

// availableScan is available without the indexes: the reference that
// check.PostFn holds the indexes to.
func (s *state) availableScan(a population.ActorID, id IntentID, o Opts) []Utxo {
	spent := map[Ref]struct{}{}
	for _, b := range s.window {
		for _, tx := range b.txs {
			for _, r := range tx.spends {
				spent[r] = struct{}{}
			}
		}
	}
	var out []Utxo
	for r, u := range s.utxos {
		if u.Owner == a && s.offerable(r, id, spent) {
			out = append(out, u)
		}
	}
	scan := map[TxID]*record{}
	for tx, rec := range s.recs {
		if rec.Actor == a {
			scan[tx] = rec
		}
	}
	out = s.appendChange(out, scan, id, o, spent)
	slices.SortFunc(out, func(x, y Utxo) int { return compareRefs(x.Ref, y.Ref) })
	return out
}

// appendChange adds the offerable change of recs, within o.MaxDepth.
func (s *state) appendChange(out []Utxo, recs map[TxID]*record, id IntentID, o Opts, spent map[Ref]struct{}) []Utxo {
	if !o.Chain {
		return out
	}
	for _, rec := range recs {
		if o.MaxDepth > 0 && s.depth(rec)+1 > o.MaxDepth {
			continue
		}
		for _, c := range rec.Change {
			if s.offerable(c.Ref, id, spent) {
				out = append(out, c)
			}
		}
	}
	return out
}

func equalUtxos(a, b []Utxo) bool { return slices.Equal(a, b) }

// indexConsistent reports whether byOwner and recsBy hold exactly what
// utxos and recs imply.
func (s *state) indexConsistent() error {
	n := 0
	for a, m := range s.byOwner {
		for r := range m {
			if u, ok := s.utxos[r]; !ok || u.Owner != a {
				return fmt.Errorf("owner index lists %x#%d under actor %d", r.Tx[:4], r.Index, a)
			}
			n++
		}
	}
	if n != len(s.utxos) {
		return fmt.Errorf("owner index has %d outputs, utxos %d", n, len(s.utxos))
	}
	n = 0
	for a, m := range s.recsBy {
		for tx, rec := range m {
			if s.recs[tx] != rec || rec.Actor != a {
				return fmt.Errorf("actor index lists %x under actor %d", tx[:4], a)
			}
			n++
		}
	}
	if n != len(s.recs) {
		return fmt.Errorf("actor index has %d attempts, recs %d", n, len(s.recs))
	}
	return nil
}

// ancestry is the unfinished attempts the inputs ins build on, directly or
// through others, in seq order.
func (s *state) ancestry(ins []Ref) []*record {
	seen := map[TxID]*record{}
	var walk func(refs []Ref)
	walk = func(refs []Ref) {
		for _, r := range refs {
			p, ok := s.parent(r)
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
func (s *state) excludes(ins []Ref, mine []*record, p *record) bool {
	for _, y := range append(s.ancestry(p.Inputs), p) {
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

func (s *state) wellFormed(a Attempt) error {
	if len(a.Inputs) == 0 {
		return fmt.Errorf("%w: no inputs", ErrBadAttempt)
	}
	for i, r := range a.Inputs {
		if slices.Contains(a.Inputs[i+1:], r) {
			return fmt.Errorf("%w: input %x#%d repeated", ErrBadAttempt, r.Tx[:4], r.Index)
		}
	}
	if _, ok := s.recs[a.Tx]; ok {
		return fmt.Errorf("%w: tx %x already reserved", ErrBadAttempt, a.Tx[:4])
	}
	for _, c := range a.Change {
		if c.Owner != a.Actor || c.Ref.Tx != a.Tx {
			return fmt.Errorf("%w: change %x#%d is not the actor's output of this tx", ErrBadAttempt, c.Ref.Tx[:4], c.Ref.Index)
		}
	}
	return nil
}

// reserve checks attempt a of intent id against the contract's rule 3 and
// records it.
func (s *state) reserve(id IntentID, a Attempt) error {
	if err := s.wellFormed(a); err != nil {
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
	mine := s.ancestry(a.Inputs)
	if !coherent(a.Inputs, mine) {
		return fmt.Errorf("%w: intent %d", ErrContradictory, id)
	}
	for _, p := range s.sorted() {
		if p.Intent == id && !s.excludes(a.Inputs, mine, p) {
			return fmt.Errorf("%w: intent %d, earlier attempt %x", ErrNotExclusive, id, p.Tx[:4])
		}
	}
	s.nextSeq++
	s.addRec(&record{Intent: id, Seq: s.nextSeq, Attempt: a})
	for _, r := range a.Inputs {
		s.held[r] = id
	}
	return nil
}

// finish drops rec and releases each input no other unfinished attempt of
// its intent spends (the contract's rule 7).
func (s *state) finish(rec *record) {
	s.delRec(rec)
	for _, r := range rec.Inputs {
		if !s.spends(rec.Intent, r) {
			delete(s.held, r)
		}
	}
}

// spends reports whether an unfinished attempt of intent id spends r.
func (s *state) spends(id IntentID, r Ref) bool {
	for _, rec := range s.recs {
		if rec.Intent == id && slices.Contains(rec.Inputs, r) {
			return true
		}
	}
	return false
}

// doomed reports whether rec can never land, judged from the settled part
// of the view only (Doomed in the model). An input is either the change of
// an unfinished attempt, judged when that attempt is, or must still be a
// settled output: a missing one was spent by another settled transaction,
// or built on an attempt already written off.
func (s *state) doomed(rec *record) bool {
	if rec.TTL != 0 && s.settled.Slot >= rec.TTL {
		return true
	}
	for _, r := range rec.Inputs {
		if _, ok := s.parent(r); ok {
			continue
		}
		if _, ok := s.utxos[r]; !ok {
			return true
		}
	}
	return false
}

// reap writes off every doomed attempt. Seq order puts parents first, so a
// child sees its parent already gone and dies in the same pass.
func (s *state) reap() []TxID {
	var dead []TxID
	for _, rec := range s.sorted() {
		if s.doomed(rec) {
			s.finish(rec)
			dead = append(dead, rec.Tx)
		}
	}
	return dead
}

// settle folds the oldest block above the settled point into the settled
// outputs and returns the attempts it held.
func (s *state) settle() []TxID {
	b := s.window[0]
	s.window = s.window[1:]
	s.spent = nil
	var done []TxID
	for _, tx := range b.txs {
		for _, r := range tx.spends {
			s.dropUtxo(r)
		}
		for _, u := range tx.creates {
			s.putUtxo(u)
		}
		if rec, ok := s.recs[tx.id]; ok {
			s.paid[rec.Intent] = struct{}{}
			s.finish(rec)
			done = append(done, tx.id)
		}
	}
	s.settled = b.point
	return done
}

func (s *state) forward(b chain.Block, o Owners) (Result, error) {
	tip := s.tip()
	if !tip.IsOrigin() && (b.Prev != tip.Hash || b.Point.Slot <= tip.Slot) {
		return Result{}, fmt.Errorf("%w: block %d at slot %d, tip at slot %d", ErrNotConnected, b.Number, b.Point.Slot, tip.Slot)
	}
	vb := vblock{point: b.Point}
	for _, tx := range b.Txs {
		t := vtx{id: tx.Hash, spends: tx.Spends}
		for _, c := range tx.Creates {
			if owner, ok := o.Owner(c.Output.Address()); ok {
				t.creates = append(t.creates, Utxo{Ref: chain.RefOf(c.Id), Owner: owner, Lovelace: c.Output.Amount().Uint64()})
			}
		}
		vb.txs = append(vb.txs, t)
	}
	s.window = append(s.window, vb)
	s.spent = nil
	var res Result
	for uint64(len(s.window)) > s.k {
		res.Settled = append(res.Settled, s.settle()...)
	}
	res.Dead = s.reap()
	return res, nil
}

// rollback cuts the view back to p. It changes no reservation: a rolled
// back attempt may be included again, and the settled point stays.
func (s *state) rollback(p chain.Point) error {
	if p == s.tip() {
		return nil
	}
	keep := -1
	if p == s.settled {
		keep = 0
	}
	for i, b := range s.window {
		if b.point == p {
			keep = i + 1
		}
	}
	if keep < 0 {
		return fmt.Errorf("%w: slot %d, settled at slot %d", ErrTooDeep, p.Slot, s.settled.Slot)
	}
	s.window = s.window[:keep]
	s.spent = nil
	return nil
}

func (s *state) refused(tx TxID) ([]TxID, error) {
	rec, ok := s.recs[tx]
	if !ok {
		return nil, fmt.Errorf("%w: %x", ErrUnknownTx, tx[:4])
	}
	s.finish(rec)
	return append([]TxID{tx}, s.reap()...), nil
}

// consistent checks the model's store invariants. Callers run it in debug
// builds and tests.
func (s *state) consistent() error {
	for _, rec := range s.recs {
		for _, r := range rec.Inputs {
			// ReservedWhileUnfinished.
			if h, ok := s.held[r]; !ok || h != rec.Intent {
				return fmt.Errorf("input %x#%d of unfinished %x not held by intent %d", r.Tx[:4], r.Index, rec.Tx[:4], rec.Intent)
			}
			// InputsTracked: an input is an unfinished attempt's change or a
			// settled output, never a rolled-back or written-off one.
			_, isChange := s.parent(r)
			if _, isSettled := s.utxos[r]; !isChange && !isSettled {
				return fmt.Errorf("unfinished %x spends untracked %x#%d", rec.Tx[:4], r.Tx[:4], r.Index)
			}
		}
		if s.doomed(rec) {
			return fmt.Errorf("unfinished %x is doomed", rec.Tx[:4])
		}
	}
	// NoLeak.
	for r, id := range s.held {
		if !s.spends(id, r) {
			return fmt.Errorf("%x#%d held by intent %d for no unfinished attempt", r.Tx[:4], r.Index, id)
		}
	}
	if uint64(len(s.window)) > s.k {
		return fmt.Errorf("%d blocks above the settled point, K is %d", len(s.window), s.k)
	}
	return nil
}
