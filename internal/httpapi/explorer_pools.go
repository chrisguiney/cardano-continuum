package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/explorer"
)

// poolsTTL is how long the pool table is reused.
const poolsTTL = 3 * time.Second

// poolTable maps a pool id (hex) to the continuum pool that forges with it,
// from the operator's pools and the followed chains' block issuers.
type poolTable struct {
	kube Kube
	tip  Tips
	now  func() time.Time

	mu     sync.Mutex // protects table, readAt
	table  map[string]explorer.PoolRef
	readAt time.Time
}

func newPoolTable(k Kube, t Tips, now func() time.Time) *poolTable {
	return &poolTable{kube: k, tip: t, now: now, table: map[string]explorer.PoolRef{}}
}

// refresh rereads the table when it is older than poolsTTL.
func (t *poolTable) refresh(ctx context.Context) error {
	t.mu.Lock()
	fresh := t.now().Sub(t.readAt) < poolsTTL
	t.mu.Unlock()
	if fresh || t.kube == nil && t.tip == nil {
		return nil
	}
	table := map[string]explorer.PoolRef{}
	byName, err := t.fromKube(ctx, table)
	if err != nil {
		return err
	}
	t.fromTips(table, byName)
	t.mu.Lock()
	t.table, t.readAt = table, t.now()
	t.mu.Unlock()
	return nil
}

// fromKube adds the pools whose spec names a pool id and returns each
// pool's implementation by name.
func (t *poolTable) fromKube(ctx context.Context, table map[string]explorer.PoolRef) (map[string]string, error) {
	byName := map[string]string{}
	if t.kube == nil {
		return byName, nil
	}
	ps, err := t.kube.Pools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	for _, p := range ps {
		ref := explorer.PoolRef{Name: p.Name, Impl: string(p.Spec.Implementation)}
		byName[p.Name] = ref.Impl
		if isPoolHex(p.Spec.PoolID) {
			table[strings.ToLower(p.Spec.PoolID)] = ref
		}
	}
	return byName, nil
}

// fromTips adds each block issuer a followed chain shows.
func (t *poolTable) fromTips(table map[string]explorer.PoolRef, byName map[string]string) {
	if t.tip == nil {
		return
	}
	for name, marks := range t.tip.Tips() {
		for _, m := range marks {
			if m.Issuer != ([28]byte{}) {
				table[hex.EncodeToString(m.Issuer[:])] = explorer.PoolRef{Name: name, Impl: byName[name]}
			}
		}
	}
}

func isPoolHex(s string) bool { return len(s) == 56 && explorer.Classify(s) == explorer.KindPool }

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

// poolID finds the pool id (hex) a search key names: a hex id, or a
// continuum pool's name.
func (t *poolTable) poolID(q string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.table[strings.ToLower(q)]; ok && p.Name != "" {
		return strings.ToLower(q), true
	}
	for id, p := range t.table {
		if p.Name == q {
			return id, true
		}
	}
	return "", false
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
