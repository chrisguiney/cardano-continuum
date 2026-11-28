package simflags

import (
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// Run holds the flags that say what the run is: its seed, whether honest
// actors err, and which adversaries act.
type Run struct {
	Seed     uint64
	Mistakes string
	Attacks  string
	// HardFork is --hard-fork, empty when unset.
	HardFork string
}

func (r *Run) register(fs *flag.FlagSet) {
	fs.Uint64Var(&r.Seed, "seed", 1, "root seed of the run")
	fs.StringVar(&r.Mistakes, "mistakes", "", "honest mistakes: on or off (default: on when a profile's competence can fall below 1)")
	fs.StringVar(&r.Attacks, "attack", "", "tier-1 attacks, comma-separated, one adversary actor each: "+JoinNames(adversary.Names())+" (devnet only)")
	fs.StringVar(&r.HardFork, "hard-fork", "", "hard fork the network proposer proposes by itself: <major.minor>@epoch:<n>, a version alone (epoch 3) or none; unset is 11.0@epoch:3 when the genesis version can fork to it")
}

// HardForkSchedule resolves --hard-fork for a run whose genesis version is
// start. Unset it is the default fork, or none when start cannot fork to it
// in one step (a devnet already at 11.0); a value the user wrote is taken
// as written and the engine refuses one start cannot reach.
func (r *Run) HardForkSchedule(start protocol.Version) (protocol.Schedule, error) {
	if r.HardFork == "" {
		if def := protocol.DefaultSchedule(); protocol.CanFollow(def.To, start) {
			return def, nil
		}
		return protocol.Schedule{}, nil
	}

	s, err := protocol.ParseSchedule(r.HardFork)
	if err != nil {
		return protocol.Schedule{}, fmt.Errorf("--hard-fork: %w", err)
	}
	return s, nil
}

// MistakesOn resolves --mistakes. Unset, mistakes are on when some
// profile's competence is a Beta with B > 0, which falls below 1; an actor
// of competence 1 never errs anyway.
func (r *Run) MistakesOn(reg *profile.Registry) (bool, error) {
	switch r.Mistakes {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "":
		// An adversary's competence says nothing about honest mistakes.
		for _, n := range reg.Names() {
			if p, _ := reg.Get(n); p.Traits.Competence.B > 0 && p.Adversary == nil {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("--mistakes %q: want on or off", r.Mistakes)
}

// AttackNames parses --attack: a comma-separated list of tier-1 attacks,
// one adversary each.
func (r *Run) AttackNames() ([]adversary.Name, error) {
	if r.Attacks == "" {
		return nil, nil
	}
	var out []adversary.Name
	for _, part := range strings.Split(r.Attacks, ",") {
		n, err := adversary.Parse(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("--attack: %w", err)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// ChainParams are the fake chain's parameters: the devnet genesis with
// every override the --attack attacks need (adversary.GenesisOverrides),
// which a devnet allows only on purpose.
func (r *Run) ChainParams() (conway.ConwayProtocolParameters, error) {
	ns, err := r.AttackNames()
	if err != nil {
		return conway.ConwayProtocolParameters{}, err
	}
	spec := genesis.Default()
	for _, n := range ns {
		for _, o := range adversary.GenesisOverrides(n) {
			ov, err := genesis.ParseOverride(o)
			if err != nil {
				return conway.ConwayProtocolParameters{}, fmt.Errorf("--attack %s: %w", n, err)
			}
			ov.Apply(&spec)
			spec.Allow.InconsistentExUnits = true
		}
	}
	return fakechain.ParamsFor(spec)
}

// WithAdversaries adds the built-in profile of every --attack to ps and
// one actor of each to cohorts, after every honest one, so the honest
// actors keep the IDs, keys and streams they have in a control run.
func (r *Run) WithAdversaries(ps []profile.Profile, cohorts []engine.Cohort) ([]profile.Profile, []engine.Cohort, error) {
	ns, err := r.AttackNames()
	if err != nil || len(ns) == 0 {
		return ps, cohorts, err
	}
	advs, err := adversary.Profiles(ns...)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range ns {
		cohorts = append(cohorts, engine.Cohort{Profile: adversary.ProfileName(n), Count: 1})
	}
	return append(ps, advs...), cohorts, nil
}

// JoinNames lists attack names for a message.
func JoinNames(ns []adversary.Name) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = string(n)
	}
	return strings.Join(s, ", ")
}

// ClearFake clears a fake run's adversaries: the in-process chain is a
// devnet with internal/genesis's magic, and chain.Guard checks it.
func ClearFake(cfg *engine.Config) error {
	g := engine.FakeGenesis(cfg, genesis.Default().Magic)
	c, err := adversary.Clear(g, g)
	cfg.Adversaries = c
	return err
}
