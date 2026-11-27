package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	evclock "github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/journal"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
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
	ErrTimeBackwards = evclock.ErrBackwards
	// ErrUnknownInput: an input kind the engine does not handle.
	ErrUnknownInput = errors.New("engine: unknown input kind")
	// ErrUnknownVerdict: a SubmitResult whose verdict is missing or unknown.
	ErrUnknownVerdict = errors.New("engine: submit result has no known verdict")
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
	feeHeadroom  = 1_000_000
	minFraction  = 0.05
	fracSpread   = 0.25
	censusStream = "census"
)

// Engine is the pure core. It is not safe for concurrent use: the loop is one
// goroutine by design.
//
// Which outputs exist, which are reserved and when a payment is settled or
// dead is the planner's ledger (led), fed the same chain events as the
// engine's own window. The engine keeps only what the ledger does not know:
// each payment's actor, bytes and TTL, and the fees and foreign outputs the
// supply check needs.
type Engine struct {
	cfg     Config
	builder *build.Builder
	sel     Selector
	retry   submit.Policy
	diag    submit.Diagnoser
	// faucet is set only when cfg.Faucet is positive.
	faucet   keys.Wallet
	refillAt sched.Time

	clk evclock.Clock
	jr  journal.Journal
	co  *cohort.Cohort

	// win folds the same blocks as led.
	win *window.Window

	led      *plan.Ledger
	pending  map[uint64]*pendingRec
	byTx     map[TxID]uint64
	awaiting []uint64 // ascending intent IDs without a node answer
	nextID   uint64
	// deposits is the lovelace the engine's settled certificates and
	// proposals hold in the ledger's deposit pot.
	deposits uint64
	pc       pace
	// contracts is how many contracts are deployed (contract 0 from
	// genesis); deployAt is the next deployment, deployN its draw.
	contracts uint64
	deployAt  sched.Time
	deployN   uint64

	guard policy.Guard
	// dark is the pool plan: pools the engine wants not forging. down are
	// pools whose pod is observed not running, repairs those a repair was
	// asked for since. stalled is set by a stall incident.
	dark, down, repairs map[population.ActorID]bool
	stalled             bool

	// Controls: paused stops the agendas; rateK multiplies every actor's
	// rate (zero is 1); weights override the profiles' intent weights.
	paused  bool
	rateK   uint32
	weights map[profile.IntentKind]float64
	// waves counts upgrade waves; adoptions are their queued image
	// changes and images the plan they built, by pool actor.
	waves     uint64
	adoptions []adoption
	images    map[population.ActorID]string
	// findings are the oracle findings logged so far, monitors the
	// invariant checks, genesis the lovelace value conservation counts
	// from and lost what settled payments sent to wrong addresses.
	findings *oracle.Set
	monitors []oracle.Monitor
	genesis  uint64
	lost     uint64
	// budget is the block the current input applied, for the block-budget
	// monitor.
	budget *oracle.BlockBudget

	// advs are the adversaries' attack policies and costs their cost
	// account; contested are the shared outputs an adversary tried to
	// spend, so an honest spend of one may meet "inputs spent". owners
	// maps an actor's lock datum to the actor, derived on demand.
	advs      map[population.ActorID]adversary.Policy
	costs     *adversary.Ledger
	contested map[Ref]bool
	owners    map[string]population.ActorID
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
		e.refillAt = 0
		e.clk.Push(0, evclock.Event{Kind: evclock.Refill})
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
	if e.led, err = plan.New(e.ledgerConfig(), genesis); err != nil {
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
	if e.deployRate() > 0 {
		e.scheduleDeploy(0)
	}
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
	}
	e := &Engine{
		cfg: cfg, builder: build.New(cfg.Build), sel: sel,
		retry: cfg.retry(), diag: diag,
		pending: map[uint64]*pendingRec{}, byTx: map[TxID]uint64{},
		win:  window.New(chain.Config{K: cfg.K, Slots: cfg.Slots, NOpt: cfg.NOpt, GovLifetime: cfg.GovLifetime}),
		dark: map[population.ActorID]bool{}, down: map[population.ActorID]bool{}, repairs: map[population.ActorID]bool{},
		images: map[population.ActorID]string{}, contracts: 1, deployAt: population.Never,
		advs: map[population.ActorID]adversary.Policy{}, costs: adversary.NewLedger(), contested: map[Ref]bool{},
	}
	if e.guard, err = policy.New(cfg.floor()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	e.findings, e.monitors = oracle.NewSet(), oracle.Monitors(cfg.K, e.guard)
	funds, err := cfg.Genesis()
	if err != nil {
		return nil, err
	}
	for _, f := range funds {
		e.genesis += f.Lovelace
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
	e.pc.e = e
	e.co = cohort.New(cohort.Spec{Seed: cfg.Seed, Census: cfg.Seed.Derive(censusStream), Slots: cfg.Slots, Proposer: proposerName,
		ActorKeys: ks, RootKeys: rootKeys, Pools: gpools, PoolStake: cfg.GenesisPoolStake},
		cohort.Env{Sched: evsched{&e.clk}, Emit: &e.jr, Chain: e.win, Att: hooks{e}, Pace: hooks{e}})
	if err := e.co.Ring.Gen.Book(); err != nil {
		return nil, err
	}
	for _, n := range cfg.Profiles.Names() {
		p, _ := cfg.Profiles.Get(n)
		if p.Hazards.Birth > 0 {
			e.co.Births.Add(p)
		}
	}
	if cfg.Faucet > 0 {
		if e.faucet, err = faucetWallet(&cfg); err != nil {
			return nil, err
		}
		if err := e.co.BindFaucet(e.faucet); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Handle implements eventlog.Engine: it applies one input and returns the
// decisions it caused, in order.
func (e *Engine) Handle(ctx context.Context, in eventlog.Record) ([]eventlog.Record, error) {
	e.jr.Begin(in.Seq)
	e.budget = nil
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
			e.budget = e.blockBudget(b.Point, c.Txs)
			err = e.watch(in.At, from, nil, nil)
		}
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode rollback: %w", err)
		}
		if err = e.rollback(ctx, b.To); err == nil {
			err = e.watch(in.At, from, nil, nil)
		}
	case eventlog.KindControl:
		var c control
		if err = eventlog.Decode(in.Body, &c); err != nil {
			return nil, fmt.Errorf("engine: decode control: %w", err)
		}
		err = e.controlInput(ctx, in.At, c)
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
		err = e.monitorSnapshot(in.At, from, m)
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

func (e *Engine) run(ctx context.Context, at sched.Time, ev evclock.Event) error {
	switch ev.Kind {
	case evclock.Birth:
		e.co.Births.Run(at, ev.Birth)
	case evclock.Hazard:
		return e.co.Life.Hazard(at, ev.Actor)
	case evclock.Act:
		return e.act(ctx, at, ev.Actor)
	case evclock.Retry:
	case evclock.Refill:
		return e.refill(ctx, at)
	case evclock.Adopt:
		e.adopt(at, ev.Intent)
	case evclock.Deploy:
		return e.deploy(at)
	}
	return nil
}

func (e *Engine) act(ctx context.Context, at sched.Time, id population.ActorID) error {
	a, _ := e.co.Get(id)
	st := e.co.Rec(id)
	if e.paused {
		e.pc.nextAct(a, st, at)
		return nil
	}
	r := st.Draw(a, "act")
	var d agenda.Decision
	var gov build.ActionType
	switch {
	case st.Boot != "":
		d, gov = e.pc.decideClock(a, st, r)
	case st.FollowUp:
		d = agenda.Decide(a, r, view{e: e}, st.Policy, e.pc.followWeights(st.Prof))
	default:
		d = agenda.Decide(a, r, view{e: e}, st.Policy, e.pc.weightsOf(st.Prof))
	}
	e.jr.Emit(at, KindIntentChosen, id, d)
	if e.pc.wait(a, st, at, d) {
		return nil
	}
	e.pc.owe(st, d, r)
	switch {
	case d.Intent.Kind == agenda.Idle:
	case !e.checkFloor(at, d.Intent):
	case d.Intent.Kind == agenda.GoDark, d.Intent.Kind == agenda.GoLive:
		e.act1Pool(at, d.Intent)
	case isAttack(d.Intent.Kind):
		if err := e.attack(ctx, at, a, d.Intent, r); err != nil {
			return err
		}
	default:
		if err := e.intend(ctx, at, a, st, d.Intent, gov, r); err != nil {
			return err
		}
	}
	e.pc.nextAct(a, st, at)
	return nil
}

// paymentSize is the amount of a payment from an actor holding bal, never
// below floor, the min-UTxO of the payee's output (build.MinPayment).
// Without a profile distribution it is a drawn fraction of the balance.
// With one it is a draw from the distribution, capped so the payment leaves
// feeHeadroom: an oversize draw pays what the wallet can rather than being
// skipped, which would bias the sizes of the payments that do go out
// toward the body.
func paymentSize(a *profile.Amount, bal, floor uint64, r *rand.Rand) uint64 {
	if a == nil {
		return max(uint64(float64(bal)*(minFraction+fracSpread*r.Float64())), floor)
	}
	ceiling := float64(bal) - feeHeadroom
	if ceiling < float64(floor) {
		return floor
	}
	return max(uint64(min(a.Sample(r), ceiling)), floor)
}

func attempt(a population.ActorID, tx TxID, inputs []Ref, ttl uint64) plan.Attempt {
	ins := make([]plan.Ref, len(inputs))
	for i, r := range inputs {
		ins[i] = plan.Ref{Tx: plan.TxID(r.Tx), Index: r.Index}
	}
	return plan.Attempt{Tx: plan.TxID(tx), Actor: a, Inputs: ins, TTL: ttl}
}

func (e *Engine) addPending(p *pendingRec) {
	e.pending[p.ID] = p
	for _, tx := range p.Txs {
		e.byTx[tx] = p.ID
	}
	if p.Awaiting {
		e.awaiting = append(e.awaiting, p.ID)
	}
}

func (e *Engine) dropPending(id uint64) {
	if p, ok := e.pending[id]; ok {
		for _, tx := range p.Txs {
			delete(e.byTx, tx)
		}
		delete(e.pending, id)
	}
	e.stopAwaiting(id)
}

func (e *Engine) stopAwaiting(id uint64) {
	if i, ok := slices.BinarySearch(e.awaiting, id); ok {
		e.awaiting = slices.Delete(e.awaiting, i, i+1)
	}
}

// submitResult feeds the node's answer to the payment's retry machine.
// Whether a rejection writes the attempt off is the machine's call, from the
// answer alone: a rejection of a resend (which the attempt's own arrival can
// cause) proves nothing unless it names a rule that reads the transaction
// only (RetryRebuildOnOwnSpendBug in models/specs/submit-retry).
func (e *Engine) submitResult(ctx context.Context, at sched.Time, r SubmitResult) error {
	p, ok := e.pending[r.Intent]
	if !ok || (!p.Awaiting && r.Verdict != VerdictDropped) {
		return nil
	}
	if r.Verdict == VerdictDropped {
		return e.dropped(ctx, at, r)
	}
	if p.Attack != "" {
		return e.attackResult(ctx, at, p, r)
	}
	if p.Refill {
		out, err := outcomeOf(r)
		if err != nil {
			return err
		}
		if err := e.judge(at, p, r, out); err != nil {
			return err
		}
		return e.refillResult(ctx, at, p, r)
	}
	out, err := outcomeOf(r)
	if err != nil {
		return err
	}
	if r.Resend {
		// The process that died before logging may have sent these bytes.
		p.M.Sent = true
	}
	if err := e.judge(at, p, r, out); err != nil {
		return err
	}
	return e.apply(ctx, at, p, submit.Answered{Out: out}, reasonOf(r))
}

// release handles attempts the ledger wrote off. The current attempt of a
// live machine is Dead to it, and it builds again unless its patience is
// spent; a payment whose last unfinished attempt is dead and whose machine
// is done with it is released and forgotten. An empty why names the cause
// per payment: expiry or conflict.
func (e *Engine) release(ctx context.Context, at sched.Time, dead []plan.TxID, why string) error {
	for _, tx := range dead {
		id, ok := e.byTx[TxID(tx)]
		if !ok {
			continue
		}
		p := e.pending[id]
		e.forgetTx(TxID(tx))
		if err := e.dead(ctx, at, p, TxID(tx)); err != nil {
			return err
		}
		if e.pending[id] != p || len(p.Txs) > 0 || (!p.oneShot() && p.M.State != submit.GaveUp) {
			continue
		}
		w := why
		if w == "" {
			w = "conflict"
			if p.TTL <= e.win.Confirmed().Slot {
				w = "expired"
			}
		}
		e.dropPending(id)
		e.jr.End(id, TxID(tx), true)
		e.jr.Emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: id, Why: w})
	}
	return nil
}

// coins lists what the ledger offers actor a for intent id, sorted by Ref.
func (e *Engine) coins(a population.ActorID, id plan.IntentID) []Coin {
	us := e.led.Available(a, id, plan.Opts{})
	out := make([]Coin, len(us))
	for i, u := range us {
		out[i] = Coin{Ref: Ref{Tx: TxID(u.Ref.Tx), Index: u.Ref.Index}, Lovelace: u.Lovelace}
	}
	return out
}

func sumCoins(cs []Coin) uint64 {
	var sum uint64
	for _, c := range cs {
		sum += c.Lovelace
	}
	return sum
}

// applyBlock pushes a block onto the volatile window and the ledger's view.
// A block with K successors is settled: its fees and foreign outputs count,
// and the ledger reports the payments it settled or killed.
func (e *Engine) applyBlock(ctx context.Context, at sched.Time, b blockRec) error {
	lb, vb, err := e.win.Blocks(b, e.co)
	if err != nil {
		return err
	}
	res, err := e.led.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
	if err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	// The ledger accepted the block, so it extends the tip the view shares.
	if err := e.win.Push(b, vb); err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	if err := e.inBlock(ctx, at, b); err != nil {
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
		if id, ok := e.byTx[TxID(tx)]; ok {
			p := e.pending[id]
			if err := e.observe(ctx, at, p, submit.Settled{Tx: tx}); err != nil {
				return err
			}
			e.dropPending(id)
			// Every attempt carries a WrongAddress mistake (it has no
			// fix), so the current one's loss is the settled one's.
			e.lost += p.Lost
			e.jr.End(id, TxID(tx), false)
			e.jr.Emit(at, KindConfirmed, population.ActorID(p.Actor), Confirmed{Intent: id, ID: TxID(tx), Lost: p.Lost})
			if err := e.settle(at, p, TxID(tx)); err != nil {
				return err
			}
		}
	}
	if err := e.release(ctx, at, res.Dead, ""); err != nil {
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
	if _, err := e.led.Advance(ctx, chain.Event{Kind: chain.Backward, Point: c.To()}); err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	gone, err := e.win.Rollback(c)
	if err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	return e.leftView(ctx, e.clk.Now(), gone)
}
