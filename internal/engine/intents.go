package engine

import (
	"context"
	"math/rand/v2"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// intend turns the chosen intent into a pending record and starts it. A
// kind with no transaction (idle, go_dark, go_live) never reaches it.
func (e *Engine) intend(ctx context.Context, at sched.Time, a population.Actor, st *cohort.Actor, in agenda.Intent, gov build.ActionType, r *rand.Rand) error {
	p, why := e.in.Prepare(at, a, in, r)
	if p != nil {
		p.GovType = string(gov)
	}
	if why == "" {
		var err error
		if why, err = e.start(ctx, at, p); err != nil {
			return err
		}
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: string(in.Kind), Why: why})
	}
	return nil
}

// start builds and reserves the first attempt of p and starts its retry
// machine. It returns why the intent was skipped, if it was.
func (e *Engine) start(ctx context.Context, at sched.Time, p *wire.Pending) (string, error) {
	id := e.pay.Next()
	p.ID = id
	built, inputs, ttl, why, err := e.in.Attempt(ctx, at, p)
	if why != "" || err != nil {
		return why, err
	}
	if err := e.pay.Reserve(ctx, id, population.ActorID(p.Actor), TxID(built.ID), inputs, ttl); err != nil {
		return "reserve: " + err.Error(), nil
	}
	e.pay.Claim(id)
	p.Tx, p.TTL, p.Cbor, p.Awaiting, p.Txs = TxID(built.ID), ttl, built.Tx, true, []TxID{TxID(built.ID)}
	if err := e.retry.Begin(p); err != nil {
		return "", err
	}
	e.pay.Add(p)
	if p.Kind == "" && !p.Refill {
		if st := e.co.Rec(population.ActorID(p.Actor)); st != nil {
			st.Remember(population.ActorID(p.To))
		}
	}
	e.pc.Excite(at)
	e.in.Submitted(at, p, built, inputs)
	return "", e.react(ctx, at, p, built.Fee)
}

// settle accounts for a settled attempt of intent p: the deposit it paid
// or refunded, a locked output it freed, and the lifecycle edge it causes.
func (e *Engine) settle(at sched.Time, p *wire.Pending, tx TxID) error {
	if out := e.pay.Deposit(e.in.Deposit(p)); out > 0 {
		e.win.Reclaim(out)
	}
	if p.Kind == unlockKind {
		// The lock's script output counted as foreign when it settled.
		e.win.Reclaim(p.Amount)
	}
	return e.co.Life.Settled(at, population.ActorID(p.Actor), profile.IntentKind(p.Kind), tx)
}

// Intent kinds that are not agenda kinds, and the amounts the engine shares
// with the intent component.
const (
	lockKind       = intent.LockKind
	unlockKind     = intent.UnlockKind
	scriptHeadroom = intent.ScriptHeadroom
	feeHeadroom    = intent.FeeHeadroom
)

// scriptActor owns the always-succeeds address in the view's book, so the
// view tracks locked outputs. It is above maxActors and below the faucet.
const scriptActor = cohort.Script

// intentSpec is what the intent component is built from. The kinds the
// steer and attack components build go through it as extras.
func intentSpec(e *Engine) intent.Spec {
	cfg := &e.cfg
	cp, _ := cfg.contractParams()
	return intent.Spec{
		Build: cfg.Build, Builder: e.builder, Selector: e.sel, Slots: cfg.Slots, TTL: cfg.TTL, Horizon: cfg.Horizon,
		NetworkID: cfg.NetworkID, Seed: cfg.Seed, Pool: cfg.population, Contract: cp, DeployRate: deployRate(cfg),
		Script: scriptActor, Cover: coverTraffic,
		Extra: map[string]intent.Extra{
			hotKind: {Headroom: feeHeadroom, Build: func(ctx context.Context, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
				return buildHot(ctx, e, w, sp, p)
			}},
			dustKind: {Headroom: feeHeadroom, Build: oneShotBuild},
		},
	}
}

// intentEnv is the rest of the engine as the intent component reaches it.
func intentEnv(e *Engine) intent.Env {
	return intent.Env{
		Roster: roster{e.co}, Ring: e.co.Ring, Chain: e.win, Flight: e.pay, Clock: evsched{&e.clk}, Journal: &e.jr,
		Judge: e.jd, Spend: ihooks{e},
	}
}

// roster is the cohort as the intent reads its actors.
type roster struct{ *cohort.Cohort }

// Profile implements intent.Roster.
func (r roster) Profile(id population.ActorID) *profile.Profile {
	st := r.Rec(id)
	if st == nil {
		return nil
	}
	return &st.Prof
}

// Paid implements intent.Roster.
func (r roster) Paid(id population.ActorID) []population.ActorID { return r.Rec(id).Paid }

// ihooks is the adversaries and the planner ledger as the intent component
// reaches them.
type ihooks struct{ e *Engine }

// Utxos implements intent.Spend.
func (h ihooks) Utxos(w keys.Wallet, coins []Coin) []common.Utxo { return h.e.ledgerUtxos(w, coins) }

// Adversary implements intent.Spend.
func (h ihooks) Adversary(id population.ActorID) bool { return h.e.isAdversary(id) }
