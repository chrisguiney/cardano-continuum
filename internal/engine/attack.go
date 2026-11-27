package engine

import (
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/attack"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/judge"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// The kinds the attack component names: an adversary's ordinary intent
// (in Submitted.Attack) and a dust flood's spec key.
const (
	coverTraffic = attack.Cover
	dustKind     = attack.DustKind
)

// attackSpec is what the attack component is built from.
func attackSpec(e *Engine) attack.Spec {
	cfg := &e.cfg
	return attack.Spec{
		Build: cfg.Build, Builder: e.builder, Selector: e.sel, Slots: cfg.Slots, BlockEvery: cfg.BlockEvery,
		NetworkID: cfg.NetworkID, Run: cfg.Run, Unlock: unlockKind, Again: intent.Again(unlockKind),
		FeeHeadroom: feeHeadroom, ScriptHeadroom: scriptHeadroom, MinPayment: minPayment, Faucet: faucetActor,
		Datum: intent.ActorDatum, Key: intent.DatumKey, Unit: intent.UnitDatum(),
	}
}

// attackEnv is the rest of the engine as the attack component reaches it.
func attackEnv(e *Engine) attack.Env {
	return attack.Env{
		Roster: roster{e.co}, Ledger: e.pay, Flight: e.pay, Retry: answers{e.retry}, Judge: rulings{e.jd, e.cfg.Build},
		Maker: e.in, Spend: ihooks{e}, Journal: &e.jr, Pace: e.pc, Chain: e.win, Clock: &e.clk, View: view{e: e},
	}
}

// answers is the retry bridge as an attack's one-shot answer reaches it.
type answers struct{ *pay.Retry }

// Outcome implements attack.Retry.
func (answers) Outcome(r SubmitResult) (submit.Outcome, error) { return pay.Outcome(r) }

// Refusable implements attack.Retry.
func (answers) Refusable(v wire.Verdict, resend bool) bool { return pay.Refusable(v, resend) }

// Reason implements attack.Retry.
func (answers) Reason(r SubmitResult) string { return pay.Reason(r) }

// rulings is the oracle bridge as an attack's verdicts reach it, on the
// run's parameters.
type rulings struct {
	*judge.Judge
	pp build.Params
}

// Expect implements attack.Judge.
func (v rulings) Expect(ks mistake.KindSet, spent bool, again []submit.Class) mistake.Expected {
	return judge.Expect(v.pp, ks, spent, again)
}

// WithClasses implements attack.Judge.
func (rulings) WithClasses(e mistake.Expected, cs ...submit.Class) mistake.Expected {
	return judge.WithClasses(e, cs...)
}

// AnswerOutcome implements attack.Judge.
func (rulings) AnswerOutcome(a wire.Answer) submit.Outcome { return judge.AnswerOutcome(a) }
