package engine

import (
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pace"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// A fitted profile file's populations (profile.Population) become actors
// here. Each pool and DRep population is a profile the engine writes and a
// cohort of Count members; the network's proposal rates are one proposer
// actor. Their per-member rates are per day and scale-free, so they hold
// at any head count.

// proposerName is the network proposer's profile name.
const proposerName = "network"

const (
	secondsPerDay = 86400.0
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

// deployRate is new contracts per second, zero without a contract
// population.
func deployRate(c *Config) float64 {
	cp, ok := c.contractParams()
	if !ok {
		return 0
	}
	wallets := 0
	for _, co := range c.Cohorts {
		if p, _ := c.Profiles.Get(co.Profile); p.Kind == profile.Wallet && co.Profile != proposerName {
			wallets += co.Count
		}
	}
	return cp.New * float64(wallets) / secondsPerDay
}

// subject is the pace's view of an actor's record.
func subject(st *cohort.Actor) pace.Subject {
	return pace.Subject{State: &st.Pace, Prof: &st.Prof, Policy: st.Policy, Rate: st.Rate, Drawer: st}
}
