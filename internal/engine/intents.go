package engine

import (
	"context"
	"math/rand/v2"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/attack"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// starter starts the engine's own intents: an agenda's choice, an inject
// scenario's and a stake-floor recovery move all reserve, build and emit
// through start, in one order.
type starter struct{ e *Engine }

// intend turns the chosen intent into a pending record and starts it. A
// kind with no transaction (idle, go_dark, go_live) never reaches it.
func (s starter) intend(ctx context.Context, at sched.Time, a population.Actor, st *cohort.Actor, in agenda.Intent, gov build.ActionType, r *rand.Rand) error {
	e := s.e
	p, why := e.in.Prepare(at, a, in, r)
	if p != nil {
		p.GovType = string(gov)
	}
	if why == "" {
		var err error
		if why, err = s.start(ctx, at, p); err != nil {
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
func (s starter) start(ctx context.Context, at sched.Time, p *wire.Pending) (string, error) {
	e := s.e
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
	return "", e.adv.React(ctx, at, p, built.Fee)
}

// move starts a recovery move: the engine's own delegation of a stranded
// wallet onto a forging pool. It needs no agenda and is never guarded.
func (s starter) move(ctx context.Context, at sched.Time, mv policy.Intent) error {
	e := s.e
	a, ok := e.co.Get(mv.Wallet)
	if !ok {
		return nil
	}
	p := &wire.Pending{Actor: uint64(a.ID), Args: wire.Args{Kind: string(profile.IntentDelegate), To: uint64(mv.Pool), Register: !e.win.View().StakeRegistered(a.ID)}}
	why, err := s.start(ctx, at, p)
	if err != nil {
		return err
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: "recovery_move", Why: why})
	}
	return nil
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
			dustKind: {Headroom: feeHeadroom, Build: attack.OneShot},
		},
	}
}

// intentEnv is the rest of the engine as the intent component reaches it.
func intentEnv(e *Engine) intent.Env {
	return intent.Env{
		Roster: roster{e.co}, Ring: e.co.Ring, Chain: e.win, Flight: e.pay, Clock: evsched{&e.clk}, Journal: &e.jr,
		Judge: e.jd, Spend: ihooks{e}, Market: e.co.Market,
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

// ihooks is the adversaries and the planner ledger as the intent and
// attack components reach them.
type ihooks struct{ e *Engine }

// Utxos implements intent.Spend. It turns coins into the outputs the builder spends. The ledger
// knows only lovelace; the view holds each output as the block carried it,
// so an input holding tokens hands them to the builder, which returns them
// in the change. Spending a token as plain lovelace leaves the
// transaction's value unbalanced (ValueNotConservedUTxO). A coin the view
// lacks is the change of an attempt not yet in a block, built from
// lovelace-only inputs.
func (h ihooks) Utxos(w keys.Wallet, coins []Coin) []common.Utxo {
	e := h.e
	out := make([]common.Utxo, len(coins))
	for i, c := range coins {
		if u, ok := e.win.View().Utxo(chain.Ref{Tx: chain.TxID(c.Ref.Tx), Index: c.Ref.Index}); ok {
			out[i] = u
			continue
		}
		out[i] = window.Utxo(w.Address(), c.Ref, c.Lovelace)
	}
	return out
}

// Adversary implements intent.Spend.
func (h ihooks) Adversary(id population.ActorID) bool { return h.e.adv.Adversary(id) }

// Settled implements attack.Spend.
func (h ihooks) Settled() []plan.Utxo { return h.e.pay.Reads().Settled() }

// Holder implements attack.Spend.
func (h ihooks) Holder(ref plan.Ref) (plan.IntentID, bool) { return h.e.pay.Reads().Holder(ref) }
