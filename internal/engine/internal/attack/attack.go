// Package attack wires internal/adversary into the engine. An adversary is
// an actor whose profile has an adversary block: Arm arms its agenda with
// the attacks (agenda.WithAttacks), Strike hands a chosen attack to
// adversary.Plan, and the step is built, charged, reserved and logged.
// front-run has no turn of its own: the composite hands every honest
// unlock to React, and the front-runner's outbidding unlock is sent ahead
// of it.
//
// An attack is one-shot, like a refill: no retry machine drives it, a
// refused attempt is released, and every answer is judged against the
// verdict its step calls for (malformed ones: never an acceptance).
//
// Nothing here runs unless the run's adversary clearance is set (the
// engine refuses an adversary actor otherwise), and every lovelace spent
// is charged against the actor's cost ceiling for the epoch before
// anything is reserved.
//
// The composite orders every call. Strike draws only from the stream it
// is handed; the order of its emits and sends is the determinism contract.
package attack

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Cover marks, in Submitted.Attack, an adversary's ordinary intent.
const Cover = "cover"

// DustKind is a dust flood's intent kind, which the intent component
// registers as an extra built by OneShot.
const DustKind = "dust"

// Attack is the adversaries' side of the engine: their accounts, what
// they see, and every attack step.
type Attack struct {
	*Accounts
	*Sight
	mk   *maker
	core *core
}

// New returns the attack component with no adversary armed.
func New(spec Spec, env Env) *Attack {
	c := &core{spec: spec, env: env}
	acct := &Accounts{core: c, advs: map[population.ActorID]adversary.Policy{}, costs: adversary.NewLedger(), contested: map[wire.Ref]bool{}}
	return &Attack{Accounts: acct, Sight: &Sight{core: c, acct: acct}, mk: &maker{core: c}, core: c}
}

// IsKind reports an agenda intent kind that is an attack.
func IsKind(k profile.IntentKind) bool {
	_, ok := adversary.Of(k)
	return ok
}

// OneShot is the rebuild of a one-shot attack's attempt, which never
// happens: no retry machine drives an attack.
func OneShot(context.Context, keys.Wallet, build.Spend, *wire.Pending) (build.Built, error) {
	return build.Built{}, fmt.Errorf("engine: a one-shot attack is never rebuilt")
}

// Strike plans and makes the attack the adversary chose.
func (k *Attack) Strike(ctx context.Context, at sched.Time, a population.Actor, in agenda.Intent, r *rand.Rand) error {
	n, _ := adversary.Of(in.Kind)
	step, why := adversary.Plan(n, k.core.env.View, r)
	if why == "" {
		var err error
		if why, err = k.strike(ctx, at, a.ID, step, 0); err != nil {
			return err
		}
	}
	if why != "" {
		k.core.env.Journal.Emit(at, wire.KindSkipped, a.ID, wire.Skipped{Kind: string(n), Why: why})
	}
	return nil
}

// React gives every front-runner the honest unlock p, built with fee, to
// outbid; the first that can afford it does.
func (k *Attack) React(ctx context.Context, at sched.Time, p *wire.Pending, fee uint64) error {
	if p.Kind != k.core.spec.Unlock || p.Attack != "" || p.Locked == nil || len(k.advs) == 0 {
		return nil
	}
	target := adversary.Locked{
		Ref:   adversary.Ref{Tx: p.Locked.Tx, Index: p.Locked.Index},
		Owner: population.ActorID(p.Actor), Lovelace: p.Amount, Busy: true,
	}
	for _, id := range k.core.env.Roster.IDs() {
		if pol := k.advs[id]; !pol.Has(adversary.FrontRun) || k.abandoned(id) {
			continue
		}
		why, err := k.strike(ctx, at, id, adversary.Outbid(target, fee), p.ID)
		if err != nil || why == "" {
			return err
		}
		k.core.env.Journal.Emit(at, wire.KindSkipped, id, wire.Skipped{Kind: string(adversary.FrontRun), Why: why})
	}
	return nil
}

// strike builds step for adversary id and, when the cost fits its ceiling,
// reserves and logs it. against is the honest intent a front-run outbids.
// It returns why the step was not made, if it was not.
func (k *Attack) strike(ctx context.Context, at sched.Time, id population.ActorID, step adversary.Step, against uint64) (string, error) {
	env := &k.core.env
	iid := env.Ledger.Next()
	s, why, err := k.mk.build(ctx, at, id, iid, step)
	if why != "" || err != nil {
		return why, err
	}
	epoch := k.core.spec.Slots.EpochAt(at)
	if left := k.costs.Remaining(id, epoch); s.cost > left {
		return fmt.Sprintf("cost ceiling: step costs %d, %d left this epoch", s.cost, left), nil
	}
	for _, b := range s.txs {
		if err := env.Ledger.Reserve(ctx, iid, id, wire.TxID(b.ID), s.inputs, s.ttl); err != nil {
			return "reserve: " + err.Error(), nil
		}
	}
	if err := k.costs.Charge(id, epoch, s.cost); err != nil {
		return "", fmt.Errorf("engine: charge attack %d: %w", iid, err)
	}
	env.Ledger.Claim(iid)
	p := s.rec
	p.ID, p.Actor, p.Attack, p.Want = iid, uint64(id), string(step.Attack), wire.WantOf(s.want)
	p.Tx, p.TTL, p.Cbor, p.Awaiting, p.Jump = wire.TxID(s.txs[0].ID), s.ttl, s.txs[0].Tx, true, against != 0
	body := wire.Attack{Intent: iid, Name: string(step.Attack), Mutation: s.mutation, Cost: s.cost, Against: against, Payees: s.payees, Spend: p.Locked}
	for _, b := range s.txs {
		p.Txs = append(p.Txs, wire.TxID(b.ID))
		body.Txs = append(body.Txs, wire.TxID(b.ID))
	}
	for _, b := range s.txs[1:] {
		p.Rivals = append(p.Rivals, wire.Rival{ID: wire.TxID(b.ID), Tx: b.Tx})
	}
	if p.Locked != nil {
		k.contested[*p.Locked] = true
		body.Value = p.Amount
	}
	env.Ledger.Add(&p)
	env.Pace.Excite(at)
	env.Journal.Emit(at, wire.KindAttack, id, body)
	for _, b := range s.txs {
		env.Maker.Submitted(at, &p, b, s.inputs)
	}
	return "", nil
}

// Result judges and records the node's answers to an attack. A first-send
// rejection writes the attempt off; the intent is released when none is
// left.
func (k *Attack) Result(ctx context.Context, at sched.Time, p *wire.Pending, r wire.SubmitResult) error {
	env := &k.core.env
	out, err := env.Retry.Outcome(r)
	if err != nil {
		return err
	}
	if err := k.judge(at, p, r, out); err != nil {
		return err
	}
	var dead []plan.TxID
	refuse := func(tx wire.TxID) error {
		d, err := env.Flight.Refused(ctx, plan.TxID(tx))
		dead = append(dead, d...)
		return err
	}
	if env.Retry.Refusable(r.Verdict, r.Resend) {
		if err := refuse(p.Tx); err != nil {
			return fmt.Errorf("engine: refuse attack %d: %w", p.ID, err)
		}
	}
	for i, a := range r.Rivals {
		if !env.Retry.Refusable(a.Verdict, r.Resend) || i >= len(p.Rivals) {
			continue
		}
		if err := refuse(p.Rivals[i].ID); err != nil {
			return fmt.Errorf("engine: refuse rival of attack %d: %w", p.ID, err)
		}
	}
	p.Awaiting, p.Cbor, p.Rivals = false, nil, nil
	env.Flight.Unawait(p.ID)
	return env.Retry.Release(ctx, at, dead, env.Retry.Reason(r))
}

// judge holds every answer to the verdict the step called for. A rival
// may be accepted only while no copy of the race was: one accepted beside
// another is a double spend in a mempool.
func (k *Attack) judge(at sched.Time, p *wire.Pending, r wire.SubmitResult, out submit.Outcome) error {
	sp, env := &k.core.spec, &k.core.env
	node := oracle.Node{Impl: "node"}
	if r.Node != nil {
		node = *r.Node
	}
	want := p.Want.Expected()
	if p.TTL <= env.Chain.Tip().Slot+1+sp.BlockEvery {
		want = env.Judge.WithClasses(want, submit.OutsideValidityInterval)
	}
	a := oracle.Attempt{Seq: env.Journal.Seq(), Tx: p.Cbor, ID: chain.TxID(p.Tx), Expected: &want, Answers: []oracle.Answer{{Node: node, Outcome: out}}}
	fs := oracle.Judge(a, sp.Run)
	taken := out.Verdict == submit.Accepted
	for i, ans := range r.Rivals {
		if i >= len(p.Rivals) {
			break
		}
		rw := mistake.Expect(0, true)
		rw.Accept = !taken
		ra := oracle.Attempt{Seq: env.Journal.Seq(), Tx: p.Rivals[i].Tx, ID: chain.TxID(p.Rivals[i].ID), Expected: &rw,
			Answers: []oracle.Answer{{Node: node, Outcome: env.Judge.AnswerOutcome(ans)}}}
		fs = append(fs, oracle.Judge(ra, sp.Run)...)
		taken = taken || ans.Verdict == wire.VerdictAccepted
	}
	return env.Judge.Report(at, fs)
}
