package pay

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Actors are the actors as the retry machine reads and changes them.
type Actors interface {
	Get(id population.ActorID) (population.Actor, bool)
	// Draw is the actor's stream for its next random decision.
	Draw(a population.Actor, purpose string) *rand.Rand
	// Known reports an actor that was ever spawned.
	Known(id population.ActorID) bool
	// Learn raises the actor's competence: a fix it made worked.
	Learn(id population.ActorID) error
	// Frustrate raises the actor's frustration: it gave up.
	Frustrate(at sched.Time, id population.ActorID) error
}

// Builder builds an intent's next attempt and logs it.
type Builder interface {
	// Attempt builds one attempt of p on inputs the ledger offers. A
	// non-empty why is a skip, not an error.
	Attempt(ctx context.Context, at sched.Time, p *wire.Pending) (built build.Built, inputs []wire.Ref, ttl uint64, why string, err error)
	// Submitted logs the reserved attempt.
	Submitted(at sched.Time, p *wire.Pending, built build.Built, inputs []wire.Ref)
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
	// End records that an intent ended in this input.
	End(id uint64, tx wire.TxID, released bool)
}

// Scheduler is the event queue as the retry machine uses it.
type Scheduler interface {
	// Retry schedules a tick for intent id's next send.
	Retry(at sched.Time, id uint64)
}

// Chain is the K-deep point the expiry of an attempt is judged at.
type Chain interface {
	Confirmed() eventlog.ChainPoint
}

// Spec is what a Retry is built from.
type Spec struct {
	Policy submit.Policy
	// Diagnoser reads rejections; nil has each actor read them at its
	// competence (mistake.Diagnoser).
	Diagnoser submit.Diagnoser
}

// Env is what the retry machine shares with the rest of the engine.
type Env struct {
	Actors  Actors
	Builder Builder
	Journal Journal
	Sched   Scheduler
	Chain   Chain
}

// Retry runs internal/submit's retry machine for each payment. Step
// decides; Retry does what the actions say, and the only thing it ever
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
type Retry struct {
	spec Spec
	pay  *Pay
	env  Env
}

// NewRetry returns the retry machines of pay's intents.
func NewRetry(spec Spec, pay *Pay, env Env) *Retry {
	return &Retry{spec: spec, pay: pay, env: env}
}

// Begin starts a payment's machine on its first attempt, which the
// composite has already built and reserved.
func (rt *Retry) Begin(p *wire.Pending) error {
	a, _ := rt.env.Actors.Get(population.ActorID(p.Actor))
	for _, ev := range []submit.Event{submit.Start{}, submit.Built{Tx: plan.TxID(p.Tx)}} {
		m, _, err := submit.Step(rt.spec.Policy, p.M, ev, a, nil)
		if err != nil {
			return fmt.Errorf("engine: intent %d: %w", p.ID, err)
		}
		p.M = m
	}
	return nil
}

// Answer feeds the node's answer to the payment's machine. why names a
// rejection's classes for the Refused decision, which also keeps the
// node's own text for each failure.
func (rt *Retry) Answer(ctx context.Context, at sched.Time, p *wire.Pending, out submit.Outcome, why string) error {
	return rt.apply(ctx, at, p, submit.Answered{Out: out}, reason{why: why, detail: detailOf(out)})
}

// reason is what a Refused decision says: why names the classes, detail
// is the node's text for them.
type reason struct {
	// why is the Refused decision's Why.
	why string
	// detail is the Refused decision's Detail.
	detail string
}

// detailOf joins the node's text of each failure that has one as
// "class: text", in failure order.
func detailOf(out submit.Outcome) string {
	var ds []string
	for _, f := range out.Failures {
		if f.Detail != "" {
			ds = append(ds, string(f.Class)+": "+f.Detail)
		}
	}
	return strings.Join(ds, "; ")
}

// Settle tells the machine of the intent whose attempt tx settled, forgets
// the intent and logs the confirmation. It returns the intent, nil when tx
// is none of the engine's.
func (rt *Retry) Settle(ctx context.Context, at sched.Time, tx plan.TxID) (*wire.Pending, error) {
	id, ok := rt.pay.Of(wire.TxID(tx))
	if !ok {
		return nil, nil
	}
	p := rt.pay.Get(id)
	if err := rt.observe(ctx, at, p, submit.Settled{Tx: tx}); err != nil {
		return nil, err
	}
	rt.pay.drop(id)
	rt.env.Journal.End(id, wire.TxID(tx), false)
	rt.env.Journal.Emit(at, wire.KindConfirmed, population.ActorID(p.Actor), wire.Confirmed{Intent: id, ID: wire.TxID(tx), Lost: p.Lost})
	return p, nil
}

// InBlock tells the machines whose attempts a block carries.
func (rt *Retry) InBlock(ctx context.Context, at sched.Time, b wire.Block) error {
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
func (rt *Retry) LeftView(ctx context.Context, at sched.Time, gone []wire.Block) error {
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
func (rt *Retry) Dropped(ctx context.Context, at sched.Time, r wire.SubmitResult) error {
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
func (rt *Retry) Release(ctx context.Context, at sched.Time, dead []plan.TxID, why string) error {
	for _, tx := range dead {
		id, ok := rt.pay.Of(wire.TxID(tx))
		if !ok {
			continue
		}
		p := rt.pay.Get(id)
		rt.pay.forget(wire.TxID(tx))
		if err := rt.dead(ctx, at, p, wire.TxID(tx)); err != nil {
			return err
		}
		if rt.pay.Get(id) != p || len(p.Txs) > 0 || (!p.OneShot() && p.M.State != submit.GaveUp) {
			continue
		}
		w := why
		if w == "" {
			w = "conflict"
			if p.TTL <= rt.env.Chain.Confirmed().Slot {
				w = "expired"
			}
		}
		rt.pay.drop(id)
		rt.env.Journal.End(id, wire.TxID(tx), true)
		rt.env.Journal.Emit(at, wire.KindReleased, population.ActorID(p.Actor), wire.Released{Intent: id, Why: w})
	}
	return nil
}

// Load puts back the unfinished intents a snapshot recorded, in its order,
// and wakes each one still backing off at now. Every unfinished attempt the
// ledger holds must belong to one of them.
func (rt *Retry) Load(now sched.Time, ps []*wire.Pending) error {
	for _, p := range ps {
		rt.pay.Add(p)
		if p.Awaiting && p.NotBefore > now {
			rt.Wake(p)
		}
	}
	attempts := 0
	for p := range rt.pay.All() {
		attempts += len(p.Txs)
	}
	if n := len(rt.pay.Reads().Unfinished()); n != attempts {
		return fmt.Errorf("engine: snapshot has %d attempts, ledger %d unfinished", attempts, n)
	}
	return nil
}

// Wake schedules a tick at the end of the payment's backoff, so the loop
// comes back to send it.
func (rt *Retry) Wake(p *wire.Pending) {
	rt.env.Sched.Retry(p.NotBefore, p.ID)
}

// apply feeds one event to the payment's machine and carries out its
// actions. why is the rejection's reason, for the Refused decision.
func (rt *Retry) apply(ctx context.Context, at sched.Time, p *wire.Pending, ev submit.Event, why reason) error {
	a, _ := rt.env.Actors.Get(population.ActorID(p.Actor))
	m, acts, err := submit.Step(rt.spec.Policy, p.M, ev, a, rt.env.Actors.Draw(a, "retry"))
	if err != nil {
		return fmt.Errorf("engine: intent %d: %w", p.ID, err)
	}
	return rt.carry(ctx, at, p, m, acts, why)
}

// carry adopts the machine's next state and does its actions. The bytes
// are kept while the attempt is pending: a rollback can ask for them again.
func (rt *Retry) carry(ctx context.Context, at sched.Time, p *wire.Pending, m submit.Machine, acts []submit.Action, why reason) error {
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
		rt.pay.drop(p.ID)
		rt.env.Journal.End(p.ID, p.Tx, true)
		rt.env.Journal.Emit(at, wire.KindReleased, population.ActorID(p.Actor), wire.Released{Intent: p.ID, Why: "gave up"})
	}
	return nil
}

func isBuild(a submit.Action) bool {
	_, ok := a.(submit.BuildAttempt)
	return ok
}

// act1 carries out one action. A backoff that precedes a build is not
// waited: the engine builds at once and waits before the send that follows,
// so the delay lands where the node is next asked.
func (rt *Retry) act1(ctx context.Context, at sched.Time, p *wire.Pending, act submit.Action, beforeBuild bool, why reason) error {
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
		a, _ := rt.env.Actors.Get(population.ActorID(p.Actor))
		c := rt.diagnoser(a).Diagnose(rt.env.Actors.Draw(a, "diagnose"), act.Failures)
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
		rt.env.Journal.Emit(at, wire.KindGaveUp, population.ActorID(p.Actor), wire.GaveUp{Intent: p.ID})
	case submit.Learn:
		return rt.env.Actors.Learn(population.ActorID(p.Actor))
	case submit.Frustrate:
		return rt.env.Actors.Frustrate(at, population.ActorID(p.Actor))
	}
	return nil
}

// refuse writes off an attempt every node rejected before it arrived.
func (rt *Retry) refuse(ctx context.Context, at sched.Time, p *wire.Pending, tx plan.TxID, why reason) error {
	dead, err := rt.pay.led.Refused(ctx, tx)
	if err != nil {
		return fmt.Errorf("engine: refuse intent %d: %w", p.ID, err)
	}
	rt.env.Journal.Emit(at, wire.KindRefused, population.ActorID(p.Actor), wire.Refused{
		Intent: p.ID,
		ID:     wire.TxID(tx),
		Why:    why.why,
		Detail: why.detail,
	})
	for _, d := range dead {
		rt.pay.forget(wire.TxID(d))
	}
	return nil
}

// rebuild builds the next attempt of the intent through Reserve, so it
// conflicts with every unfinished earlier attempt and cannot land twice. It
// reports BuildFailed, and the machine gives up, when there is nothing to
// spend, the builder refuses, or Reserve does.
func (rt *Retry) rebuild(ctx context.Context, at sched.Time, p *wire.Pending) (submit.Event, error) {
	if p.Kind == "" {
		if !rt.env.Actors.Known(population.ActorID(p.To)) {
			return submit.BuildFailed{}, nil
		}
	}
	built, inputs, ttl, why, err := rt.env.Builder.Attempt(ctx, at, p)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return submit.BuildFailed{}, nil
	}
	if err := rt.pay.Reserve(ctx, p.ID, population.ActorID(p.Actor), wire.TxID(built.ID), inputs, ttl); err != nil {
		return submit.BuildFailed{}, nil
	}
	p.Tx, p.TTL, p.Cbor = wire.TxID(built.ID), ttl, built.Tx
	p.Txs = append(p.Txs, p.Tx)
	rt.pay.byTx[p.Tx] = p.ID
	rt.env.Builder.Submitted(at, p, built, inputs)
	return submit.Built{Tx: plan.TxID(built.ID)}, nil
}

// observe feeds an observation to p's machine. One the machine has no row
// for in its state (an attempt in a block while a resend is unanswered)
// tells it nothing and is dropped.
func (rt *Retry) observe(ctx context.Context, at sched.Time, p *wire.Pending, ev submit.Event) error {
	if p.OneShot() || p.M.State == submit.GaveUp || p.M.State == submit.Done {
		return nil
	}
	err := rt.apply(ctx, at, p, ev, reason{})
	if errors.Is(err, submit.ErrIllegal) {
		return nil
	}
	return err
}

// dead tells p's machine its current attempt is written off. The machine
// builds the next attempt, through Reserve, unless patience is spent.
func (rt *Retry) dead(ctx context.Context, at sched.Time, p *wire.Pending, tx wire.TxID) error {
	if tx != p.Tx {
		return nil
	}
	return rt.observe(ctx, at, p, submit.Dead{})
}

// diagnoser is who reads a rejection: the configured diagnoser, or the
// actor at its competence.
func (rt *Retry) diagnoser(a population.Actor) submit.Diagnoser {
	if rt.spec.Diagnoser == nil {
		return mistake.Diagnoser{Competence: a.Competence}
	}
	return rt.spec.Diagnoser
}
