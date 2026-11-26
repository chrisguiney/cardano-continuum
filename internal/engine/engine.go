package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
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
// balance, never less than minPayment, and needs feeHeadroom on top so the
// builder can pay the fee and return change.
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

	guard policy.Guard
	// dark is the pool plan: pools the engine wants not forging. down are
	// pools whose pod is observed not running, repairs those a repair was
	// asked for since. stalled is set by a stall incident.
	dark, down, repairs map[population.ActorID]bool
	stalled             bool
	// rootKeys is the run seed's own keystore (genesis pools, faucet, committee);
	// gpools are the genesis pools.
	rootKeys *keys.Store
	gpools   []genesisPool
	// pools maps each pool actor to the pool it operates.
	pools map[population.ActorID][28]byte

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
	e.out = nil // genesis spawns are configuration, not decisions
	return e, nil
}

func newEmpty(cfg Config) (*Engine, error) {
	if err := cfg.validate(); err != nil {
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
	}
	if e.guard, err = policy.New(cfg.floor()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
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
	e.out = nil
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
		err = e.applyBlock(ctx, in.At, blockRec{Point: b.Point, Txs: c.Txs, Number: c.Number})
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode rollback: %w", err)
		}
		err = e.rollback(ctx, b.To)
	case eventlog.KindPoolStatus:
		var s PoolStatus
		if err = eventlog.Decode(in.Body, &s); err != nil {
			return nil, fmt.Errorf("engine: decode pool status: %w", err)
		}
		err = e.poolStatus(ctx, in.At, s)
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
	st := &actor{prof: p, policy: pol, rate: baseRate(p), wallet: e.keys.Wallet(keys.ActorID(a.ID)),
		hazardAt: population.Never, actAt: population.Never}
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
	rate := st.rate * a.Traits.RateMult
	if rate <= 0 || st.policy == nil {
		st.actAt = population.Never
		return
	}
	var mod sched.Modulation
	if d := st.prof.Rates.Diurnal; d > 0 {
		mod = sched.Daily{Amplitude: d}
	}
	st.actAt = sched.NextArrival(e.draw(a, st, "arrival"), rate, mod, now)
	st.actH = e.queue.Push(st.actAt, qev{kind: evAct, actor: a.ID})
}

func (e *Engine) act(ctx context.Context, at sched.Time, id population.ActorID) error {
	a, _ := e.census.Get(id)
	st := e.actors[id]
	r := e.draw(a, st, "act")
	d := agenda.Decide(a, r, view{e: e}, st.policy, st.prof.Weights)
	e.emit(at, KindIntentChosen, id, d)
	switch {
	case d.Intent.Kind == agenda.Idle:
	case !e.checkFloor(at, d.Intent):
	case d.Intent.Kind == agenda.GoDark, d.Intent.Kind == agenda.GoLive:
		e.act1Pool(at, d.Intent)
	default:
		if err := e.intend(ctx, at, a, st, d.Intent, r); err != nil {
			return err
		}
	}
	e.nextAct(a, st, at)
	return nil
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
	if !ok || !p.Awaiting {
		return nil
	}
	if p.Refill {
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
	return e.apply(ctx, at, p, submit.Answered{Out: out}, reasonOf(r))
}

// release emits Released for the payments whose last unfinished attempt is
// dead and forgets them. An empty why names the cause per payment: expiry or
// conflict.
func (e *Engine) release(at sched.Time, dead []plan.TxID, why string) {
	for _, tx := range dead {
		id, ok := e.byTx[TxID(tx)]
		if !ok {
			continue
		}
		p := e.pending[id]
		e.forgetTx(TxID(tx))
		if len(p.Txs) > 0 {
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
		e.emit(at, KindReleased, population.ActorID(p.Actor), Released{Intent: id, Why: w})
	}
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
			e.dropPending(id)
			e.emit(at, KindConfirmed, population.ActorID(p.Actor), Confirmed{Intent: id, ID: p.Tx})
			if err := e.settle(at, p, TxID(tx)); err != nil {
				return err
			}
		}
	}
	e.release(at, res.Dead, "")
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
	e.window = e.window[:keep]
	return nil
}

// walletOf is the wallet of a ledger owner: an actor or the faucet.
func (e *Engine) walletOf(id population.ActorID) keys.Wallet {
	if id == faucetActor {
		return e.faucet
	}
	return e.actors[id].wallet
}
