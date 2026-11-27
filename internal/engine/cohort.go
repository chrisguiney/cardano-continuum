package engine

import (
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// The cohort is the engine's population: its actors and their wallets and
// pool keys, the lifecycle edges that move them, the birth streams that add
// them, the genesis pools some of them operate and the identity rows the UI
// reads. It schedules and emits only through the interfaces below, which
// the composite satisfies, so none of it reaches the other components.
//
//	roster   the actors, their records and the address index
//	life     spawn, the hazard clock and the chain-gated lifecycle edges
//	births   the per-profile birth streams
//	keyring  wallets, DRep and pool keys, and the genesis pools
//	idents   the identity rows

// scheduler is the event queue as the cohort uses it.
type scheduler interface {
	Hazard(at sched.Time, id population.ActorID) sched.Handle
	Birth(at sched.Time, idx int) sched.Handle
	Cancel(h sched.Handle)
}

// emitter writes a decision to the journal.
type emitter interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// attackHooks is what the cohort asks of the adversaries.
type attackHooks interface {
	// Arm returns pol with the actor's attacks, when its profile has them.
	Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy
	// Adversary reports whether the actor is an adversary.
	Adversary(id population.ActorID) bool
	// Spent is what the adversary has spent on attacks.
	Spent(id population.ActorID) uint64
}

// paceHooks is what the cohort asks of the pacing: an actor's intent
// clocks, and its next wake.
type paceHooks interface {
	Clocks(a population.Actor, p profile.Profile) ([]clock, profile.IntentKind)
	Wake(a population.Actor, st *actor, now sched.Time)
}

// chainView is the chain side the cohort books into and reads.
type chainView interface {
	Book() *chain.Book
	View() *chain.View
}

// env is what the cohort's parts share of the rest of the engine.
type env struct {
	sched scheduler
	emit  emitter
	view  chainView
	att   attackHooks
	pace  paceHooks
}

// pacing is an actor's wake-up state: the next act and the population
// clocks and Hawkes follow-ups that decide it.
type pacing struct {
	actAt sched.Time
	actH  sched.Handle
	// cs are a population actor's intent clocks and boot the intent it
	// registers with (populations.go); boot is empty for every other actor.
	cs   []clock
	boot profile.IntentKind
	// booting is set while the pending wake was drawn at the boot rate:
	// it registers, and must not fire a fitted clock (a clock's first
	// event would come at the boot rate, far too soon).
	booting bool
	// owed is how many follow-ups the actor's transactions have begotten
	// and not yet made, when its profile has Hawkes parameters; followUp
	// marks the pending wake as one of them.
	owed     uint32
	followUp bool
	// waits is how many blocks a follow-up has waited for coins to act on.
	waits uint8
}

type actor struct {
	pacing
	prof     profile.Profile
	policy   agenda.Policy
	rate     float64 // sum of the profile's base rates, before RateMult
	wallet   keys.Wallet
	hazardAt sched.Time
	hazardTo population.State
	hazardH  sched.Handle
	draws    uint64
	// paid are the actor's payees, most recent last, at most maxPaid.
	paid []population.ActorID
	// ident is the actor's unchanging public identity (identity.go),
	// computed on first use.
	ident   store.Identity
	identOK bool
}

// draw returns a fresh stream for the actor's next random decision. Each
// draw has its own derived stream, so the snapshot only needs the counter.
func (st *actor) draw(a population.Actor, purpose string) *rand.Rand {
	st.draws++
	return a.Stream(purpose, strconv.FormatUint(st.draws, 10))
}

// maxPaid bounds the payees an actor remembers.
const maxPaid = 32

// remember records a payee as the actor's most recent.
func (st *actor) remember(to population.ActorID) {
	st.paid = slices.DeleteFunc(st.paid, func(id population.ActorID) bool { return id == to })
	st.paid = append(st.paid, to)
	if len(st.paid) > maxPaid {
		st.paid = slices.Delete(st.paid, 0, len(st.paid)-maxPaid)
	}
}

// cohort is the population and its parts.
type cohort struct {
	*roster
	life   *life
	births *births
	idents *idents
}

// cohortSpec is what a cohort is built from.
type cohortSpec struct {
	seed      rng.Seed
	census    rng.Seed
	slots     sched.Slots
	proposer  string
	actorKeys *keys.Store
	rootKeys  *keys.Store
	pools     []genesisPool
	poolStake uint64
}

func newCohort(sp cohortSpec, v env) *cohort {
	ring := newKeyring(sp.actorKeys, sp.rootKeys, &gset{pools: sp.pools, stake: sp.poolStake, view: v.view})
	r := newRoster(sp.census, ring, v)
	l := &life{r: r, slots: sp.slots}
	return &cohort{roster: r, life: l,
		births: &births{life: l, seed: sp.seed, sched: v.sched, emit: v.emit},
		idents: &idents{r: r, proposer: sp.proposer, att: v.att, view: v.view}}
}

// roster is the actors: the census, each actor's in-memory record, and the
// index from address to actor.
type roster struct {
	census population.Census
	actors map[population.ActorID]*actor
	order  []population.ActorID // ascending
	byAddr map[string]population.ActorID
	// seed is the census's stream root.
	seed   rng.Seed
	ring   *keyring
	faucet keys.Wallet
	env    env
}

func newRoster(seed rng.Seed, ring *keyring, v env) *roster {
	return &roster{actors: map[population.ActorID]*actor{}, byAddr: map[string]population.ActorID{}, seed: seed, ring: ring, env: v}
}

// get is the census entry of an actor.
func (r *roster) get(id population.ActorID) (population.Actor, bool) { return r.census.Get(id) }

// rec is the actor's record, nil when there is none.
func (r *roster) rec(id population.ActorID) *actor { return r.actors[id] }

// ids lists every actor, ascending. The slice is the roster's own.
func (r *roster) ids() []population.ActorID { return r.order }

// pop is the population's census.
func (r *roster) pop() *population.Census { return &r.census }

// Owner is the actor whose address is addr, or the faucet.
func (r *roster) Owner(addr []byte) (population.ActorID, bool) {
	id, ok := r.byAddr[string(addr)]
	return id, ok
}

// Address is the wallet address of a ledger owner: an actor or the faucet.
func (r *roster) Address(id population.ActorID) common.Address { return r.wallet(id).Address() }

// wallet is the wallet of a ledger owner: an actor or the faucet.
func (r *roster) wallet(id population.ActorID) keys.Wallet {
	if id == faucetActor {
		return r.faucet
	}
	return r.actors[id].wallet
}

// bindFaucet names the faucet's wallet as the owner of its address.
func (r *roster) bindFaucet(w keys.Wallet) error {
	addr, err := w.Address().Bytes()
	if err != nil {
		return fmt.Errorf("engine: faucet address: %w", err)
	}
	r.faucet = w
	r.byAddr[string(addr)] = faucetActor
	_ = r.env.view.Book().AddAddress(w.Address(), faucetActor)
	return nil
}

// revive spawns the actor a snapshot recorded, so it gets the same traits.
func (r *roster) revive(p profile.Profile, born sched.Time) population.Actor {
	return r.census.Spawn(p, r.seed, born)
}

// register builds the in-memory side of an actor: policy, wallet, address
// index. It schedules nothing.
func (r *roster) register(a population.Actor, p profile.Profile) *actor {
	pol, err := agenda.For(p.Agenda)
	if err != nil {
		pol = nil // the actor scores nothing and stays idle
	}
	pol = r.env.att.Arm(a.ID, p, pol)
	st := &actor{prof: p, policy: pol, rate: baseRate(p), wallet: r.ring.actors.Wallet(keys.ActorID(a.ID))}
	st.hazardAt, st.actAt = population.Never, population.Never
	st.cs, st.boot = r.env.pace.Clocks(a, p)
	r.actors[a.ID] = st
	r.order = append(r.order, a.ID)
	if addr, err := st.wallet.Address().Bytes(); err == nil {
		r.byAddr[string(addr)] = a.ID
	}
	r.ring.bind(r.env.view.Book(), a.ID, p, st.wallet)
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

// life moves actors along their lifecycle: it spawns them, runs their
// hazard clocks, and applies the chain-gated edges a confirmed certificate
// or an epoch transition causes. Settled means K deep, as everything
// durable.
type life struct {
	r     *roster
	slots sched.Slots
}

// spawn adds an actor and schedules its first hazard.
func (l *life) spawn(p profile.Profile, at sched.Time) population.Actor {
	a := l.r.census.Spawn(p, l.r.seed, at)
	st := l.r.register(a, p)
	l.nextHazard(a, st, at)
	// Pools and DReps have no hazard clock to wake them: they act from
	// birth, registering first.
	if p.Kind != profile.Wallet {
		l.r.env.pace.Wake(a, st, at)
	}
	return a
}

func (l *life) nextHazard(a population.Actor, st *actor, now sched.Time) {
	st.hazardAt, st.hazardTo = population.NextHazard(a, st.prof, st.draw(a, "hazard"), now)
	if st.hazardAt != population.Never {
		st.hazardH = l.r.env.sched.Hazard(st.hazardAt, a.ID)
	}
}

func (l *life) hazard(at sched.Time, id population.ActorID) error {
	a, _ := l.r.census.Get(id)
	st := l.r.actors[id]
	from, to := a.State, st.hazardTo
	st.hazardAt = population.Never
	if err := l.r.census.Apply(population.Transition{ID: id, To: to, At: at, Cause: population.Timer{}}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", id, err)
	}
	l.r.env.emit.Emit(at, KindTransition, id, Transition{From: string(from), To: string(to)})
	a, _ = l.r.census.Get(id)
	switch {
	case to == population.Active:
		l.r.env.pace.Wake(a, st, at)
	case from == population.Active && st.actAt != population.Never:
		l.r.env.sched.Cancel(st.actH)
		st.actAt = population.Never
	}
	l.nextHazard(a, st, at)
	return nil
}

// settled applies the lifecycle edge a settled attempt of an intent of the
// given kind causes for its actor.
func (l *life) settled(at sched.Time, id population.ActorID, kind profile.IntentKind, tx TxID) error {
	a, ok := l.r.census.Get(id)
	if !ok {
		return nil
	}
	cause := population.Confirmed{Tx: hex.EncodeToString(tx[:])}
	switch kind {
	case profile.IntentRegisterPool:
		return l.move(at, a, population.Active, cause)
	case profile.IntentRetirePool:
		return l.move(at, a, population.Retiring, cause)
	case profile.IntentRegisterDRep:
		return l.move(at, a, population.Voting, cause)
	case profile.IntentUpdatePool:
		// A re-registration cancels a pending retirement.
		return l.move(at, a, population.Active, cause)
	case profile.IntentRetireDRep:
		if err := l.move(at, a, population.Expired, cause); err != nil {
			return err
		}
		if st := l.r.actors[id]; st.actAt != population.Never {
			l.r.env.sched.Cancel(st.actH)
			st.actAt = population.Never
		}
	}
	return nil
}

// move applies a lifecycle edge if the actor's kind has it from its
// current state; any other request is a no-op, since a confirmation can
// arrive for an actor that has already moved on.
func (l *life) move(at sched.Time, a population.Actor, to population.State, cause population.Cause) error {
	if !population.Allowed(a.Kind, a.State, to) {
		return nil
	}
	if err := l.r.census.Apply(population.Transition{ID: a.ID, To: to, At: at, Cause: cause}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", a.ID, err)
	}
	l.r.env.emit.Emit(at, KindTransition, a.ID, Transition{From: string(a.State), To: string(to)})
	return nil
}

// epochApplied retires every retiring pool actor whose retirement the
// confirmed view's epoch transition into epoch applied.
func (l *life) epochApplied(at sched.Time, epoch uint64) error {
	c := l.r.env.view.View().Confirmed()
	for _, id := range l.r.census.IDs(profile.Pool, population.Retiring) {
		if c.PoolRegistered(id) {
			continue
		}
		a, _ := l.r.census.Get(id)
		if err := l.move(at, a, population.Retired, population.EpochApplied{Epoch: epoch}); err != nil {
			return err
		}
		st := l.r.actors[id]
		if st.actAt != population.Never {
			l.r.env.sched.Cancel(st.actH)
			st.actAt = population.Never
		}
	}
	return nil
}

// lifePath is each kind's lifecycle in order from its initial state, so
// restore can walk an actor to its recorded state.
var lifePath = map[profile.Kind][]population.State{
	profile.Wallet: {population.Born, population.Active},
	profile.Pool:   {population.Registered, population.Active, population.Retiring, population.Retired},
	profile.DRep:   {population.Registered, population.Voting, population.Expired},
}

// walk moves a freshly spawned actor to state to at time at. Chain-gated
// edges take a cause standing for the confirmation the snapshot recorded.
func (l *life) walk(a population.Actor, to population.State, at sched.Time) error {
	path := lifePath[a.Kind]
	if a.Kind == profile.Wallet && to != population.Born && to != population.Active {
		path = append(path, to)
	}
	from := a.State
	for _, s := range path[1:] {
		if from == to {
			return nil
		}
		var cause population.Cause = population.Timer{}
		switch {
		case s == population.Retired:
			cause = population.EpochApplied{Epoch: l.slots.EpochAt(at)}
		case population.ChainGated(a.Kind, from, s):
			cause = population.Confirmed{Tx: "snapshot"}
		}
		if err := l.r.census.Apply(population.Transition{ID: a.ID, To: s, At: at, Cause: cause}); err != nil {
			return fmt.Errorf("engine: restore actor %d: %w", a.ID, err)
		}
		from = s
	}
	if from != to {
		return fmt.Errorf("engine: restore actor %d: state %s unreachable", a.ID, to)
	}
	return nil
}

type birth struct {
	prof profile.Profile
	at   sched.Time
	n    uint64
	h    sched.Handle
}

// births are the per-profile birth streams: each profile with a birth
// hazard adds actors at its own Poisson stream.
type births struct {
	list  []*birth // by profile name
	life  *life
	seed  rng.Seed
	sched scheduler
	emit  emitter
}

func (bs *births) add(p profile.Profile) { bs.list = append(bs.list, &birth{prof: p}) }

// start schedules every stream's first birth after now.
func (bs *births) start(now sched.Time) {
	for _, b := range bs.list {
		bs.schedule(b, now)
	}
}

// run spawns the member the birth event of stream idx brings, then
// schedules the stream's next.
func (bs *births) run(at sched.Time, idx int) {
	b := bs.list[idx]
	a := bs.life.spawn(b.prof, at)
	bs.emit.Emit(at, KindSpawned, a.ID, Spawned{Profile: b.prof.Name})
	bs.schedule(b, at)
}

func (bs *births) schedule(b *birth, now sched.Time) {
	b.n++
	r := bs.seed.Derive("birth", b.prof.Name, strconv.FormatUint(b.n, 10)).Rand()
	b.at = population.NextBirth(r, b.prof, now)
	bs.push(b)
}

func (bs *births) push(b *birth) {
	if b.at == population.Never {
		return
	}
	idx := slices.Index(bs.list, b)
	b.h = bs.sched.Birth(b.at, idx)
}

// len is the number of streams.
func (bs *births) len() int { return len(bs.list) }

// stream is stream i's profile name, next birth and draw count.
func (bs *births) stream(i int) (name string, at sched.Time, n uint64) {
	b := bs.list[i]
	return b.prof.Name, b.at, b.n
}

// resume sets stream i to a snapshot's state and schedules its next birth.
func (bs *births) resume(i int, at sched.Time, n uint64) {
	b := bs.list[i]
	b.at, b.n = at, n
	bs.push(b)
}
