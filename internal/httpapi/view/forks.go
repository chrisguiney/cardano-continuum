package view

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// poolChain is one pool's newest blocks, oldest first, never empty.
type poolChain struct {
	name  string
	marks []chain.Mark
}

func (c poolChain) tip() chain.Mark { return c.marks[len(c.marks)-1] }

// at is the block c holds at height h.
func (c poolChain) at(h uint64) (chain.Mark, bool) {
	for _, m := range c.marks {
		if m.Height == h {
			return m, true
		}
	}
	return chain.Mark{}, false
}

// poolChains takes each pool's followed blocks, or the single tip its
// status records (height unknown) when nothing follows it.
func poolChains(ps []v1alpha1.ContinuumPool, tips map[string][]chain.Mark) []poolChain {
	var out []poolChain
	for _, p := range ps {
		if ms := tips[p.Name]; len(ms) > 0 {
			out = append(out, poolChain{p.Name, ms})
			continue
		}
		var m chain.Mark
		raw, err := hex.DecodeString(p.Status.Tip.Hash)
		if err != nil || len(raw) != len(m.Hash) {
			continue
		}
		m.Slot = uint64(max(p.Status.Tip.Slot, 0))
		copy(m.Hash[:], raw)
		out = append(out, poolChain{p.Name, []chain.Mark{m}})
	}
	return out
}

// findForks compares the pools' chains. Pools are compared at the lowest
// tip height, so one that is merely behind on the same chain agrees. They
// have forked when they hold different blocks there on at least minDepth
// consecutive heights: a one-block split between competing leaders resolves
// itself. A pool more than k blocks below the furthest is reported as
// behind. When any pool's height is unknown the comparison is by tip slot.
func findForks(cs []poolChain, k, minDepth uint64) []Fork {
	out := []Fork{}
	if len(cs) < 2 {
		return out
	}
	lo, hi := cs[0].tip().Height, cs[0].tip().Height
	for _, c := range cs {
		lo, hi = min(lo, c.tip().Height), max(hi, c.tip().Height)
		if c.tip().Height == 0 {
			return slotForks(cs)
		}
	}
	if f, ok := diverged(cs, lo, minDepth); ok {
		out = append(out, f)
	}
	if hi-lo > k {
		out = append(out, behind(cs, hi, k))
	}
	return out
}

func diverged(cs []poolChain, lo, minDepth uint64) (Fork, bool) {
	depth := uint64(0)
	for h := lo; h >= 1; h-- {
		var first chain.Mark
		same, n := true, 0
		for _, c := range cs {
			m, ok := c.at(h)
			if !ok {
				continue
			}
			if n == 0 {
				first = m
			}
			n++
			same = same && m.Hash == first.Hash
		}
		if same {
			break
		}
		depth++
	}
	if depth == 0 || depth < minDepth {
		return Fork{}, false
	}
	byHash := map[string]*ForkGroup{}
	f := Fork{Height: lo, Depth: depth, Reason: ReasonDiverged}
	for _, c := range cs {
		m, ok := c.at(lo)
		if !ok {
			continue
		}
		h := hex.EncodeToString(m.Hash[:])
		if byHash[h] == nil {
			byHash[h] = &ForkGroup{Hash: h}
			f.Slot = max(f.Slot, m.Slot)
		}
		byHash[h].Nodes = append(byHash[h].Nodes, c.name)
	}
	f.Groups = groups(byHash)
	return f, len(f.Groups) > 1
}

func behind(cs []poolChain, hi, k uint64) Fork {
	lag := &ForkGroup{}
	lead := &ForkGroup{}
	f := Fork{Reason: ReasonBehind}
	for _, c := range cs {
		t := c.tip()
		g := lead
		if t.Height+k < hi {
			g = lag
		}
		if g.Hash == "" {
			g.Hash = hex.EncodeToString(t.Hash[:])
		}
		g.Nodes = append(g.Nodes, c.name)
		if g == lag {
			f.Height = max(f.Height, t.Height)
		}
	}
	f.Slot = 0
	f.Groups = []ForkGroup{*lag, *lead}
	return f
}

func groups(m map[string]*ForkGroup) []ForkGroup {
	out := make([]ForkGroup, 0, len(m))
	for _, g := range m {
		slices.Sort(g.Nodes)
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b ForkGroup) int { return strings.Compare(a.Hash, b.Hash) })
	return out
}

// slotForks reports each tip slot where the pools that report a hash do
// not all agree: the only comparison available without block numbers.
func slotForks(cs []poolChain) []Fork {
	bySlot := map[uint64]map[string]*ForkGroup{}
	for _, c := range cs {
		t := c.tip()
		h := hex.EncodeToString(t.Hash[:])
		if bySlot[t.Slot] == nil {
			bySlot[t.Slot] = map[string]*ForkGroup{}
		}
		if bySlot[t.Slot][h] == nil {
			bySlot[t.Slot][h] = &ForkGroup{Hash: h}
		}
		g := bySlot[t.Slot][h]
		g.Nodes = append(g.Nodes, c.name)
	}
	out := []Fork{}
	for slot, hashes := range bySlot {
		if len(hashes) > 1 {
			out = append(out, Fork{Slot: slot, Reason: ReasonDiverged, Groups: groups(hashes)})
		}
	}
	slices.SortFunc(out, func(a, b Fork) int { return int(a.Slot) - int(b.Slot) })
	return out
}
