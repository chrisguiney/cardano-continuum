package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/attack"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/floor"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/journal"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/judge"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pace"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Errors returned by Handle.
var (
	// ErrRollbackBelowConfirmed: the chain rolled back past the K-deep point
	// durable state came from. Design A cannot unwind it, so the engine stops
	// instead of silently resetting.
	ErrRollbackBelowConfirmed = window.ErrRollbackBelowConfirmed
	// ErrUnknownPoint: a rollback names a point the engine never applied.
	ErrUnknownPoint = window.ErrUnknownPoint
	// ErrTimeBackwards: a Tick older than the engine's clock.
	ErrTimeBackwards = clock.ErrBackwards
	// ErrUnknownInput: an input kind the engine does not handle.
	ErrUnknownInput = errors.New("engine: unknown input kind")
	// ErrUnknownVerdict: a SubmitResult whose verdict is missing or unknown.
	ErrUnknownVerdict = pay.ErrUnknownVerdict
	// ErrBadPoint: a block point that is neither the origin nor a 32-byte
	// hash.
	ErrBadPoint = wire.ErrBadPoint
)

// Payment sizing. A payment sends a drawn fraction of the actor's spendable
// balance, never less than the payee output's min-UTxO, and needs
// feeHeadroom on top so the builder can pay the fee and return change.
// minPayment is what an adversary's probe payments send.
const (
	minPayment   = 2_000_000
	censusStream = "census"
)

// Engine is the pure core. It is not safe for concurrent use: the loop is one
// goroutine by design.
//
// Which outputs exist, which are reserved and when a payment is settled or
// dead is the planner's ledger, reached only through pay and fed the same
// chain events as the engine's own window. The engine keeps only what the
// ledger does not know: each payment's actor, bytes and TTL, and the fees
// and foreign outputs the supply check needs.
type Engine struct {
	cfg     Config
	builder *build.Builder
	sel     Selector

	// fc is the faucet's refill round; it does nothing unless cfg.Faucet is
	// positive.
	fc  *faucet
	clk clock.Clock
	jr  journal.Journal
	co  *cohort.Cohort

	// win folds the same blocks as pay's ledger.
	win *window.Window

	pay   *pay.Pay
	retry *pay.Retry
	pc    *pace.Pace
	in    *intent.Intent

	fl *floor.Floor

	// st is the operator's controls and the upgrade waves.
	st *steering

	// jd is the oracle bridge: mistakes, verdicts, findings and monitors.
	jd *judge.Judge

	// adv is the adversaries: policies, cost accounts, contested outputs.
	adv *attack.Attack
}

// New returns a fresh engine at time zero holding the genesis population and
// funds.
func New(cfg Config) (*Engine, error) {
	e, err := newEmpty(cfg)
	if err != nil {
		return nil, err
	}
	funds, err := cfg.Genesis()
	if err != nil {
		return nil, err
	}
	var owners []population.ActorID
	for _, co := range cfg.Cohorts {
		p, _ := cfg.Profiles.Get(co.Profile)
		for range co.Count {
			owners = append(owners, e.co.Life.Spawn(p, 0).ID)
		}
	}
	if cfg.Faucet > 0 {
		owners = []population.ActorID{faucetActor}
		e.fc.Start()
	}
	var genesis []plan.Utxo
	for i, f := range funds {
		addr, err := f.Address.Bytes()
		if err != nil {
			return nil, err
		}
		// Genesis outputs are (Blake2b-256 of the address, 0), as in
		// Shelley genesis and the fake chain.
		genesis = append(genesis, plan.Utxo{Ref: plan.Ref{Tx: blake2b256(addr)}, Owner: owners[i], Lovelace: f.Lovelace})
	}
	if err = e.pay.Genesis(e.ledgerConfig(), genesis); err != nil {
		return nil, fmt.Errorf("engine: ledger: %w", err)
	}
	var gs chain.State
	for i, f := range funds {
		gs.Utxos = append(gs.Utxos, window.Utxo(f.Address, Ref{Tx: TxID(genesis[i].Ref.Tx)}, f.Lovelace))
	}
	if err := e.co.Ring.Gen.Seed(&gs, window.Utxo); err != nil {
		return nil, err
	}
	for _, g := range e.co.Ring.Gen.Pools {
		// A genesis pool is registered by the genesis itself.
		if err := e.co.Pop().Apply(population.Transition{ID: g.Actor, To: population.Active, Cause: population.Confirmed{Tx: "genesis"}}); err != nil {
			return nil, fmt.Errorf("engine: genesis pool actor %d: %w", g.Actor, err)
		}
	}
	if err = e.win.Open(gs, window.Saved{}); err != nil {
		return nil, fmt.Errorf("engine: view: %w", err)
	}
	e.co.Births.Start(0)
	e.in.Begin(0)
	e.jr.Drop() // genesis spawns are configuration, not decisions
	return e, nil
}

func newEmpty(cfg Config) (*Engine, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := cfg.checkAdversaries(); err != nil {
		return nil, err
	}
	ks, err := keys.New(cfg.Seed.Derive("keys"), cfg.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: keystore: %w", err)
	}
	sel := cfg.Selector
	if sel == nil {
		sel = LargestFirst{}
	}
	var diag submit.Diagnoser = submit.Perfect{}
	if cfg.Diagnoser != nil {
		diag = cfg.Diagnoser
	} else if cfg.Mistakes {
		diag = nil // each actor reads its rejections at its competence
	}
	e := &Engine{
		cfg: cfg, builder: build.New(cfg.Build), sel: sel,
		pay: pay.New(),
		win: window.New(chain.Config{K: cfg.K, Slots: cfg.Slots, NOpt: cfg.NOpt, GovLifetime: cfg.GovLifetime}),
	}
	e.retry = pay.NewRetry(pay.Spec{Policy: cfg.retry(), Diagnoser: diag}, e.pay,
		pay.Env{Actors: hooks{e}, Builder: hooks{e}, Journal: &e.jr, Sched: evsched{&e.clk}, Chain: e.win})
	if e.fl, err = floor.New(floor.Spec{Guard: cfg.floor(), Slots: cfg.Slots, SimulatedPods: cfg.SimulatedPods},
		floor.Env{Stake: floorHooks{e}, Book: e.pay, Census: floorHooks{e}, Journal: &e.jr, Clock: &e.clk}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	var genesis uint64
	funds, err := cfg.Genesis()
	if err != nil {
		return nil, err
	}
	for _, f := range funds {
		genesis += f.Lovelace
	}
	if err := e.win.Book().AddAddress(build.AlwaysSucceeds.Address(cfg.NetworkID), scriptActor); err != nil {
		return nil, fmt.Errorf("engine: book: %w", err)
	}
	rootKeys, err := keys.New(cfg.Seed, cfg.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	gpools, err := cfg.genesisPools(rootKeys)
	if err != nil {
		return nil, err
	}
	e.pc = pace.New(pace.Spec{Proposer: proposerName, Proposals: cfg.Network.Proposals, Population: e.cfg.population, Slots: cfg.Slots, BlockEvery: cfg.BlockEvery},
		pace.Env{Sched: evsched{&e.clk}, Knobs: hooks{e}, Chain: e.win, Flight: hooks{e}, Agenda: hooks{e}})
	e.co = cohort.New(cohort.Spec{Seed: cfg.Seed, Census: cfg.Seed.Derive(censusStream), Slots: cfg.Slots, Proposer: proposerName,
		ActorKeys: ks, RootKeys: rootKeys, Pools: gpools, PoolStake: cfg.GenesisPoolStake},
		cohort.Env{Sched: evsched{&e.clk}, Emit: &e.jr, Chain: e.win, Att: hooks{e}, Pace: hooks{e}})
	e.jd = judge.New(judgeSpec(e, genesis), judgeEnv(e))
	e.in = intent.New(intentSpec(e), intentEnv(e))
	e.adv = attack.New(attackSpec(e), attackEnv(e))
	e.st = newSteering(steerSpec{Seed: cfg.Seed, Slots: cfg.Slots},
		steerEnv{Scenarios: scenarioRoute{e}, Pools: steerRing{e}, Clock: evsched{&e.clk}, Journal: &e.jr})
	if err := e.co.Ring.Gen.Book(); err != nil {
		return nil, err
	}
	for _, n := range cfg.Profiles.Names() {
		p, _ := cfg.Profiles.Get(n)
		if p.Hazards.Birth > 0 {
			e.co.Births.Add(p)
		}
	}
	if e.fc, err = newFaucetFor(e); err != nil {
		return nil, err
	}
	return e, nil
}

// Handle implements eventlog.Engine: it applies one input and returns the
// decisions it caused, in order.
func (e *Engine) Handle(ctx context.Context, in eventlog.Record) ([]eventlog.Record, error) {
	e.jr.Begin(in.Seq)
	e.jd.Begin()
	from := e.win.Confirmed()
	var err error
	switch in.Kind {
	case eventlog.KindTick:
		err = e.tick(ctx, in.At)
	case eventlog.KindBlockApplied:
		var b eventlog.BlockApplied
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode block: %w", err)
		}
		var c BlockContent
		if len(b.Content) > 0 {
			if err = eventlog.Decode(b.Content, &c); err != nil {
				return nil, fmt.Errorf("engine: decode block content: %w", err)
			}
		}
		if err = e.applyBlock(ctx, in.At, blockRec{Point: b.Point, Txs: c.Txs, Number: c.Number}); err == nil {
			e.jd.Block(b.Point, c.Txs)
			err = e.jd.Watch(in.At, from, nil, nil)
		}
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode rollback: %w", err)
		}
		if err = e.rollback(ctx, b.To); err == nil {
			err = e.jd.Watch(in.At, from, nil, nil)
		}
	case eventlog.KindControl:
		var c control
		if err = eventlog.Decode(in.Body, &c); err != nil {
			return nil, fmt.Errorf("engine: decode control: %w", err)
		}
		err = e.st.Control(ctx, in.At, c)
	case eventlog.KindPoolStatus:
		var s PoolStatus
		if err = eventlog.Decode(in.Body, &s); err != nil {
			return nil, fmt.Errorf("engine: decode pool status: %w", err)
		}
		err = e.poolStatus(ctx, in.At, s)
	case eventlog.KindMonitorSnapshot:
		var m MonitorSnapshot
		if err = eventlog.Decode(in.Body, &m); err != nil {
			return nil, fmt.Errorf("engine: decode monitor snapshot: %w", err)
		}
		err = e.jd.Snapshot(in.At, from, m)
	case eventlog.KindSubmitResult:
		var r SubmitResult
		if err = eventlog.Decode(in.Body, &r); err != nil {
			return nil, fmt.Errorf("engine: decode submit result: %w", err)
		}
		err = e.submitResult(ctx, in.At, r)
	default:
		err = fmt.Errorf("%w: %s", ErrUnknownInput, in.Kind)
	}
	if err != nil {
		e.jr.Drop()
		return nil, err
	}
	return e.jr.Take(), nil
}

// tick runs every scheduled event due at or before t, in the clock's order.
func (e *Engine) tick(ctx context.Context, t sched.Time) error {
	if err := e.clk.Open(t); err != nil {
		return err
	}
	for {
		at, batch, ok := e.clk.Next(t)
		if !ok {
			return nil
		}
		for _, ev := range batch {
			if err := e.run(ctx, at, ev); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) run(ctx context.Context, at sched.Time, ev clock.Event) error {
	switch ev.Kind {
	case clock.Birth:
		e.co.Births.Run(at, ev.Birth)
	case clock.Hazard:
		return e.co.Life.Hazard(at, ev.Actor)
	case clock.Act:
		return e.act(ctx, at, ev.Actor)
	case clock.Retry:
	case clock.Refill:
		return e.fc.Refill(ctx, at)
	case clock.Adopt:
		e.st.Adopt(at, ev.Intent)
	case clock.Deploy:
		return e.in.Deploy(at)
	}
	return nil
}

func (e *Engine) act(ctx context.Context, at sched.Time, id population.ActorID) error {
	a, _ := e.co.Get(id)
	st := e.co.Rec(id)
	if e.st.Paused() {
		e.pc.Next(a, subject(st), at)
		return nil
	}
	r := st.Draw(a, "act")
	var d agenda.Decision
	var gov build.ActionType
	switch {
	case st.Boot != "":
		d, gov = e.pc.Decide(a, subject(st), r)
	case st.FollowUp:
		d = agenda.Decide(a, r, view{e: e}, st.Policy, e.pc.FollowWeights(st.Prof))
	default:
		d = agenda.Decide(a, r, view{e: e}, st.Policy, e.pc.Weights(st.Prof))
	}
	e.jr.Emit(at, KindIntentChosen, id, d)
	if e.pc.Wait(a, subject(st), at, d) {
		return nil
	}
	e.pc.Owe(subject(st), d, r)
	switch {
	case d.Intent.Kind == agenda.Idle:
	case !e.fl.Check(at, d.Intent):
	case d.Intent.Kind == agenda.GoDark, d.Intent.Kind == agenda.GoLive:
		e.fl.Take(at, d.Intent)
	case attack.IsKind(d.Intent.Kind):
		if err := e.adv.Strike(ctx, at, a, d.Intent, r); err != nil {
			return err
		}
	default:
		if err := e.intend(ctx, at, a, st, d.Intent, gov, r); err != nil {
			return err
		}
	}
	e.pc.Next(a, subject(st), at)
	return nil
}

// submitResult feeds the node's answer to the payment's retry machine.
// Whether a rejection writes the attempt off is the machine's call, from the
// answer alone: a rejection of a resend (which the attempt's own arrival can
// cause) proves nothing unless it names a rule that reads the transaction
// only (RetryRebuildOnOwnSpendBug in models/specs/submit-retry).
func (e *Engine) submitResult(ctx context.Context, at sched.Time, r SubmitResult) error {
	p := e.pay.Get(r.Intent)
	if p == nil || (!p.Awaiting && r.Verdict != VerdictDropped) {
		return nil
	}
	if r.Verdict == VerdictDropped {
		return e.retry.Dropped(ctx, at, r)
	}
	if p.Attack != "" {
		return e.adv.Result(ctx, at, p, r)
	}
	if p.Refill {
		out, err := pay.Outcome(r)
		if err != nil {
			return err
		}
		if err := e.jd.Answer(at, p, r, out); err != nil {
			return err
		}
		return e.fc.Result(ctx, at, p, r)
	}
	out, err := pay.Outcome(r)
	if err != nil {
		return err
	}
	if r.Resend {
		// The process that died before logging may have sent these bytes.
		p.M.Sent = true
	}
	if err := e.jd.Answer(at, p, r, out); err != nil {
		return err
	}
	return e.retry.Answer(ctx, at, p, out, pay.Reason(r))
}

// applyBlock pushes a block onto the volatile window and the ledger's view.
// A block with K successors is settled: its fees and foreign outputs count,
// and the ledger reports the payments it settled or killed.
func (e *Engine) applyBlock(ctx context.Context, at sched.Time, b blockRec) error {
	lb, vb, err := e.win.Blocks(b, e.co)
	if err != nil {
		return err
	}
	res, err := e.pay.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
	if err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	// The ledger accepted the block, so it extends the tip the view shares.
	if err := e.win.Push(b, vb); err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	if err := e.retry.InBlock(ctx, at, b); err != nil {
		return err
	}
	was := e.cfg.Slots.EpochOfSlot(e.win.Confirmed().Slot)
	e.win.Confirm(e.co)
	if ep := e.cfg.Slots.EpochOfSlot(e.win.Confirmed().Slot); ep > was {
		if err := e.co.Life.EpochApplied(at, ep); err != nil {
			return err
		}
	}
	for _, tx := range res.Settled {
		p, err := e.retry.Settle(ctx, at, tx)
		if err != nil {
			return err
		}
		if p == nil {
			continue
		}
		// Every attempt carries a WrongAddress mistake (it has no fix), so
		// the current one's loss is the settled one's.
		e.jd.AddLost(p.Lost)
		if err := e.settle(at, p, TxID(tx)); err != nil {
			return err
		}
	}
	if err := e.retry.Release(ctx, at, res.Dead, ""); err != nil {
		return err
	}
	return e.recover(ctx, at)
}

// rollback unwinds the volatile window and the ledger's view to p. It checks
// before it changes anything, so a refused rollback leaves the engine as it
// was. The ledger keeps every reservation: a rolled-back payment may be
// included again.
func (e *Engine) rollback(ctx context.Context, p eventlog.ChainPoint) error {
	c, err := e.win.Check(p)
	if err != nil {
		return err
	}
	if _, err := e.pay.Advance(ctx, chain.Event{Kind: chain.Backward, Point: c.To()}); err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	gone, err := e.win.Rollback(c)
	if err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	return e.retry.LeftView(ctx, e.clk.Now(), gone)
}
