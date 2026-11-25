// Package imageset plans `continuum image set`: which ContinuumNetwork or
// ContinuumPools receive a new image reference. Planning is pure; the
// command applies the Changes it returns.
package imageset

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// Kind says what a Change patches.
type Kind string

const (
	// KindNetwork patches ContinuumNetwork.spec.images[impl].
	KindNetwork Kind = "network"
	// KindPool patches ContinuumPool.spec.image.
	KindPool Kind = "pool"
)

// Change is one patch: Name's image goes from From to To.
type Change struct {
	Kind   Kind
	Name   string
	PoolID string
	Tier   v1alpha1.Tier
	Impl   v1alpha1.Implementation
	From   string
	To     string
}

// Selector narrows a change to pools. The zero Selector changes the
// network default instead. PoolID is exclusive with Percent; Tier combines
// with Percent, which then picks from the pools of that tier.
type Selector struct {
	// Percent selects the first Percent% of the matching pools, in poolId
	// order, rounded up so a nonzero percentage selects at least one.
	Percent int
	// PoolID selects every pool with that PoolID.
	PoolID string
	// Tier selects pools of one tier.
	Tier v1alpha1.Tier
}

var impls = []v1alpha1.Implementation{v1alpha1.Dingo, v1alpha1.CardanoNode, v1alpha1.Amaru, v1alpha1.Rogue}

var tiers = []v1alpha1.Tier{v1alpha1.TierLogical, v1alpha1.TierValidating, v1alpha1.TierForging}

// ParseAssignment splits "<impl>=<ref>".
func ParseAssignment(arg string) (v1alpha1.Implementation, string, error) {
	name, ref, ok := strings.Cut(arg, "=")
	impl := v1alpha1.Implementation(name)
	if !ok || ref == "" || !slices.Contains(impls, impl) {
		return "", "", fmt.Errorf("want <impl>=<ref> with impl one of %v, got %q", impls, arg)
	}
	return impl, ref, nil
}

// ParsePercent reads "N%" with N a whole number from 1 to 100.
func ParsePercent(s string) (int, error) {
	num, ok := strings.CutSuffix(s, "%")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n < 1 || n > 100 {
		return 0, fmt.Errorf("want a whole percentage from 1%% to 100%%, got %q", s)
	}
	return n, nil
}

func (s Selector) zero() bool { return s == Selector{} }

func (s Selector) validate() error {
	switch {
	case s.PoolID != "" && s.Percent != 0:
		return fmt.Errorf("a pool id and a percentage select differently; give one")
	case s.Percent < 0 || s.Percent > 100:
		return fmt.Errorf("percentage %d outside 1..100", s.Percent)
	case s.Tier != "" && !slices.Contains(tiers, s.Tier):
		return fmt.Errorf("unknown tier %q, want one of %v", s.Tier, tiers)
	}
	return nil
}

// Plan returns the patches that give impl the image ref. With the zero
// Selector it is the network's default. Otherwise it is the pools of impl
// that match, ordered by poolId then name, cut to the first Percent%, so
// a re-run of the same command selects the same pools.
func Plan(net *v1alpha1.ContinuumNetwork, pools []v1alpha1.ContinuumPool, impl v1alpha1.Implementation, ref string, sel Selector) ([]Change, error) {
	if !slices.Contains(impls, impl) {
		return nil, fmt.Errorf("unknown implementation %q", impl)
	}
	if err := sel.validate(); err != nil {
		return nil, err
	}
	if sel.zero() {
		return []Change{{Kind: KindNetwork, Name: net.Name, Impl: impl, From: net.Spec.Images[impl], To: ref}}, nil
	}
	match := slices.DeleteFunc(slices.Clone(pools), func(p v1alpha1.ContinuumPool) bool {
		return p.Spec.Implementation != impl ||
			(sel.PoolID != "" && p.Spec.PoolID != sel.PoolID) ||
			(sel.Tier != "" && p.Spec.Tier != sel.Tier)
	})
	if len(match) == 0 {
		return nil, fmt.Errorf("no %s pool matches the selection", impl)
	}
	slices.SortFunc(match, func(a, b v1alpha1.ContinuumPool) int {
		return cmp.Or(strings.Compare(a.Spec.PoolID, b.Spec.PoolID), strings.Compare(a.Name, b.Name))
	})
	if sel.Percent > 0 {
		match = match[:(len(match)*sel.Percent+99)/100]
	}
	out := make([]Change, len(match))
	for i, p := range match {
		out[i] = Change{Kind: KindPool, Name: p.Name, PoolID: p.Spec.PoolID, Tier: p.Spec.Tier, Impl: impl, From: p.Spec.Image, To: ref}
	}
	return out, nil
}

// Write prints cs, one line each. A dry run says nothing was applied.
func Write(w io.Writer, cs []Change, dry bool) error {
	for _, c := range cs {
		var err error
		if c.Kind == KindNetwork {
			_, err = fmt.Fprintf(w, "network %s: images[%s] %q -> %q\n", c.Name, c.Impl, c.From, c.To)
		} else {
			_, err = fmt.Fprintf(w, "pool %s (%s, %s, %s): image %q -> %q\n", c.Name, c.PoolID, c.Impl, c.Tier, c.From, c.To)
		}
		if err != nil {
			return err
		}
	}
	verb := "applied"
	if dry {
		verb = "dry run, nothing applied"
	}
	_, err := fmt.Fprintf(w, "%d change(s), %s\n", len(cs), verb)
	return err
}
