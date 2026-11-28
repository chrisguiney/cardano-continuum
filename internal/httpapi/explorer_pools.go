package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
)

// poolsTTL is how long the pool table is reused.
const poolsTTL = 3 * time.Second

// poolTable maps a pool id (hex) to the node that forges with its keys
// (view.NodeKeys), and lists the nodes by name.
type poolTable struct {
	kube Kube
	tip  Tips
	now  func() time.Time
	// issuerOf is the issuer (hex pool id) of a block the index holds.
	issuerOf func(hash string) (string, bool)

	mu     sync.Mutex // protects table, nodes, readAt
	table  map[string]explorer.PoolRef
	nodes  map[string]explorer.PoolRef
	readAt time.Time
}

func newPoolTable(k Kube, t Tips, now func() time.Time, issuerOf func(string) (string, bool)) *poolTable {
	return &poolTable{
		kube:     k,
		tip:      t,
		now:      now,
		issuerOf: issuerOf,
		table:    map[string]explorer.PoolRef{},
		nodes:    map[string]explorer.PoolRef{},
	}
}

// refresh rereads the table when it is older than poolsTTL. It runs
// without mu, so the index it reads may call resolve.
func (t *poolTable) refresh(ctx context.Context) error {
	t.mu.Lock()
	fresh := t.now().Sub(t.readAt) < poolsTTL
	t.mu.Unlock()
	if fresh || t.kube == nil {
		return nil
	}
	ps, err := t.kube.Pools(ctx)
	if err != nil {
		return fmt.Errorf("list pools: %w", err)
	}

	var tips map[string][]chain.Mark
	if t.tip != nil {
		tips = t.tip.Tips()
	}
	keys := view.NodeKeys(ps, tips, t.issuerOf)
	slices.SortFunc(ps, func(a, b v1alpha1.ContinuumPool) int { return strings.Compare(a.Name, b.Name) })
	table, nodes := map[string]explorer.PoolRef{}, map[string]explorer.PoolRef{}
	for _, p := range ps {
		ref := explorer.PoolRef{Name: p.Name, Impl: string(p.Spec.Implementation)}
		nodes[p.Name] = ref
		// Two nodes on one key is equivocation; the first by name keeps
		// the label so it does not change between reads.
		if id, ok := keys[p.Name]; ok && table[id].Name == "" {
			table[id] = ref
		}
	}

	t.mu.Lock()
	t.table, t.nodes, t.readAt = table, nodes, t.now()
	t.mu.Unlock()
	return nil
}

func (t *poolTable) resolve(id string) (explorer.PoolRef, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.table[id]
	return p, ok
}

func (t *poolTable) known() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.table))
	for id := range t.table {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// poolID reports the pool id (hex) q names when a node forges with it.
func (t *poolTable) poolID(q string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.table[strings.ToLower(q)]; ok && p.Name != "" {
		return strings.ToLower(q), true
	}
	return "", false
}

// node reports that q names a node.
func (t *poolTable) node(q string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.nodes[q]
	return ok
}

// tips is each pool's followed chain as heights and hashes.
func (t *poolTable) tips() map[string][]explorer.Tip {
	out := map[string][]explorer.Tip{}
	if t.tip == nil {
		return out
	}
	for name, marks := range t.tip.Tips() {
		for _, m := range marks {
			out[name] = append(out[name], explorer.Tip{Height: m.Height, Hash: hex.EncodeToString(m.Hash[:])})
		}
	}
	return out
}
