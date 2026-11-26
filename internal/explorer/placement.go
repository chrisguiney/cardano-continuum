package explorer

import (
	"slices"
	"strings"
)

// Tip is a block on a pool's followed chain.
type Tip struct {
	Height uint64
	Hash   string
}

// Placed is where one pool's chain stands against a block: State is "on"
// (it has the block), "fork" (a different block at that height; Hash is
// which), "behind" (it has not reached the height) or "unknown" (the height
// is older than its retained window).
type Placed struct {
	Pool    string `json:"pool"`
	State   string `json:"state"`
	OnBlock bool   `json:"on_block"`
	Hash    string `json:"hash,omitempty"`
}

// Placement reports, for each pool, whether its chain holds the block at
// height with this hash. Pure.
func Placement(height uint64, hash string, tips map[string][]Tip) []Placed {
	out := make([]Placed, 0, len(tips))
	for pool, ts := range tips {
		out = append(out, place(pool, height, hash, ts))
	}
	slices.SortFunc(out, func(a, b Placed) int { return strings.Compare(a.Pool, b.Pool) })
	return out
}

func place(pool string, height uint64, hash string, ts []Tip) Placed {
	var top uint64
	for _, t := range ts {
		top = max(top, t.Height)
		if t.Height != height {
			continue
		}
		if t.Hash == hash {
			return Placed{Pool: pool, State: "on", OnBlock: true, Hash: t.Hash}
		}
		return Placed{Pool: pool, State: "fork", Hash: t.Hash}
	}
	if top < height {
		return Placed{Pool: pool, State: "behind"}
	}
	return Placed{Pool: pool, State: "unknown"}
}
