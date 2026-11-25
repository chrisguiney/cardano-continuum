package profile

import (
	"fmt"
	"maps"
	"math"
	"sort"
)

// Registry holds profiles by name. It is immutable after construction.
type Registry struct {
	byName map[string]Profile
}

// NewRegistry indexes ps by name. Two profiles with one name is an error.
func NewRegistry(ps []Profile) (*Registry, error) {
	r := &Registry{byName: make(map[string]Profile, len(ps))}
	for _, p := range ps {
		if _, dup := r.byName[p.Name]; dup {
			return nil, fmt.Errorf("duplicate profile name %q", p.Name)
		}
		r.byName[p.Name] = p
	}
	return r, nil
}

// Get returns the profile called name.
func (g *Registry) Get(name string) (Profile, bool) {
	p, ok := g.byName[name]
	return p, ok
}

// Names returns every profile name in sorted order.
func (g *Registry) Names() []string {
	names := make([]string, 0, len(g.byName))
	for n := range g.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Scale returns a registry whose per-intent base rates are multiplied by k.
// Shapes (traits, weights, hazards, Hawkes, diurnal) are unchanged, so a
// population can be run hotter or colder without changing who its actors are.
// The receiver is not modified.
func (g *Registry) Scale(k float64) (*Registry, error) {
	if !(k > 0) || math.IsInf(k, 0) {
		return nil, fmt.Errorf("scale factor must be finite and > 0, got %s", ff(k))
	}
	out := &Registry{byName: make(map[string]Profile, len(g.byName))}
	for name, p := range g.byName {
		if p.Rates.Base != nil {
			base := maps.Clone(p.Rates.Base)
			for i, v := range base {
				base[i] = v * k
			}
			p.Rates.Base = base
		}
		out.byName[name] = p
	}
	return out, nil
}
