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
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
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
	idleKind     = string(agenda.Idle)
	sendKind     = string(profile.IntentSend)
	censusStream = "census"
)

type evKind uint8

// Event kinds in the order same-time events run.
const (
	evBirth evKind = iota + 1
	evHazard
	evAct
)

type qev struct {
	kind  evKind
	actor population.ActorID
	birth int
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

	census population.Census
	queue  sched.Queue
	now    sched.Time
	actors map[population.ActorID]*actor
	order  []population.ActorID // ascending
	births []*birth             // by profile name
	byAddr map[string]population.ActorID

	conf   eventlog.ChainPoint
	window []blockRec

	led      *plan.Ledger
	pending  map[uint64]*pendingRec
	byTx     map[TxID]uint64
	awaiting []uint64 // ascending intent IDs without a node answer
	nextID   uint64
	fees     uint64
	foreign  uint64

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
	var genesis []plan.Utxo
	i := 0
	for _, co := range cfg.Cohorts {
		p, _ := cfg.Profiles.Get(co.Profile)
		for range co.Count {
			a := e.spawn(p, 0)
			f := funds[i]
			i++
			addr, err := f.Address.Bytes()
			if err != nil {
				return nil, err
			}
			// Genesis outputs are (Blake2b-256 of the address, 0), as in
			// Shelley genesis and the fake chain.
			genesis = append(genesis, plan.Utxo{Ref: plan.Ref{Tx: blake2b256(addr)}, Owner: a.ID, Lovelace: f.Lovelace})
		}
	}
	if e.led, err = plan.New(e.ledgerConfig(), genesis); err != nil {
		return nil, fmt.Errorf("engine: ledger: %w", err)
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
	e := &Engine{
		cfg: cfg, keys: ks, builder: build.New(cfg.Build), sel: sel, root: cfg.Seed.Derive(censusStream),
		actors: map[population.ActorID]*actor{}, byAddr: map[string]population.ActorID{},
		pending: map[uint64]*pendingRec{}, byTx: map[TxID]uint64{},
	}
	for _, n := range cfg.Profiles.Names() {
		p, _ := cfg.Profiles.Get(n)
		if p.Hazards.Birth > 0 {
			e.births = append(e.births, &birth{prof: p})
		}
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
		err = e.applyBlock(ctx, in.At, blockRec{Point: b.Point, Txs: c.Txs})
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err = eventlog.Decode(in.Body, &b); err != nil {
			return nil, fmt.Errorf("engine: decode rollback: %w", err)
		}
		err = e.rollback(ctx, b.To)
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
	switch string(d.Intent.Kind) {
	case idleKind:
	case sendKind:
		if err := e.pay(ctx, at, a, st, r); err != nil {
			return err
		}
	default:
		e.emit(at, KindSkipped, id, Skipped{Kind: string(d.Intent.Kind), Why: "intent kind not supported in M1"})
	}
	e.nextAct(a, st, at)
	return nil
}

// pay builds a payment to another live wallet, reserves its inputs and
// queues it for submission.
func (e *Engine) pay(ctx context.Context, at sched.Time, a population.Actor, st *actor, r *rand.Rand) error {
	skip := func(why string) { e.emit(at, KindSkipped, a.ID, Skipped{Kind: sendKind, Why: why}) }
	var targets []population.ActorID
	for _, id := range e.order {
		if id == a.ID {
			continue
		}
		if t, _ := e.census.Get(id); t.State != population.Abandoned {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		skip("no payee")
		return nil
	}
	to := targets[r.IntN(len(targets))]
	id := e.nextID + 1
	avail := e.coins(a.ID, plan.IntentID(id))
	bal := sumCoins(avail)
	amount := max(uint64(float64(bal)*(minFraction+fracSpread*r.Float64())), minPayment)
	coins := e.sel.Select(avail, amount+feeHeadroom)
	if coins == nil {
		skip(fmt.Sprintf("spendable %d below %d", bal, amount+feeHeadroom))
		return nil
	}
	inputs := make([]Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	ttl := e.cfg.Slots.SlotAt(at) + e.cfg.TTL
	built, err := e.builder.Pay(ctx, st.wallet, build.Payment{
		Inputs: e.ledgerUtxos(st.wallet, coins), To: e.actors[to].wallet.Address(), Amount: amount, TTL: ttl,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		skip("build: " + err.Error())
		return nil
	}
	// The reservation is part of this decision. It is durable once the input
	// that caused it is, since replaying the input repeats it; the Runner
	// also syncs the Submitted record before the transaction leaves.
	if err := e.led.Reserve(ctx, plan.IntentID(id), attempt(a.ID, TxID(built.ID), inputs, ttl)); err != nil {
		skip("reserve: " + err.Error())
		return nil
	}
	e.nextID = id
	p := &pendingRec{ID: id, Actor: uint64(a.ID), Tx: TxID(built.ID), TTL: ttl, Cbor: built.Tx, Awaiting: true}
	e.addPending(p)
	e.emit(at, KindSubmitted, a.ID, Submitted{
		Intent: p.ID, ID: p.Tx, To: uint64(to), Amount: amount, Fee: built.Fee, TTL: ttl, Inputs: inputs, Tx: built.Tx,
	})
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
	e.byTx[p.Tx] = p.ID
	if p.Awaiting {
		e.awaiting = append(e.awaiting, p.ID)
	}
}

func (e *Engine) dropPending(id uint64) {
	if p, ok := e.pending[id]; ok {
		delete(e.byTx, p.Tx)
		delete(e.pending, id)
	}
	e.stopAwaiting(id)
}

func (e *Engine) stopAwaiting(id uint64) {
	if i, ok := slices.BinarySearch(e.awaiting, id); ok {
		e.awaiting = slices.Delete(e.awaiting, i, i+1)
	}
}

// submitResult records the node's answer. Only a rejection of the first
// send writes the payment off: a transport error, or a rejection of a
// resend (which the transaction's own earlier arrival can cause), leaves it
// reserved until the ledger settles it or finds it dead
// (PlannerReleaseOnSubmitErrorBug in models/specs/planner-reservations).
func (e *Engine) submitResult(ctx context.Context, at sched.Time, r SubmitResult) error {
	p, ok := e.pending[r.Intent]
	if !ok || !p.Awaiting {
		return nil
	}
	switch r.Verdict {
	case VerdictAccepted, VerdictUnreached:
	case VerdictRejected:
		if r.Resend {
			break
		}
		dead, err := e.led.Refused(ctx, plan.TxID(p.Tx))
		if err != nil {
			return fmt.Errorf("engine: refuse intent %d: %w", p.ID, err)
		}
		e.release(at, dead, "rejected: "+r.Reason)
		return nil
	default:
		return fmt.Errorf("%w: intent %d", ErrUnknownVerdict, r.Intent)
	}
	p.Awaiting, p.Cbor = false, nil
	e.stopAwaiting(p.ID)
	return nil
}

// release emits Released for the payments of dead transactions and forgets
// them. An empty why names the cause per payment: expiry or conflict.
func (e *Engine) release(at sched.Time, dead []plan.TxID, why string) {
	for _, tx := range dead {
		id, ok := e.byTx[TxID(tx)]
		if !ok {
			continue
		}
		p := e.pending[id]
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
	res, err := e.led.Advance(ctx, chain.Event{Kind: chain.Forward, Block: lb, Point: lb.Point})
	if err != nil {
		return fmt.Errorf("engine: ledger: %w", err)
	}
	e.window = append(e.window, b)
	for uint64(len(e.window)) > e.cfg.K {
		e.confirm(e.window[0])
		e.window = slices.Delete(e.window, 0, 1)
	}
	for _, tx := range res.Settled {
		if id, ok := e.byTx[TxID(tx)]; ok {
			p := e.pending[id]
			e.dropPending(id)
			e.emit(at, KindConfirmed, population.ActorID(p.Actor), Confirmed{Intent: id, ID: p.Tx})
		}
	}
	e.release(at, res.Dead, "")
	return nil
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
			t.Creates = append(t.Creates, ownedUtxo(e.actors[owner].wallet.Address(), Ref{Tx: tx.ID, Index: o.Index}, o.Lovelace))
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
	e.window = e.window[:keep]
	return nil
}
