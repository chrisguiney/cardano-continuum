package engine

import (
	"context"
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file feeds the retry machine what the chain and the mempool show,
// besides the node's answers: an attempt in a block (InBlock), out of the
// view again after a rollback (LeftView), absent from the mempool
// (Dropped), written off by the planner (Dead) and settled (Settled). The
// machine decides; the planner alone releases (models/specs/submit-retry,
// rule 1).
//
// A restart does not go through submit.Restart. It would be a logged input
// that changes the machines (every unfinished attempt sent, pending ones
// sent again), and a crashed run's log would then differ from the
// uninterrupted one, which engine-recovery design A requires byte for
// byte. The Runner instead marks the submissions a crash interrupted as
// possibly sent (SubmitResult.Resend), which is Restart's rule 3.

// frustration is what one give-up adds to an actor's frustration, which
// raises its dormancy hazard (population.DormancyOnsetRate).
const frustration = 1.0

// observe feeds an observation to p's machine. One the machine has no row
// for in its state (an attempt in a block while a resend is unanswered)
// tells it nothing and is dropped.
func (e *Engine) observe(ctx context.Context, at sched.Time, p *pendingRec, ev submit.Event) error {
	if p.oneShot() || p.M.State == submit.GaveUp || p.M.State == submit.Done {
		return nil
	}
	err := e.apply(ctx, at, p, ev, "")
	if errors.Is(err, submit.ErrIllegal) {
		return nil
	}
	return err
}

// inBlock tells the machines whose attempts a block carries.
func (e *Engine) inBlock(ctx context.Context, at sched.Time, b blockRec) error {
	for _, tx := range b.Txs {
		id, ok := e.byTx[tx.ID]
		if !ok {
			continue
		}
		p := e.pending[id]
		if p.M.State != submit.Pending || p.Tx != tx.ID {
			continue
		}
		if err := e.observe(ctx, at, p, submit.InBlock{Tx: plan.TxID(tx.ID)}); err != nil {
			return err
		}
	}
	return nil
}

// leftView tells the machines whose confirmed attempt a rollback removed:
// the same bytes go out again.
func (e *Engine) leftView(ctx context.Context, at sched.Time, gone []blockRec) error {
	for _, b := range gone {
		for _, tx := range b.Txs {
			id, ok := e.byTx[tx.ID]
			if !ok {
				continue
			}
			p := e.pending[id]
			if p.M.State != submit.Confirmed || p.Tx != tx.ID {
				continue
			}
			if err := e.observe(ctx, at, p, submit.LeftView{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropped records a drop notice for the intent's current attempt: the
// ledger notes it (it releases nothing) and the machine builds again.
func (e *Engine) dropped(ctx context.Context, at sched.Time, r SubmitResult) error {
	p, ok := e.pending[r.Intent]
	if !ok || r.Tx == nil || p.Tx != *r.Tx || p.M.State != submit.Pending {
		return nil
	}
	e.led.DropNotice(plan.TxID(p.Tx))
	return e.observe(ctx, at, p, submit.Dropped{})
}

// dead tells p's machine its current attempt is written off. The machine
// builds the next attempt, through Reserve, unless patience is spent.
func (e *Engine) dead(ctx context.Context, at sched.Time, p *pendingRec, tx TxID) error {
	if tx != p.Tx {
		return nil
	}
	return e.observe(ctx, at, p, submit.Dead{})
}

// learn raises the actor's competence toward 1 at its learning rate: a
// fix it made worked.
func (e *Engine) learn(id population.ActorID) error {
	a, ok := e.census.Get(id)
	if !ok {
		return nil
	}
	return e.census.SetCompetence(id, a.Competence+a.Traits.LearningRate*(1-a.Competence))
}

// frustrate raises the actor's frustration, and with it the dormancy
// hazard, which is drawn again from now.
func (e *Engine) frustrate(at sched.Time, id population.ActorID) error {
	a, ok := e.census.Get(id)
	if !ok {
		return nil
	}
	if err := e.census.SetFrustration(id, a.Frustration+frustration); err != nil {
		return err
	}
	st := e.actors[id]
	if st.hazardAt != population.Never {
		e.queue.Cancel(st.hazardH)
	}
	a, _ = e.census.Get(id)
	e.nextHazard(a, st, at)
	return nil
}
