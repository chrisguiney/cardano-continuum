package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	evclock "github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file runs internal/submit's retry machine for each payment. Step
// decides; the engine does what the actions say, and the only thing it ever
// asks the ledger to write off is an attempt the node's answer proves never
// arrived (Refused). Everything else the ledger releases itself, on
// settlement or death.

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

// begin starts a payment's machine on its first attempt, which pay has
// already built and reserved.
func (e *Engine) begin(p *pendingRec) error {
	a, _ := e.census.Get(population.ActorID(p.Actor))
	for _, ev := range []submit.Event{submit.Start{}, submit.Built{Tx: plan.TxID(p.Tx)}} {
		m, _, err := submit.Step(e.retry, p.M, ev, a, nil)
		if err != nil {
			return fmt.Errorf("engine: intent %d: %w", p.ID, err)
		}
		p.M = m
	}
	return nil
}

// apply feeds one event to the payment's machine and carries out its
// actions. why is the node's text for a rejection, for the Refused decision.
func (e *Engine) apply(ctx context.Context, at sched.Time, p *pendingRec, ev submit.Event, why string) error {
	a, _ := e.census.Get(population.ActorID(p.Actor))
	st := e.actors[a.ID]
	m, acts, err := submit.Step(e.retry, p.M, ev, a, e.draw(a, st, "retry"))
	if err != nil {
		return fmt.Errorf("engine: intent %d: %w", p.ID, err)
	}
	return e.carry(ctx, at, p, m, acts, why)
}

// carry adopts the machine's next state and does its actions. The bytes
// are kept while the attempt is pending: a rollback can ask for them again.
func (e *Engine) carry(ctx context.Context, at sched.Time, p *pendingRec, m submit.Machine, acts []submit.Action, why string) error {
	p.M = m
	if m.State == submit.Pending {
		p.Awaiting = false
		e.stopAwaiting(p.ID)
	}
	for i, act := range acts {
		if err := e.act1(ctx, at, p, act, i+1 < len(acts) && isBuild(acts[i+1]), why); err != nil {
			return err
		}
	}
	// A nested apply (a diagnosis) may have released the payment already.
	if p.M.State == submit.GaveUp && len(p.Txs) == 0 && e.pending[p.ID] == p {
		e.dropPending(p.ID)
		e.jr.End(p.ID, p.Tx, true)
		e.jr.Emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: p.ID, Why: "gave up"})
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
func (e *Engine) act1(ctx context.Context, at sched.Time, p *pendingRec, act submit.Action, beforeBuild bool, why string) error {
	switch act := act.(type) {
	case submit.Backoff:
		if !beforeBuild {
			p.NotBefore = at + sched.Time(act.D.Milliseconds())
			e.wake(p)
		}
	case submit.SendTx:
		e.setAwaiting(p)
	case submit.Refused:
		return e.refuse(ctx, at, p, plan.TxID(act.Tx), why)
	case submit.Diagnose:
		a, _ := e.census.Get(population.ActorID(p.Actor))
		c := e.diagnoser(a).Diagnose(e.draw(a, e.actors[a.ID], "diagnose"), act.Failures)
		return e.apply(ctx, at, p, submit.Diagnosed{Class: c}, why)
	case submit.BuildAttempt:
		ev, err := e.rebuild(ctx, at, p)
		if err != nil {
			return err
		}
		return e.apply(ctx, at, p, ev, why)
	case submit.GiveUp:
		p.Awaiting, p.Cbor = false, nil
		e.stopAwaiting(p.ID)
		e.jr.Emit(at, KindGaveUp, population.ActorID(p.Actor), GaveUp{Intent: p.ID})
	case submit.Learn:
		return e.learn(population.ActorID(p.Actor))
	case submit.Frustrate:
		return e.frustrate(at, population.ActorID(p.Actor))
	}
	return nil
}

// refuse writes off an attempt every node rejected before it arrived.
func (e *Engine) refuse(ctx context.Context, at sched.Time, p *pendingRec, tx plan.TxID, why string) error {
	dead, err := e.led.Refused(ctx, tx)
	if err != nil {
		return fmt.Errorf("engine: refuse intent %d: %w", p.ID, err)
	}
	e.jr.Emit(at, KindRefused, population.ActorID(p.Actor), Refused{Intent: p.ID, ID: TxID(tx), Why: why})
	for _, d := range dead {
		e.forgetTx(TxID(d))
	}
	return nil
}

// rebuild builds the next attempt of the intent through Reserve, so it
// conflicts with every unfinished earlier attempt and cannot land twice. It
// reports BuildFailed, and the machine gives up, when there is nothing to
// spend, the builder refuses, or Reserve does.
func (e *Engine) rebuild(ctx context.Context, at sched.Time, p *pendingRec) (submit.Event, error) {
	if p.Kind == "" {
		if _, ok := e.actors[population.ActorID(p.To)]; !ok {
			return submit.BuildFailed{}, nil
		}
	}
	built, inputs, ttl, why, err := e.attemptOf(ctx, at, p)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return submit.BuildFailed{}, nil
	}
	if err := e.led.Reserve(ctx, plan.IntentID(p.ID), attempt(population.ActorID(p.Actor), TxID(built.ID), inputs, ttl)); err != nil {
		return submit.BuildFailed{}, nil
	}
	p.Tx, p.TTL, p.Cbor = TxID(built.ID), ttl, built.Tx
	p.Txs = append(p.Txs, p.Tx)
	e.byTx[p.Tx] = p.ID
	e.emitSubmitted(at, p, built, inputs)
	return submit.Built{Tx: plan.TxID(built.ID)}, nil
}

// forgetTx removes a finished attempt from its payment's list and the index.
func (e *Engine) forgetTx(tx TxID) {
	id, ok := e.byTx[tx]
	if !ok {
		return
	}
	delete(e.byTx, tx)
	if p, ok := e.pending[id]; ok {
		p.Txs = slices.DeleteFunc(p.Txs, func(t TxID) bool { return t == tx })
	}
}

// setAwaiting marks the payment's current attempt as waiting for a send.
func (e *Engine) setAwaiting(p *pendingRec) {
	p.Awaiting = true
	if _, ok := slices.BinarySearch(e.awaiting, p.ID); !ok {
		e.awaiting = append(e.awaiting, p.ID)
		slices.Sort(e.awaiting)
	}
}

// wake schedules a tick at the end of the payment's backoff, so the loop
// comes back to send it.
func (e *Engine) wake(p *pendingRec) {
	e.clk.Push(p.NotBefore, evclock.Event{Kind: evclock.Retry, Intent: p.ID})
}

// reasonOf joins the failure classes of a rejection for the Refused record.
func reasonOf(r SubmitResult) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "rejected: " + strings.Join(r.Failures, ", ")
}
