package main

import (
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

// resolveProfiles turns a --profiles argument into profile file bytes and a
// label for error messages. A shipped name (m1, preview, preprod, mainnet)
// resolves to its embedded file; an argument with a path separator or a YAML
// extension is read from disk. Empty means m1.
func resolveProfiles(arg string) ([]byte, string, error) {
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

// cohortsFor splits wallets over the profiles of reg in name order. When
// every profile has a weight the split follows the weights by largest
// remainder, so the counts sum to wallets; when none has one it is even.
func cohortsFor(reg *profile.Registry, wallets int) ([]engine.Cohort, error) {
	return cohortsOf(reg, reg.Names(), wallets)
}

// cohortsOf is cohortsFor over the named profiles only.
func cohortsOf(reg *profile.Registry, names []string, wallets int) ([]engine.Cohort, error) {
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
