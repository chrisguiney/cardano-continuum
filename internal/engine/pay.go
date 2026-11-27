package engine

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// frustration is what one give-up adds to an actor's frustration, which
// raises its dormancy hazard (population.DormancyOnsetRate).
const frustration = 1.0

// book is the unfinished intents: by ID, by unfinished attempt, and the
// ones waiting for a node's answer.
type book struct {
	pending  map[uint64]*pendingRec
	byTx     map[TxID]uint64
	awaiting []uint64 // ascending intent IDs without a node answer
}

func newBook() book {
	return book{pending: map[uint64]*pendingRec{}, byTx: map[TxID]uint64{}}
}

// Add records an unfinished intent and its attempts.
func (b *book) Add(p *pendingRec) {
	b.pending[p.ID] = p
	for _, tx := range p.Txs {
		b.byTx[tx] = p.ID
	}
	if p.Awaiting {
		b.awaiting = append(b.awaiting, p.ID)
	}
}

// Drop forgets an intent and its attempts.
func (b *book) Drop(id uint64) {
	if p, ok := b.pending[id]; ok {
		for _, tx := range p.Txs {
			delete(b.byTx, tx)
		}
		delete(b.pending, id)
	}
	b.Unawait(id)
}

// Unawait takes an intent off the queue of submissions to make.
func (b *book) Unawait(id uint64) {
	if i, ok := slices.BinarySearch(b.awaiting, id); ok {
		b.awaiting = slices.Delete(b.awaiting, i, i+1)
	}
}

// Get is the unfinished intent id, nil when there is none.
func (b *book) Get(id uint64) *pendingRec { return b.pending[id] }

// Of is the intent an unfinished attempt belongs to.
func (b *book) Of(tx TxID) (uint64, bool) {
	id, ok := b.byTx[tx]
	return id, ok
}

// Len is the number of unfinished intents.
func (b *book) Len() int { return len(b.pending) }

// IDs are the unfinished intents, ascending.
func (b *book) IDs() []uint64 {
	ids := make([]uint64, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// All yields the unfinished intents in no particular order; callers that
// emit or draw in the loop use IDs instead.
func (b *book) All() iter.Seq[*pendingRec] { return maps.Values(b.pending) }

// Has reports whether an unfinished intent of the actor has kind.
func (b *book) Has(id population.ActorID, kind string) bool {
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
func (b *book) InFlight(now sched.Time) []Submission {
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
func (b *book) await(p *pendingRec) {
	p.Awaiting = true
	if _, ok := slices.BinarySearch(b.awaiting, p.ID); !ok {
		b.awaiting = append(b.awaiting, p.ID)
		slices.Sort(b.awaiting)
	}
}

// forget removes a finished attempt from its payment's list and the index.
func (b *book) forget(tx TxID) {
	id, ok := b.byTx[tx]
	if !ok {
		return
	}
	delete(b.byTx, tx)
	if p, ok := b.pending[id]; ok {
		p.Txs = slices.DeleteFunc(p.Txs, func(t TxID) bool { return t == tx })
	}
}

// pay is the engine's bridge to the planner's ledger: which outputs exist,
// which are reserved, and when an attempt is settled or dead. It holds the
// unfinished intents beside it and is the only part of the engine that
// calls the ledger.
type pay struct {
	book
	led    *plan.Ledger
	nextID uint64
	// deposits is the lovelace the engine's settled certificates and
	// proposals hold in the ledger's deposit pot.
	deposits uint64
}

// Genesis starts the ledger on the genesis outputs.
func (pa *pay) Genesis(cfg plan.Config, utxos []plan.Utxo) error {
	led, err := plan.New(cfg, utxos)
	pa.led = led
	return err
}

// Open restores the ledger from its blob and the counters a snapshot kept.
func (pa *pay) Open(cfg plan.Config, blob store.Blob, nextID, deposits uint64) error {
	led, err := plan.Open(cfg, blob)
	if err != nil {
		return err
	}
	pa.led, pa.nextID, pa.deposits = led, nextID, deposits
	return nil
}

// Counters are the last intent ID handed out and the deposits held.
func (pa *pay) Counters() (nextID, deposits uint64) { return pa.nextID, pa.deposits }

// Next is the ID the next intent will get.
func (pa *pay) Next() uint64 { return pa.nextID + 1 }

// Claim takes id, from Next, for an intent whose first attempt is reserved.
func (pa *pay) Claim(id uint64) { pa.nextID = id }

// Reserve reserves an attempt of intent id; the ledger refuses one that
// does not conflict with every unfinished attempt of the intent.
func (pa *pay) Reserve(ctx context.Context, id uint64, a population.ActorID, tx TxID, inputs []Ref, ttl uint64) error {
	return pa.led.Reserve(ctx, plan.IntentID(id), attempt(a, tx, inputs, ttl))
}

// Refused writes off an attempt every node rejected before it arrived and
// returns the attempts that died with it.
func (pa *pay) Refused(ctx context.Context, tx plan.TxID) ([]plan.TxID, error) {
	return pa.led.Refused(ctx, tx)
}

// Advance feeds the ledger a chain event.
func (pa *pay) Advance(ctx context.Context, ev chain.Event) (plan.Result, error) {
	return pa.led.Advance(ctx, ev)
}

// Coins lists what the ledger offers actor a for intent id, sorted by Ref.
func (pa *pay) Coins(a population.ActorID, id plan.IntentID) []Coin {
	us := pa.led.Available(a, id, plan.Opts{})
	out := make([]Coin, len(us))
	for i, u := range us {
		out[i] = Coin{Ref: Ref{Tx: TxID(u.Ref.Tx), Index: u.Ref.Index}, Lovelace: u.Lovelace}
	}
	return out
}

// Spendable is the lovelace the ledger would offer the actor's next
// payment.
func (pa *pay) Spendable(id uint64) uint64 {
	return sumCoins(pa.Coins(population.ActorID(id), plan.IntentID(pa.nextID+1)))
}

// Balances returns each actor's settled lovelace, reserved or not, by
// actor ID.
func (pa *pay) Balances() map[uint64]uint64 {
	out := map[uint64]uint64{}
	for _, u := range pa.led.Settled() {
		out[uint64(u.Owner)] += u.Lovelace
	}
	return out
}

// Supply is the settled outputs' lovelace and the deposits held.
func (pa *pay) Supply() uint64 {
	var sum uint64
	for _, u := range pa.led.Settled() {
		sum += u.Lovelace
	}
	return sum + pa.deposits
}

// Deposit accounts for a settled attempt's deposit: paid when d is
// positive, refunded when negative. It returns the part of a refund that
// comes from outside the population.
func (pa *pay) Deposit(d int64) uint64 {
	switch {
	case d >= 0:
		pa.deposits += uint64(d)
	case uint64(-d) <= pa.deposits:
		pa.deposits -= uint64(-d)
	default:
		// A refund of a deposit paid before the engine's genesis (a
		// genesis registration) comes from outside the population.
		out := uint64(-d) - pa.deposits
		pa.deposits = 0
		return out
	}
	return 0
}

// Blob is the ledger's state, for store.Checkpoint.Reservations.
func (pa *pay) Blob() (store.Blob, error) { return pa.led.Blob() }

// Reads is the ledger's read side.
func (pa *pay) Reads() reads { return reads{pa.led} }

// reads reads the ledger without changing it.
type reads struct{ led *plan.Ledger }

// Settled implements oracle.Holders.
func (r reads) Settled() []plan.Utxo { return r.led.Settled() }

// Unfinished implements oracle.Holders.
func (r reads) Unfinished() []plan.Pending { return r.led.Unfinished() }

// Holder implements oracle.Holders.
func (r reads) Holder(ref plan.Ref) (plan.IntentID, bool) { return r.led.Holder(ref) }

// Available is what the ledger offers actor a for intent id.
func (r reads) Available(a population.ActorID, id plan.IntentID, o plan.Opts) []plan.Utxo {
	return r.led.Available(a, id, o)
}

// ResumePoint is the ledger's settled point.
func (r reads) ResumePoint() chain.Point { return r.led.ResumePoint() }

// retry runs internal/submit's retry machine for each payment. Step
// decides; retry does what the actions say, and the only thing it ever
// asks the ledger to write off is an attempt the node's answer proves never
// arrived (Refused). Everything else the ledger releases itself, on
// settlement or death.
//
// Besides the node's answers it feeds the machine what the chain and the
// mempool show: an attempt in a block (InBlock), out of the view again
// after a rollback (LeftView), absent from the mempool (Dropped), written
// off by the planner (Dead) and settled (Settled). The machine decides;
// the planner alone releases (models/specs/submit-retry, rule 1).
//
// A restart does not go through submit.Restart. It would be a logged input
// that changes the machines (every unfinished attempt sent, pending ones
// sent again), and a crashed run's log would then differ from the
// uninterrupted one, which engine-recovery design A requires byte for
// byte. The Runner instead marks the submissions a crash interrupted as
// possibly sent (SubmitResult.Resend), which is Restart's rule 3.
type retry struct {
	e    *Engine
	pol  submit.Policy
	diag submit.Diagnoser
	pay  *pay
}

// outcomeOf rebuilds the node's answer from the logged result.
func outcomeOf(r SubmitResult) (submit.Outcome, error) {
	switch r.Verdict {
	case VerdictAccepted:
		return submit.Outcome{Verdict: submit.Accepted}, nil
	case VerdictUnreached, VerdictDropped:
		return submit.Outcome{Verdict: submit.Unreached}, nil
	case VerdictRejected:
		o := submit.Outcome{Verdict: submit.Rejected}
		for _, c := range r.Failures {
			o.Failures = append(o.Failures, submit.Failure{Class: submit.Class(c)})
		}
		return o, nil
	}
	return submit.Outcome{}, fmt.Errorf("%w: intent %d", ErrUnknownVerdict, r.Intent)
}

// reasonOf joins the failure classes of a rejection for the Refused record.
func reasonOf(r SubmitResult) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "rejected: " + strings.Join(r.Failures, ", ")
}

func attempt(a population.ActorID, tx TxID, inputs []Ref, ttl uint64) plan.Attempt {
	ins := make([]plan.Ref, len(inputs))
	for i, r := range inputs {
		ins[i] = plan.Ref{Tx: plan.TxID(r.Tx), Index: r.Index}
	}
	return plan.Attempt{Tx: plan.TxID(tx), Actor: a, Inputs: ins, TTL: ttl}
}

func sumCoins(cs []Coin) uint64 {
	var sum uint64
	for _, c := range cs {
		sum += c.Lovelace
	}
	return sum
}

func isBuild(a submit.Action) bool {
	_, ok := a.(submit.BuildAttempt)
	return ok
}

// Begin starts a payment's machine on its first attempt, which the
// composite has already built and reserved.
func (rt *retry) Begin(p *pendingRec) error {
	a, _ := rt.e.co.Get(population.ActorID(p.Actor))
	for _, ev := range []submit.Event{submit.Start{}, submit.Built{Tx: plan.TxID(p.Tx)}} {
		m, _, err := submit.Step(rt.pol, p.M, ev, a, nil)
		if err != nil {
			return fmt.Errorf("engine: intent %d: %w", p.ID, err)
		}
		p.M = m
	}
	return nil
}

// Answer feeds the node's answer to the payment's machine. why is the
// node's text for a rejection, for the Refused decision.
func (rt *retry) Answer(ctx context.Context, at sched.Time, p *pendingRec, out submit.Outcome, why string) error {
	return rt.apply(ctx, at, p, submit.Answered{Out: out}, why)
}

// Settle tells the machine of the intent whose attempt tx settled, forgets
// the intent and logs the confirmation. It returns the intent, nil when tx
// is none of the engine's.
func (rt *retry) Settle(ctx context.Context, at sched.Time, tx plan.TxID) (*pendingRec, error) {
	id, ok := rt.pay.Of(TxID(tx))
	if !ok {
		return nil, nil
	}
	p := rt.pay.Get(id)
	if err := rt.observe(ctx, at, p, submit.Settled{Tx: tx}); err != nil {
		return nil, err
	}
	rt.pay.Drop(id)
	rt.e.jr.End(id, TxID(tx), false)
	rt.e.jr.Emit(at, KindConfirmed, population.ActorID(p.Actor), Confirmed{Intent: id, ID: TxID(tx), Lost: p.Lost})
	return p, nil
}

// InBlock tells the machines whose attempts a block carries.
func (rt *retry) InBlock(ctx context.Context, at sched.Time, b blockRec) error {
	for _, tx := range b.Txs {
		id, ok := rt.pay.Of(tx.ID)
		if !ok {
			continue
		}
		p := rt.pay.Get(id)
		if p.M.State != submit.Pending || p.Tx != tx.ID {
			continue
		}
		if err := rt.observe(ctx, at, p, submit.InBlock{Tx: plan.TxID(tx.ID)}); err != nil {
			return err
		}
	}
	return nil
}

// LeftView tells the machines whose confirmed attempt a rollback removed:
// the same bytes go out again.
func (rt *retry) LeftView(ctx context.Context, at sched.Time, gone []blockRec) error {
	for _, b := range gone {
		for _, tx := range b.Txs {
			id, ok := rt.pay.Of(tx.ID)
			if !ok {
				continue
			}
			p := rt.pay.Get(id)
			if p.M.State != submit.Confirmed || p.Tx != tx.ID {
				continue
			}
			if err := rt.observe(ctx, at, p, submit.LeftView{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Dropped records a drop notice for the intent's current attempt: the
// ledger notes it (it releases nothing) and the machine builds again.
func (rt *retry) Dropped(ctx context.Context, at sched.Time, r SubmitResult) error {
	p := rt.pay.Get(r.Intent)
	if p == nil || r.Tx == nil || p.Tx != *r.Tx || p.M.State != submit.Pending {
		return nil
	}
	rt.pay.led.DropNotice(plan.TxID(p.Tx))
	return rt.observe(ctx, at, p, submit.Dropped{})
}

// Release handles attempts the ledger wrote off. The current attempt of a
// live machine is Dead to it, and it builds again unless its patience is
// spent; a payment whose last unfinished attempt is dead and whose machine
// is done with it is released and forgotten. An empty why names the cause
// per payment: expiry or conflict.
func (rt *retry) Release(ctx context.Context, at sched.Time, dead []plan.TxID, why string) error {
	for _, tx := range dead {
		id, ok := rt.pay.Of(TxID(tx))
		if !ok {
			continue
		}
		p := rt.pay.Get(id)
		rt.pay.forget(TxID(tx))
		if err := rt.dead(ctx, at, p, TxID(tx)); err != nil {
			return err
		}
		if rt.pay.Get(id) != p || len(p.Txs) > 0 || (!p.oneShot() && p.M.State != submit.GaveUp) {
			continue
		}
		w := why
		if w == "" {
			w = "conflict"
			if p.TTL <= rt.e.win.Confirmed().Slot {
				w = "expired"
			}
		}
		rt.pay.Drop(id)
		rt.e.jr.End(id, TxID(tx), true)
		rt.e.jr.Emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: id, Why: w})
	}
	return nil
}

// Wake schedules a tick at the end of the payment's backoff, so the loop
// comes back to send it.
func (rt *retry) Wake(p *pendingRec) {
	rt.e.clk.Push(p.NotBefore, clock.Event{Kind: clock.Retry, Intent: p.ID})
}

// apply feeds one event to the payment's machine and carries out its
// actions. why is the node's text for a rejection, for the Refused decision.
func (rt *retry) apply(ctx context.Context, at sched.Time, p *pendingRec, ev submit.Event, why string) error {
	a, _ := rt.e.co.Get(population.ActorID(p.Actor))
	st := rt.e.co.Rec(a.ID)
	m, acts, err := submit.Step(rt.pol, p.M, ev, a, st.Draw(a, "retry"))
	if err != nil {
		return fmt.Errorf("engine: intent %d: %w", p.ID, err)
	}
	return rt.carry(ctx, at, p, m, acts, why)
}

// carry adopts the machine's next state and does its actions. The bytes
// are kept while the attempt is pending: a rollback can ask for them again.
func (rt *retry) carry(ctx context.Context, at sched.Time, p *pendingRec, m submit.Machine, acts []submit.Action, why string) error {
	p.M = m
	if m.State == submit.Pending {
		p.Awaiting = false
		rt.pay.Unawait(p.ID)
	}
	for i, act := range acts {
		if err := rt.act1(ctx, at, p, act, i+1 < len(acts) && isBuild(acts[i+1]), why); err != nil {
			return err
		}
	}
	// A nested apply (a diagnosis) may have released the payment already.
	if p.M.State == submit.GaveUp && len(p.Txs) == 0 && rt.pay.Get(p.ID) == p {
		rt.pay.Drop(p.ID)
		rt.e.jr.End(p.ID, p.Tx, true)
		rt.e.jr.Emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: p.ID, Why: "gave up"})
	}
	return nil
}

// act1 carries out one action. A backoff that precedes a build is not
// waited: the engine builds at once and waits before the send that follows,
// so the delay lands where the node is next asked.
func (rt *retry) act1(ctx context.Context, at sched.Time, p *pendingRec, act submit.Action, beforeBuild bool, why string) error {
	switch act := act.(type) {
	case submit.Backoff:
		if !beforeBuild {
			p.NotBefore = at + sched.Time(act.D.Milliseconds())
			rt.Wake(p)
		}
	case submit.SendTx:
		rt.pay.await(p)
	case submit.Refused:
		return rt.refuse(ctx, at, p, plan.TxID(act.Tx), why)
	case submit.Diagnose:
		a, _ := rt.e.co.Get(population.ActorID(p.Actor))
		c := rt.diagnoser(a).Diagnose(rt.e.co.Rec(a.ID).Draw(a, "diagnose"), act.Failures)
		return rt.apply(ctx, at, p, submit.Diagnosed{Class: c}, why)
	case submit.BuildAttempt:
		ev, err := rt.rebuild(ctx, at, p)
		if err != nil {
			return err
		}
		return rt.apply(ctx, at, p, ev, why)
	case submit.GiveUp:
		p.Awaiting, p.Cbor = false, nil
		rt.pay.Unawait(p.ID)
		rt.e.jr.Emit(at, KindGaveUp, population.ActorID(p.Actor), GaveUp{Intent: p.ID})
	case submit.Learn:
		return rt.e.learn(population.ActorID(p.Actor))
	case submit.Frustrate:
		return rt.e.frustrate(at, population.ActorID(p.Actor))
	}
	return nil
}

// refuse writes off an attempt every node rejected before it arrived.
func (rt *retry) refuse(ctx context.Context, at sched.Time, p *pendingRec, tx plan.TxID, why string) error {
	dead, err := rt.pay.led.Refused(ctx, tx)
	if err != nil {
		return fmt.Errorf("engine: refuse intent %d: %w", p.ID, err)
	}
	rt.e.jr.Emit(at, KindRefused, population.ActorID(p.Actor), Refused{Intent: p.ID, ID: TxID(tx), Why: why})
	for _, d := range dead {
		rt.pay.forget(TxID(d))
	}
	return nil
}

// rebuild builds the next attempt of the intent through Reserve, so it
// conflicts with every unfinished earlier attempt and cannot land twice. It
// reports BuildFailed, and the machine gives up, when there is nothing to
// spend, the builder refuses, or Reserve does.
func (rt *retry) rebuild(ctx context.Context, at sched.Time, p *pendingRec) (submit.Event, error) {
	if p.Kind == "" {
		if rt.e.co.Rec(population.ActorID(p.To)) == nil {
			return submit.BuildFailed{}, nil
		}
	}
	built, inputs, ttl, why, err := rt.e.attemptOf(ctx, at, p)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return submit.BuildFailed{}, nil
	}
	if err := rt.pay.Reserve(ctx, p.ID, population.ActorID(p.Actor), TxID(built.ID), inputs, ttl); err != nil {
		return submit.BuildFailed{}, nil
	}
	p.Tx, p.TTL, p.Cbor = TxID(built.ID), ttl, built.Tx
	p.Txs = append(p.Txs, p.Tx)
	rt.pay.byTx[p.Tx] = p.ID
	rt.e.emitSubmitted(at, p, built, inputs)
	return submit.Built{Tx: plan.TxID(built.ID)}, nil
}

// observe feeds an observation to p's machine. One the machine has no row
// for in its state (an attempt in a block while a resend is unanswered)
// tells it nothing and is dropped.
func (rt *retry) observe(ctx context.Context, at sched.Time, p *pendingRec, ev submit.Event) error {
	if p.oneShot() || p.M.State == submit.GaveUp || p.M.State == submit.Done {
		return nil
	}
	err := rt.apply(ctx, at, p, ev, "")
	if errors.Is(err, submit.ErrIllegal) {
		return nil
	}
	return err
}

// dead tells p's machine its current attempt is written off. The machine
// builds the next attempt, through Reserve, unless patience is spent.
func (rt *retry) dead(ctx context.Context, at sched.Time, p *pendingRec, tx TxID) error {
	if tx != p.Tx {
		return nil
	}
	return rt.observe(ctx, at, p, submit.Dead{})
}

// diagnoser is who reads a rejection: with mistakes on, the actor at its
// competence; otherwise the configured one.
func (rt *retry) diagnoser(a population.Actor) submit.Diagnoser {
	if rt.e.cfg.Mistakes && rt.e.cfg.Diagnoser == nil {
		return mistake.Diagnoser{Competence: a.Competence}
	}
	return rt.diag
}
