package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Errors returned by Handle.
var (
	// ErrRollbackBelowConfirmed: the chain rolled back past the K-deep point
	// durable state came from. Design A cannot unwind it, so the engine stops
	// instead of silently resetting.
	ErrRollbackBelowConfirmed = errors.New("engine: rollback below the confirmed point")
	// ErrUnknownPoint: a rollback names a point the engine never applied.
	ErrUnknownPoint = errors.New("engine: rollback to an unknown point")
	// ErrTimeBackwards: a Tick older than the engine's clock.
	ErrTimeBackwards = errors.New("engine: tick moves time backwards")
	// ErrUnknownInput: an input kind the engine does not handle.
	ErrUnknownInput = errors.New("engine: unknown input kind")
	// ErrUnknownVerdict: a SubmitResult whose verdict is missing or unknown.
	ErrUnknownVerdict = errors.New("engine: submit result has no known verdict")
	// ErrBadPoint: a block point that is neither the origin nor a 32-byte
	// hash.
	ErrBadPoint = errors.New("engine: malformed block point")
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

type evKind uint8

// Event kinds in the order same-time events run.
const (
	evBirth evKind = iota + 1
	evHazard
	evAct
	// evRetry only wakes the loop at the end of a backoff; InFlight reads
	// the payment's NotBefore against the clock.
	evRetry
	// evRefill is the faucet's round.
	evRefill
	// evAdopt is one pool's adoption in an upgrade wave; intent indexes
	// the adoptions.
	evAdopt
	// evDeploy opens the next contract.
	evDeploy
)

type qev struct {
	kind   evKind
	actor  population.ActorID
	birth  int
	intent uint64
}

type actor struct {
	prof     profile.Profile
	policy   agenda.Policy
	rate     float64 // sum of the profile's base rates, before RateMult
	wallet   keys.Wallet
	hazardAt sched.Time
	hazardTo population.State
	hazardH  sched.Handle
	actAt    sched.Time
	actH     sched.Handle
	draws    uint64
	// cs are a population actor's intent clocks and boot the intent it
	// registers with (populations.go); boot is empty for every other actor.
	cs   []clock
	boot profile.IntentKind
	// booting is set while the pending wake was drawn at the boot rate:
	// it registers, and must not fire a fitted clock (a clock's first
	// event would come at the boot rate, far too soon).
	booting bool
	// excite is the self-excitation of the actor's wake-ups when its
	// profile has Hawkes parameters.
	excite sched.Excitation
	// followUp marks a pending wake that the excitation, not the base
	// rate, caused: a follow-up acts rather than idling.
	followUp bool
	// paid are the actor's payees, most recent last, at most maxPaid.
	paid []population.ActorID
}

type birth struct {
	prof profile.Profile
	at   sched.Time
	n    uint64
	h    sched.Handle
}

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
	keys    *keys.Store
	builder *build.Builder
	sel     Selector
	root    rng.Seed
	retry   submit.Policy
	diag    submit.Diagnoser
	// faucet is set only when cfg.Faucet is positive.
	faucet   keys.Wallet
	refillAt sched.Time

	census population.Census
	queue  sched.Queue
	now    sched.Time
	actors map[population.ActorID]*actor
	order  []population.ActorID // ascending
	births []*birth             // by profile name
	byAddr map[string]population.ActorID

	conf   eventlog.ChainPoint
	window []blockRec
	// view folds the same blocks as window and led; book names what it
	// tracks.
	view *chain.View
	book *chain.Book

	led      *plan.Ledger
	pending  map[uint64]*pendingRec
	byTx     map[TxID]uint64
	awaiting []uint64 // ascending intent IDs without a node answer
	nextID   uint64
	fees     uint64
	foreign  uint64
	// deposits is the lovelace the engine's settled certificates and
	// proposals hold in the ledger's deposit pot.
	deposits uint64
	market   market
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
	// rootKeys is the run seed's own keystore (genesis pools, faucet, committee);
	// gpools are the genesis pools.
	rootKeys *keys.Store
	gpools   []genesisPool
	// pools maps each pool actor to the pool it operates.
	pools map[population.ActorID][28]byte

	// findings are the oracle findings logged so far, monitors the
	// invariant checks, genesis the lovelace value conservation counts
	// from and lost what settled payments sent to wrong addresses. seq is
	// the input being handled and ends the intents it ended.
	findings *oracle.Set
	monitors []oracle.Monitor
	genesis  uint64
	lost     uint64
	seq      uint64
	ends     []oracle.End

	// advs are the adversaries' attack policies and costs their cost
	// account; contested are the shared outputs an adversary tried to
	// spend, so an honest spend of one may meet "inputs spent". owners
	// maps an actor's lock datum to the actor, derived on demand.
	advs      map[population.ActorID]adversary.Policy
	costs     *adversary.Ledger
	contested map[Ref]bool
	owners    map[string]population.ActorID

	out []eventlog.Record
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
			owners = append(owners, e.spawn(p, 0).ID)
		}
	}
	if cfg.Faucet > 0 {
		owners = []population.ActorID{faucetActor}
		e.refillAt = 0
		e.queue.Push(0, qev{kind: evRefill})
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
		gs.Utxos = append(gs.Utxos, ownedUtxo(f.Address, Ref{Tx: TxID(genesis[i].Ref.Tx)}, f.Lovelace))
	}
	if err := e.stakeGenesis(&gs); err != nil {
		return nil, err
	}
	for _, g := range e.gpools {
		// A genesis pool is registered by the genesis itself.
		if err := e.census.Apply(population.Transition{ID: g.Actor, To: population.Active, Cause: population.Confirmed{Tx: "genesis"}}); err != nil {
			return nil, fmt.Errorf("engine: genesis pool actor %d: %w", g.Actor, err)
		}
	}
	if e.view, err = chain.New(e.viewConfig(), gs); err != nil {
		return nil, fmt.Errorf("engine: view: %w", err)
	}
	for _, b := range e.births {
		e.scheduleBirth(b, 0)
	}
	if e.deployRate() > 0 {
		e.scheduleDeploy(0)
	}
	e.out = nil // genesis spawns are configuration, not decisions
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
		cfg: cfg, keys: ks, builder: build.New(cfg.Build), sel: sel, root: cfg.Seed.Derive(censusStream),
		retry: cfg.retry(), diag: diag,
		actors: map[population.ActorID]*actor{}, byAddr: map[string]population.ActorID{},
		pending: map[uint64]*pendingRec{}, byTx: map[TxID]uint64{}, book: chain.NewBook(),
		pools: map[population.ActorID][28]byte{},
		dark:  map[population.ActorID]bool{}, down: map[population.ActorID]bool{}, repairs: map[population.ActorID]bool{},
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
	if err := e.book.AddAddress(build.AlwaysSucceeds.Address(cfg.NetworkID), scriptActor); err != nil {
		return nil, fmt.Errorf("engine: book: %w", err)
	}
	if e.rootKeys, err = keys.New(cfg.Seed, cfg.NetworkID); err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	if e.gpools, err = cfg.genesisPools(e.rootKeys); err != nil {
		return nil, err
	}
	if err := e.bookGenesis(); err != nil {
		return nil, err
	}
	for _, n := range cfg.Profiles.Names() {
		p, _ := cfg.Profiles.Get(n)
		if p.Hazards.Birth > 0 {
			e.births = append(e.births, &birth{prof: p})
		}
	}
	if cfg.Faucet > 0 {
		if e.faucet, err = faucetWallet(&cfg); err != nil {
			return nil, err
		}
		addr, err := e.faucet.Address().Bytes()
		if err != nil {
			return nil, fmt.Errorf("engine: faucet address: %w", err)
		}
		e.byAddr[string(addr)] = faucetActor
		_ = e.book.AddAddress(e.faucet.Address(), faucetActor)
	}
	return e, nil
}

// Handle implements eventlog.Engine: it applies one input and returns the
// decisions it caused, in order.
func (e *Engine) Handle(ctx context.Context, in eventlog.Record) ([]eventlog.Record, error) {
	e.out, e.ends, e.seq = nil, nil, in.Seq
	from := e.conf
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
			err = e.watch(in.At, from, nil)
		}
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode rollback: %w", err)
		}
		if err = e.rollback(ctx, b.To); err == nil {
			err = e.watch(in.At, from, nil)
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
		e.out = nil
		return nil, err
	}
	out := e.out
	e.out = nil
	return out, nil
}

func (e *Engine) emit(at sched.Time, kind string, a population.ActorID, body any) {
	raw, err := eventlog.Encode(body)
	if err != nil {
		// Bodies are fixed structs of plain fields; encoding cannot fail.
		panic(fmt.Sprintf("engine: encode %s: %v", kind, err))
	}
	e.out = append(e.out, eventlog.Record{At: at, Class: eventlog.Decision, Kind: kind, Actor: eventlog.ActorID(a), Body: raw})
}

// tick runs every scheduled event due at or before t. Events due at the same
// time run sorted by (kind, actor, birth), not in push order, so a queue
// rebuilt from a snapshot runs them in the same order as the live one.
func (e *Engine) tick(ctx context.Context, t sched.Time) error {
	if t < e.now {
		return fmt.Errorf("%w: %d < %d", ErrTimeBackwards, t, e.now)
	}
	for {
		at, ok := e.queue.Peek()
		if !ok || at > t {
			break
		}
		var batch []qev
		for {
			next, ok := e.queue.Peek()
			if !ok || next != at {
				break
			}
			_, ev, _ := e.queue.Pop()
			batch = append(batch, ev.(qev))
		}
		slices.SortFunc(batch, func(a, b qev) int {
			return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.actor, b.actor), cmp.Compare(a.birth, b.birth))
		})
		e.now = at
		for _, ev := range batch {
			if err := e.run(ctx, at, ev); err != nil {
				return err
			}
		}
	}
	e.now = t
	return nil
}

func (e *Engine) run(ctx context.Context, at sched.Time, ev qev) error {
	switch ev.kind {
	case evBirth:
		b := e.births[ev.birth]
		a := e.spawn(b.prof, at)
		e.emit(at, KindSpawned, a.ID, Spawned{Profile: b.prof.Name})
		e.scheduleBirth(b, at)
	case evHazard:
		return e.hazard(at, ev.actor)
	case evAct:
		return e.act(ctx, at, ev.actor)
	case evRetry:
	case evRefill:
		return e.refill(ctx, at)
	case evAdopt:
		e.adopt(at, ev.intent)
	case evDeploy:
		return e.deploy(at)
	}
	return nil
}

func (e *Engine) scheduleBirth(b *birth, now sched.Time) {
	b.n++
	r := e.cfg.Seed.Derive("birth", b.prof.Name, strconv.FormatUint(b.n, 10)).Rand()
	b.at = population.NextBirth(r, b.prof, now)
	e.pushBirth(b)
}

func (e *Engine) pushBirth(b *birth) {
	if b.at == population.Never {
		return
	}
	idx := slices.Index(e.births, b)
	b.h = e.queue.Push(b.at, qev{kind: evBirth, birth: idx})
}

// spawn adds an actor and schedules its first hazard.
func (e *Engine) spawn(p profile.Profile, at sched.Time) population.Actor {
	a := e.census.Spawn(p, e.root, at)
	st := e.register(a, p)
	e.nextHazard(a, st, at)
	// Pools and DReps have no hazard clock to wake them: they act from
	// birth, registering first.
	if p.Kind != profile.Wallet {
		e.nextAct(a, st, at)
	}
	return a
}

// register builds the in-memory side of an actor: policy, wallet, address
// index. It schedules nothing.
func (e *Engine) register(a population.Actor, p profile.Profile) *actor {
	pol, err := agenda.For(p.Agenda)
	if err != nil {
		pol = nil // the actor scores nothing and stays idle
	}
	pol = e.arm(a.ID, p, pol)
	st := &actor{prof: p, policy: pol, rate: baseRate(p), wallet: e.keys.Wallet(keys.ActorID(a.ID)),
		hazardAt: population.Never, actAt: population.Never}
	st.cs, st.boot, _ = e.clocks(a, p)
	e.actors[a.ID] = st
	e.order = append(e.order, a.ID)
	if addr, err := st.wallet.Address().Bytes(); err == nil {
		e.byAddr[string(addr)] = a.ID
	}
	e.bookActor(a.ID, p, st)
	return st
}

// baseRate sums the base rates in sorted key order: float addition is not
// associative, so summing in map order would make the rate vary by run.
func baseRate(p profile.Profile) float64 {
	ks := make([]string, 0, len(p.Rates.Base))
	for k := range p.Rates.Base {
		ks = append(ks, string(k))
	}
	slices.Sort(ks)
	var sum float64
	for _, k := range ks {
		sum += p.Rates.Base[profile.IntentKind(k)]
	}
	return sum
}

// draw returns a fresh stream for the actor's next random decision. Each
// draw has its own derived stream, so the snapshot only needs the counter.
func (e *Engine) draw(a population.Actor, st *actor, purpose string) *rand.Rand {
	st.draws++
	return a.Stream(purpose, strconv.FormatUint(st.draws, 10))
}

func (e *Engine) nextHazard(a population.Actor, st *actor, now sched.Time) {
	st.hazardAt, st.hazardTo = population.NextHazard(a, st.prof, e.draw(a, st, "hazard"), now)
	if st.hazardAt != population.Never {
		st.hazardH = e.queue.Push(st.hazardAt, qev{kind: evHazard, actor: a.ID})
	}
}

func (e *Engine) hazard(at sched.Time, id population.ActorID) error {
	a, _ := e.census.Get(id)
	st := e.actors[id]
	from, to := a.State, st.hazardTo
	st.hazardAt = population.Never
	if err := e.census.Apply(population.Transition{ID: id, To: to, At: at, Cause: population.Timer{}}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", id, err)
	}
	e.emit(at, KindTransition, id, Transition{From: string(from), To: string(to)})
	a, _ = e.census.Get(id)
	switch {
	case to == population.Active:
		e.nextAct(a, st, at)
	case from == population.Active && st.actAt != population.Never:
		e.queue.Cancel(st.actH)
		st.actAt = population.Never
	}
	e.nextHazard(a, st, at)
	return nil
}

func (e *Engine) nextAct(a population.Actor, st *actor, now sched.Time) {
	if st.boot != "" {
		e.nextClock(a, st, now)
		return
	}
	rate := st.rate * a.Traits.RateMult * float64(max(e.rateK, 1))
	if rate <= 0 || st.policy == nil {
		st.actAt = population.Never
		return
	}
	var mod sched.Modulation
	if d := st.prof.Rates.Diurnal; d > 0 {
		mod = sched.Daily{Amplitude: d}
	}
	if h := st.prof.Rates.Hawkes; h.Alpha > 0 {
		// The base rate is cut so the excited process averages rate:
		// mean = mu / (1 - alpha/beta).
		mu := rate * (1 - h.Alpha/h.Beta)
		st.actAt, st.followUp = sched.NextExcited(e.draw(a, st, "arrival"), mu, h.Beta, st.excite, mod, now)
		st.actH = e.queue.Push(st.actAt, qev{kind: evAct, actor: a.ID})
		return
	}
	st.actAt = sched.NextArrival(e.draw(a, st, "arrival"), rate, mod, now)
	st.actH = e.queue.Push(st.actAt, qev{kind: evAct, actor: a.ID})
}

func (e *Engine) act(ctx context.Context, at sched.Time, id population.ActorID) error {
	a, _ := e.census.Get(id)
	st := e.actors[id]
	if e.paused {
		e.nextAct(a, st, at)
		return nil
	}
	r := e.draw(a, st, "act")
	var d agenda.Decision
	var gov build.ActionType
	switch {
	case st.boot != "":
		d, gov = e.decideClock(a, st, r)
	case st.followUp:
		// A burst is follow-up activity: the excited wake-up does
		// something. Excitation counts acts, so a burst of idle wake-ups
		// would leave transactions as spread out as without it.
		w := maps.Clone(e.weightsOf(st.prof))
		if w == nil {
			w = map[profile.IntentKind]float64{}
		}
		w[agenda.Idle] = 0
		d = agenda.Decide(a, r, view{e: e}, st.policy, w)
	default:
		d = agenda.Decide(a, r, view{e: e}, st.policy, e.weightsOf(st.prof))
	}
	if h := st.prof.Rates.Hawkes; h.Alpha > 0 && d.Intent.Kind != agenda.Idle {
		st.excite = st.excite.Excite(at, h.Alpha, h.Beta)
	}
	e.emit(at, KindIntentChosen, id, d)
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
	e.nextAct(a, st, at)
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
			if p.TTL <= e.conf.Slot {
				w = "expired"
			}
		}
		e.dropPending(id)
		e.ended(id, TxID(tx), true)
		e.emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: id, Why: w})
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
	lb, err := e.ledgerBlock(e.Tip(), b)
	if err != nil {
		return err
	}
	vb, err := viewBlock(e.Tip(), b)
	if err != nil {
		return err
	}
	res, err := e.led.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
	if err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	// The ledger accepted the block, so it extends the tip the view shares.
	if _, err := e.view.Apply(vb); err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	e.window = append(e.window, b)
	if err := e.inBlock(ctx, at, b); err != nil {
		return err
	}
	was := e.cfg.Slots.EpochOfSlot(e.conf.Slot)
	for uint64(len(e.window)) > e.cfg.K {
		e.confirm(e.window[0])
		e.window = slices.Delete(e.window, 0, 1)
	}
	if ep := e.cfg.Slots.EpochOfSlot(e.conf.Slot); ep > was {
		if err := e.epochApplied(at, ep); err != nil {
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
			e.ended(id, TxID(tx), false)
			e.emit(at, KindConfirmed, population.ActorID(p.Actor), Confirmed{Intent: id, ID: TxID(tx), Lost: p.Lost})
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

func (e *Engine) confirm(b blockRec) {
	for _, tx := range b.Txs {
		for _, o := range tx.Outputs {
			if _, ok := e.byAddr[string(o.Address)]; !ok {
				e.foreign += o.Lovelace
			}
		}
		e.fees += tx.Fee
	}
	e.conf = b.Point
}

// ledgerBlock is b as the ledger reads it. The digest has no previous hash;
// the follower delivers blocks in chain order, so it is the engine's tip.
// Only outputs to the population are passed: the ledger tracks no others.
func (e *Engine) ledgerBlock(prev eventlog.ChainPoint, b blockRec) (chain.Block, error) {
	p, ok := chain.PointFromLog(b.Point)
	if !ok {
		return chain.Block{}, fmt.Errorf("%w: slot %d, hash of %d bytes", ErrBadPoint, b.Point.Slot, len(b.Point.Hash))
	}
	pp, _ := chain.PointFromLog(prev)
	out := chain.Block{Point: p, Prev: pp.Hash}
	for _, tx := range b.Txs {
		t := chain.Tx{Hash: chain.TxID(tx.ID)}
		for _, r := range tx.Inputs {
			t.Spends = append(t.Spends, chain.Ref{Tx: chain.TxID(r.Tx), Index: r.Index})
		}
		for _, o := range tx.Outputs {
			owner, ok := e.byAddr[string(o.Address)]
			if !ok {
				continue
			}
			t.Creates = append(t.Creates, ownedUtxo(e.walletOf(owner).Address(), Ref{Tx: tx.ID, Index: o.Index}, o.Lovelace))
		}
		out.Txs = append(out.Txs, t)
	}
	return out, nil
}

// rollback unwinds the volatile window and the ledger's view to p. It checks
// before it changes anything, so a refused rollback leaves the engine as it
// was. The ledger keeps every reservation: a rolled-back payment may be
// included again.
func (e *Engine) rollback(ctx context.Context, p eventlog.ChainPoint) error {
	keep := -1
	if samePoint(p, e.conf) {
		keep = 0
	}
	for i := len(e.window) - 1; i >= 0 && keep < 0; i-- {
		if samePoint(e.window[i].Point, p) {
			keep = i + 1
		}
	}
	if keep < 0 {
		if p.Slot < e.conf.Slot {
			return fmt.Errorf("%w: slot %d, confirmed slot %d", ErrRollbackBelowConfirmed, p.Slot, e.conf.Slot)
		}
		return fmt.Errorf("%w: slot %d", ErrUnknownPoint, p.Slot)
	}
	lp, _ := chain.PointFromLog(p)
	if _, err := e.led.Advance(ctx, chain.Event{Kind: chain.Backward, Point: lp}); err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	if _, err := e.view.Rollback(lp); err != nil {
		return fmt.Errorf("engine: view: %w", err)
	}
	gone := slices.Clone(e.window[keep:])
	e.window = e.window[:keep]
	return e.leftView(ctx, e.now, gone)
}

// walletOf is the wallet of a ledger owner: an actor or the faucet.
func (e *Engine) walletOf(id population.ActorID) keys.Wallet {
	if id == faucetActor {
		return e.faucet
	}
	return e.actors[id].wallet
}

// weightsOf is a profile's intent weights with the controls' overrides.
func (e *Engine) weightsOf(p profile.Profile) map[profile.IntentKind]float64 {
	if len(e.weights) == 0 {
		return p.Weights
	}
	w := maps.Clone(p.Weights)
	if w == nil {
		w = map[profile.IntentKind]float64{}
	}
	maps.Copy(w, e.weights)
	return w
}
