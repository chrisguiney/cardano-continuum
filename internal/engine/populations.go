package engine

import (
	"fmt"
	"maps"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	evclock "github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// A fitted profile file's populations (profile.Population) become actors
// here. Each pool and DRep population is a profile the engine writes and a
// cohort of Count members; the network's proposal rates are one proposer
// actor. Their per-member rates are per day and scale-free, so they hold
// at any head count.
//
// A population actor does not wake at one rate and let its agenda choose
// among everything: that would make each intent's rate depend on what else
// the agenda offers. It runs one Poisson clock per intent at the fitted
// rate (a superposition: wake at the sum, then draw which clock fired), and
// the agenda only says whether the fired intent is possible now. Until it
// is registered it wakes at bootRate to register instead, since a pool or
// DRep that waited a fitted lifetime to register would never exist.

// proposerName is the network proposer's profile name.
const proposerName = "network"

const (
	secondsPerDay = 86400.0
	// bootRate is how often (per second) an unregistered population actor
	// wakes to register: every ten minutes, a few blocks of a devnet.
	bootRate = 1.0 / 600
	// Budgets are what each actor needs beyond its deposits: fees and the
	// proposer's stake key deposit.
	feeBudget = 100_000_000
	// proposalBudget is how many governance action deposits the proposer
	// holds. The fake chain never expires a proposal, so deposits are not
	// returned and a long run's proposals past this many are skipped.
	proposalBudget = 200
)

// Populate adds a profile file's non-wallet populations to cfg: an actor
// profile and a cohort for each pool and DRep population with a Count, a
// proposer actor when the network proposes, and the populations and
// network rates themselves (contract deployment reads them, and the log
// header's profile hash covers them). cfg.Profiles (and cfg.Fitted, when
// set) must hold f's profiles already, scaled by cfg.Scale; cfg.Build
// gives the deposits the budgets cover. A file with no populations leaves
// cfg as it was.
func Populate(cfg *Config, f profile.File) error {
	cfg.Populations, cfg.Network = f.Populations, f.Network
	extra := populationProfiles(f, cfg.Build)
	if len(extra) == 0 {
		return nil
	}
	for _, p := range extra {
		if _, ok := cfg.Profiles.Get(p.Name); ok {
			return fmt.Errorf("%w: population %q is also a profile name", ErrConfig, p.Name)
		}
	}
	fitted := cfg.Fitted
	if fitted == nil {
		fitted = cfg.Profiles
	}
	var err error
	if cfg.Profiles, err = withProfiles(cfg.Profiles, extra, cfg.Scale); err != nil {
		return err
	}
	if cfg.Fitted != nil {
		if cfg.Fitted, err = withProfiles(fitted, extra, 1); err != nil {
			return err
		}
	}
	for _, p := range extra {
		n := 1
		if pop, ok := cfg.population(p.Name); ok {
			n = pop.Count
		}
		cfg.Cohorts = append(cfg.Cohorts, Cohort{Profile: p.Name, Count: n})
	}
	return nil
}

func withProfiles(reg *profile.Registry, extra []profile.Profile, scale float64) (*profile.Registry, error) {
	ps := profilesIn(reg)
	add, err := profile.NewRegistry(extra)
	if err != nil {
		return nil, err
	}
	if scale != 0 && scale != 1 {
		if add, err = add.Scale(scale); err != nil {
			return nil, err
		}
	}
	return profile.NewRegistry(append(ps, profilesIn(add)...))
}

func profilesIn(reg *profile.Registry) []profile.Profile {
	var ps []profile.Profile
	for _, n := range reg.Names() {
		p, _ := reg.Get(n)
		ps = append(ps, p)
	}
	return ps
}

// populationProfiles writes the actor profile of each population that runs
// as actors, in file order, then the proposer's.
func populationProfiles(f profile.File, bp build.Params) []profile.Profile {
	// Population actors are not fitted for competence or care: they act
	// as built, and a pool operator always pays its own fees.
	traits := profile.Traits{
		Competence: profile.Beta{A: 1, B: 0}, Patience: profile.Const{V: 5},
		LearningRate: profile.Const{V: 0}, Care: profile.Const{V: 1},
	}
	var out []profile.Profile
	for _, pop := range f.Populations {
		if pop.Count == 0 {
			continue
		}
		p := profile.Profile{Name: pop.Name, Kind: pop.Kind, Traits: traits}
		switch {
		case pop.Pool != nil:
			p.Agenda, p.Budget = "operator", profile.Lovelace(bp.PoolDeposit+feeBudget)
			p.Rates.Base = perSecond(map[profile.IntentKind]float64{
				profile.IntentUpdatePool: pop.Pool.Update, profile.IntentRetirePool: pop.Pool.Retire,
				profile.IntentVote: pop.Pool.Vote,
			})
			p.Hazards.Birth = pop.Pool.Register * float64(pop.Count) / secondsPerDay
		case pop.DRep != nil:
			p.Agenda, p.Budget = "civic", profile.Lovelace(bp.DRepDeposit+feeBudget)
			p.Rates.Base = perSecond(map[profile.IntentKind]float64{
				profile.IntentVote: pop.DRep.Vote, profile.IntentRetireDRep: pop.DRep.Retire,
			})
			p.Hazards.Birth = pop.DRep.Register * float64(pop.Count) / secondsPerDay
		default:
			continue
		}
		out = append(out, p)
	}
	var total float64
	for _, k := range sortedKeys(f.Network.Proposals) {
		total += f.Network.Proposals[k]
	}
	if total > 0 {
		out = append(out, profile.Profile{
			Name: proposerName, Kind: profile.Wallet, Agenda: "civic", Traits: traits,
			Budget: profile.Lovelace(proposalBudget*bp.GovActionDeposit + bp.KeyDeposit + feeBudget),
			Rates:  profile.Rates{Base: map[profile.IntentKind]float64{profile.IntentPropose: total / secondsPerDay}},
		})
	}
	return out
}

// perSecond converts per-day rates, dropping zeros so a population that
// never retires has no retirement clock.
func perSecond(daily map[profile.IntentKind]float64) map[profile.IntentKind]float64 {
	out := map[profile.IntentKind]float64{}
	for k, v := range daily {
		if v > 0 {
			out[k] = v / secondsPerDay
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// population is the population called name.
func (c *Config) population(name string) (profile.Population, bool) {
	for _, p := range c.Populations {
		if p.Name == name {
			return p, true
		}
	}
	return profile.Population{}, false
}

// contractParams is the file's contract population, if it has one.
func (c *Config) contractParams() (*profile.ContractParams, bool) {
	for _, p := range c.Populations {
		if p.Contract != nil {
			return p.Contract, true
		}
	}
	return nil, false
}

// clock is one of a population actor's per-intent Poisson clocks. Action
// is the governance action type of a proposer's clock.
type clock struct {
	kind   profile.IntentKind
	action build.ActionType
	rate   float64 // per second
}

// clocks are a population actor's intent clocks, in kind order, and the
// intent it registers with; ok is false for an actor of no population.
func (e *Engine) clocks(a population.Actor, p profile.Profile) (cs []clock, boot profile.IntentKind, ok bool) {
	if p.Name == proposerName && len(e.cfg.Network.Proposals) > 0 {
		return proposerClocks(p.Rates.Base[profile.IntentPropose], e.cfg.Network.Proposals), profile.IntentRegisterStake, true
	}
	pop, found := e.cfg.population(p.Name)
	if !found || pop.Count == 0 {
		return nil, "", false
	}
	for _, k := range slices.Sorted(maps.Keys(p.Rates.Base)) {
		c := clock{kind: k, rate: p.Rates.Base[k]}
		if k == profile.IntentVote && pop.DRep != nil {
			// Only Voting of the DReps vote at all, so each voter votes at
			// the population's rate over that share.
			if pop.DRep.Voting <= 0 || a.Stream("voter").Float64() >= pop.DRep.Voting {
				continue
			}
			c.rate /= pop.DRep.Voting
		}
		cs = append(cs, c)
	}
	boot = profile.IntentRegisterPool
	if pop.DRep != nil {
		boot = profile.IntentRegisterDRep
	}
	return cs, boot, true
}

// proposerClocks splits the proposer's total rate over the action types in
// proportion to the fitted rates.
func proposerClocks(total float64, rates map[string]float64) []clock {
	var sum float64
	for _, k := range sortedKeys(rates) {
		sum += rates[k]
	}
	var cs []clock
	for _, k := range sortedKeys(rates) {
		if rates[k] > 0 {
			cs = append(cs, clock{kind: profile.IntentPropose, action: build.ActionType(k), rate: total * rates[k] / sum})
		}
	}
	return cs
}

// registered reports whether a population actor's registration is on the
// chain the engine follows.
func (e *Engine) registered(id population.ActorID, boot profile.IntentKind) bool {
	if e.win.View() == nil {
		// Genesis actors spawn before the view exists; none has registered.
		return false
	}
	switch boot {
	case profile.IntentRegisterPool:
		return e.win.View().PoolRegistered(id)
	case profile.IntentRegisterDRep:
		return e.win.View().DRepRegistered(id)
	}
	return e.win.View().StakeRegistered(id)
}

// bootstrapping reports that the proposer is registered and the chain has
// no open proposal, none of its own in flight. A fitted proposal rate
// leaves governance empty for days on a fresh devnet (mainnet always has
// some open), so the voters would have nothing to vote on: the proposer
// then wakes at the boot rate and proposes at once, keeping at least one
// proposal open. Which action type it proposes is drawn from the fitted
// rates as any of its wakes, from the actor's seeded stream.
func (e *Engine) bootstrapping(id population.ActorID, st *actor) bool {
	return st.prof.Name == proposerName && len(e.cfg.Network.Proposals) > 0 && e.win.View() != nil &&
		e.registered(id, st.boot) && len(e.win.View().OpenProposals()) == 0 && !e.inFlight(id, string(profile.IntentPropose))
}

// ready reports that a population actor wakes at its fitted rates:
// registered, and not bootstrapping governance.
func (e *Engine) ready(id population.ActorID, st *actor) bool {
	return e.registered(id, st.boot) && !e.bootstrapping(id, st)
}

// clockRate is a population actor's wake rate: the boot rate until it is
// ready, then the sum of its clocks.
func (e *Engine) clockRate(id population.ActorID, st *actor) float64 {
	if !e.ready(id, st) {
		return bootRate
	}
	var sum float64
	for _, c := range st.cs {
		sum += c.rate
	}
	return sum * float64(max(e.rateK, 1))
}

// fire draws which clock woke the actor: the boot intent while it is
// unregistered, else a clock in proportion to its rate.
func (e *Engine) fire(id population.ActorID, st *actor, r *rand.Rand) clock {
	switch {
	case !e.registered(id, st.boot):
		return clock{kind: st.boot}
	case len(st.cs) == 0, st.booting && !e.bootstrapping(id, st):
		// Registered since this wake was drawn: the next one is drawn at
		// the clocks' rate, which by memorylessness is the right wait.
		return clock{kind: agenda.Idle}
	}
	var sum float64
	for _, c := range st.cs {
		sum += c.rate
	}
	u := r.Float64() * sum
	for _, c := range st.cs {
		if u < c.rate {
			return c
		}
		u -= c.rate
	}
	return st.cs[len(st.cs)-1]
}

// decideClock is a population actor's decision: the fired clock's intent
// if its agenda scores it possible now, else idle. The proposer has no
// agenda for proposing; it proposes whenever its clock fires.
func (e *Engine) decideClock(a population.Actor, st *actor, r *rand.Rand) (agenda.Decision, build.ActionType) {
	c := e.fire(a.ID, st, r)
	if c.kind == agenda.Idle {
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: agenda.Idle, Value: 1, Why: "registered: waking at the fitted rates from now"}}}, ""
	}
	if e.inFlight(a.ID, string(c.kind)) {
		// A second registration beside one in flight would pay its deposit
		// twice.
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: c.kind, Why: "already in flight"}}}, ""
	}
	if st.prof.Name == proposerName {
		why := "network proposes " + string(c.action)
		if e.bootstrapping(a.ID, st) {
			why += ": no proposal open"
		}
		if c.kind == profile.IntentRegisterStake {
			why = "proposal deposits return to a registered stake account"
		}
		s := agenda.Score{Kind: c.kind, Value: 1, Why: why}
		return agenda.Decision{Intent: agenda.Intent{Kind: c.kind, Actor: a.ID}, Scores: []agenda.Score{s}}, c.action
	}
	var mine []agenda.Score
	for _, s := range st.policy.Score(a, view{e: e}) {
		if s.Kind == c.kind {
			mine = append(mine, s)
		}
	}
	in, ss := agenda.Choose(a.ID, r, mine, nil)
	return agenda.Decision{Intent: in, Scores: ss}, c.action
}

// poolTerms are the pledge, cost and margin a pool actor registers or
// updates with: draws from the population's fitted distributions on a
// stream named by the intent, so a rebuild offers the same terms. A pool
// of no population registers the minimum terms.
func (e *Engine) poolTerms(id population.ActorID, intent uint64) (pledge, cost uint64, margin *big.Rat) {
	pledge, cost, margin = 1_000_000, e.cfg.Build.MinPoolCost, big.NewRat(1, 100)
	st, ok := e.actors[id]
	if !ok {
		return pledge, cost, margin
	}
	pop, ok := e.cfg.population(st.prof.Name)
	if !ok || pop.Pool == nil {
		return pledge, cost, margin
	}
	a, _ := e.census.Get(id)
	r := a.Stream("pool-terms", strconv.FormatUint(intent, 10))
	pledge = lovelaceOf(pop.Pool.Pledge.Sample(r))
	cost = max(lovelaceOf(pop.Pool.Cost.Sample(r)), e.cfg.Build.MinPoolCost)
	// A margin is a rational on chain; millionths are finer than any pool
	// sets.
	m := min(max(pop.Pool.Margin.Sample(r), 0), 1)
	return pledge, cost, big.NewRat(int64(math.Round(m*1e6)), 1e6)
}

// maxTerm caps a drawn pledge or cost: a lognormal tail can pass the
// lovelace supply.
const maxTerm = 45_000_000_000_000_000

func lovelaceOf(x float64) uint64 {
	if math.IsNaN(x) || x <= 0 {
		return 0
	}
	return uint64(min(x, maxTerm))
}

// Contracts.
//
// Contract 0 (build.AlwaysSucceeds) exists from genesis. A file with a
// contract population deploys more at its new_rate per genesis wallet per
// day: a deployment is the moment its address becomes one wallets lock at,
// so it has no transaction of its own. Each contract draws a usage weight
// from the population's Pareto, and a lock picks its contract in
// proportion to the weights, so a few contracts take most calls.

// deployRate is new contracts per second, zero without a contract
// population.
func (e *Engine) deployRate() float64 {
	c, ok := e.cfg.contractParams()
	if !ok {
		return 0
	}
	wallets := 0
	for _, co := range e.cfg.Cohorts {
		if p, _ := e.cfg.Profiles.Get(co.Profile); p.Kind == profile.Wallet && co.Profile != proposerName {
			wallets += co.Count
		}
	}
	return c.New * float64(wallets) / secondsPerDay
}

func (e *Engine) scheduleDeploy(now sched.Time) {
	e.deployN++
	r := e.cfg.Seed.Derive("deploy", strconv.FormatUint(e.deployN, 10)).Rand()
	e.deployAt = sched.NextArrival(r, e.deployRate(), nil, now)
	e.pushDeploy()
}

func (e *Engine) pushDeploy() {
	if e.deployAt != population.Never && e.deployAt != 0 {
		e.clk.Push(e.deployAt, evclock.Event{Kind: evclock.Deploy})
	}
}

// deploy makes the next contract available.
func (e *Engine) deploy(at sched.Time) error {
	i := e.contracts
	if err := e.bookContract(i); err != nil {
		return err
	}
	e.contracts++
	e.jr.Emit(at, KindDeployed, 0, Deployed{Contract: i, Address: build.Contract(i).Name})
	e.scheduleDeploy(at)
	return nil
}

func (e *Engine) bookContract(i uint64) error {
	if err := e.win.Book().AddAddress(build.Contract(i).Address(e.cfg.NetworkID), scriptActor); err != nil {
		return fmt.Errorf("engine: book contract %d: %w", i, err)
	}
	return nil
}

// Contracts is the number of contracts deployed, contract 0 included.
func (e *Engine) Contracts() int { return int(e.contracts) }

// usage is contract i's usage weight.
func (e *Engine) usage(i uint64) float64 {
	c, ok := e.cfg.contractParams()
	if !ok {
		return 1
	}
	return c.Usage.Sample(e.cfg.Seed.Derive("contract", strconv.FormatUint(i, 10)).Rand())
}

// pickContract draws the contract a lock goes to. With one contract it
// draws nothing, so a file without contracts locks as it always did.
func (e *Engine) pickContract(r *rand.Rand) uint64 {
	if e.contracts <= 1 {
		return 0
	}
	ws := make([]float64, e.contracts)
	var sum float64
	for i := range ws {
		ws[i] = e.usage(uint64(i))
		sum += ws[i]
	}
	u := r.Float64() * sum
	for i, w := range ws {
		if u < w {
			return uint64(i)
		}
		u -= w
	}
	return e.contracts - 1
}

// nextClock schedules a population actor's next wake. A retired pool or
// DRep never wakes again.
func (e *Engine) nextClock(a population.Actor, st *actor, now sched.Time) {
	st.actAt = population.Never
	if a.State == population.Retired || a.State == population.Expired {
		return
	}
	st.booting = !e.ready(a.ID, st)
	if at := sched.NextArrival(e.draw(a, st, "arrival"), e.clockRate(a.ID, st), nil, now); at != population.Never {
		st.actAt = at
		st.actH = e.clk.Push(at, evclock.Event{Kind: evclock.Act, Actor: a.ID})
	}
}
