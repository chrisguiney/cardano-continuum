package simflags

import (
	"bytes"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/profiles"
)

// Population holds the flags that say who takes part: the profile set, how
// many wallets and pool operators it is spread over, and its rate scale.
type Population struct {
	Wallets  int
	Pools    int
	Profiles string
	Scale    float64
}

func (p *Population) register(fs *flag.FlagSet) {
	fs.IntVar(&p.Wallets, "wallets", 100, "wallets at genesis, spread evenly over the profiles")
	fs.StringVar(&p.Profiles, "profiles", "", "shipped profile set (m1, m3, preview, preprod) or a profile file (default: m1)")
	fs.Float64Var(&p.Scale, "scale", 1, "multiply every profile's base rates by this factor")
	fs.IntVar(&p.Pools, "pools", 0, "pool operators at genesis, spread over the pool profiles; on a live run the first --genesis-pools operate the genesis pools")
}

// Scaled applies --scale to the population's base rates.
func (p *Population) Scaled(reg *profile.Registry) (*profile.Registry, error) {
	if p.Scale == 1 {
		return reg, nil
	}
	return reg.Scale(p.Scale)
}

// parse reads the profile set --profiles names.
func (p *Population) parse() (*profile.Registry, profile.File, error) {
	raw, name, err := ResolveProfiles(p.Profiles)
	if err != nil {
		return nil, profile.File{}, err
	}
	file, err := profile.ParseFile(bytes.NewReader(raw))
	if err != nil {
		return nil, profile.File{}, fmt.Errorf("%s: %w", name, err)
	}
	reg, err := profile.NewRegistry(file.Profiles)
	if err != nil {
		return nil, profile.File{}, err
	}
	return reg, file, nil
}

// cohorts spreads the pools evenly over the pool profiles and the wallets by
// profile weight over the others.
func (p *Population) cohorts(reg *profile.Registry) ([]engine.Cohort, error) {
	if p.Wallets < 0 {
		return nil, fmt.Errorf("--wallets %d", p.Wallets)
	}
	if p.Pools < 0 {
		return nil, fmt.Errorf("--pools %d", p.Pools)
	}
	// Pool profiles take the --pools actors and come first, so the first
	// pool actors are the genesis pools' operators; every other profile
	// shares the --wallets.
	var pools, others []string
	for _, n := range reg.Names() {
		if q, _ := reg.Get(n); q.Kind == profile.Pool {
			pools = append(pools, n)
		} else {
			others = append(others, n)
		}
	}
	if p.Pools > 0 && len(pools) == 0 {
		return nil, fmt.Errorf("--pools %d: the profile set has no pool profile", p.Pools)
	}
	cohorts := spread(pools, p.Pools)
	wallets, err := CohortsOf(reg, others, p.Wallets)
	if err != nil {
		return nil, err
	}
	return append(cohorts, wallets...), nil
}

// spread divides n actors evenly over the profiles names.
func spread(names []string, n int) []engine.Cohort {
	var out []engine.Cohort
	if len(names) == 0 {
		return nil
	}
	for i, name := range names {
		c := n / len(names)
		if i < n%len(names) {
			c++
		}
		out = append(out, engine.Cohort{Profile: name, Count: c})
	}
	return out
}

// ResolveProfiles turns a --profiles argument into profile file bytes and a
// label for error messages. A shipped name (m1, preview, preprod, mainnet)
// resolves to its embedded file; an argument with a path separator or a YAML
// extension is read from disk. Empty means m1.
func ResolveProfiles(arg string) ([]byte, string, error) {
	if arg == "" {
		return profiles.M1, "m1.yaml", nil
	}
	if strings.ContainsRune(arg, filepath.Separator) || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
		raw, err := os.ReadFile(arg)
		return raw, arg, err
	}
	raw, err := profiles.Named(arg)
	if err != nil {
		return nil, "", err
	}
	return raw, arg + ".yaml", nil
}

// CohortsFor splits wallets over the profiles of reg in name order. When
// every profile has a weight the split follows the weights by largest
// remainder, so the counts sum to wallets; when none has one it is even.
func CohortsFor(reg *profile.Registry, wallets int) ([]engine.Cohort, error) {
	return CohortsOf(reg, reg.Names(), wallets)
}

// CohortsOf is CohortsFor over the named profiles only.
func CohortsOf(reg *profile.Registry, names []string, wallets int) ([]engine.Cohort, error) {
	if len(names) == 0 {
		return nil, nil
	}
	weights := make([]float64, len(names))
	set := 0
	var sum float64
	for i, n := range names {
		p, _ := reg.Get(n)
		weights[i] = p.Weight
		if p.Weight > 0 {
			set++
			sum += p.Weight
		}
	}
	switch {
	case set == 0:
		for i := range weights {
			weights[i] = 1
		}
		sum = float64(len(names))
	case set != len(names):
		return nil, fmt.Errorf("profile weights must be set on every profile or on none, got %d of %d", set, len(names))
	}
	counts := make([]int, len(names))
	rem := make([]float64, len(names))
	left := wallets
	for i, w := range weights {
		exact := float64(wallets) * w / sum
		counts[i] = int(math.Floor(exact))
		rem[i] = exact - float64(counts[i])
		left -= counts[i]
	}
	order := make([]int, len(names))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rem[order[a]] > rem[order[b]] })
	for _, i := range order[:left] {
		counts[i]++
	}
	cohorts := make([]engine.Cohort, len(names))
	for i, n := range names {
		cohorts[i] = engine.Cohort{Profile: n, Count: counts[i]}
	}
	return cohorts, nil
}
