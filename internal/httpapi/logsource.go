package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Names reads the actor names and roles the store's read model holds.
// *store.DB satisfies it.
type Names interface {
	Actors(ctx context.Context, q store.ActorQuery) ([]store.ActorRow, error)
}

// Kube reads the operator's view of the network: its peer policy and the
// ContinuumPool objects with their observed status.
type Kube interface {
	Topology(ctx context.Context) (v1alpha1.TopologyMode, error)
	Pools(ctx context.Context) ([]v1alpha1.ContinuumPool, error)
}

// namesTTL is how long a read of the store's actor table is reused.
const namesTTL = 5 * time.Second

// LogSourceConfig wires a LogSource. Names, Kube, Slots and Now are
// optional: without them names are empty, the pool view is empty, the epoch
// is unset, and the clock is time.Now.
type LogSourceConfig struct {
	Projection *Projection
	Hub        *Hub
	Names      Names
	Kube       Kube
	Slots      *sched.Slots
	Now        func() time.Time
}

// LogSource is the read-only Source over an event log's projection, the
// store's actor table and the operator's pool status.
type LogSource struct {
	cfg LogSourceConfig

	mu     sync.Mutex // protects cached, cachedAt
	cached map[uint64]store.ActorRow
	cacheT time.Time
}

// NewLogSource returns a LogSource.
func NewLogSource(cfg LogSourceConfig) *LogSource {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LogSource{cfg: cfg}
}

// Snapshot implements Source.
func (s *LogSource) Snapshot(ctx context.Context) (Snapshot, error) {
	n, err := s.names(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap := s.cfg.Projection.Snapshot(s.cfg.Slots)
	snap.Population.ByProfile = s.cfg.Projection.ProfileCounts(func(id uint64) string { return n[id].Profile })
	return snap, nil
}

// ActorTimeline implements Source.
func (s *LogSource) ActorTimeline(ctx context.Context, id uint64) (Timeline, error) {
	t, err := s.cfg.Projection.Timeline(id)
	if err != nil {
		return t, err
	}
	if n, err := s.names(ctx); err == nil {
		t.Role = n[id].Role
		if t.Profile == "" {
			t.Profile = n[id].Profile
		}
	}
	return t, nil
}

// Findings implements Source.
func (s *LogSource) Findings(_ context.Context, p Page) (FindingPage, error) {
	return FindingPage{Offset: p.Offset, Limit: p.Limit, Rows: []Finding{}}, nil
}

// Subscribe implements Source.
func (s *LogSource) Subscribe(f Filter, after *uint64) (*Subscription, error) {
	return s.cfg.Hub.Subscribe(f, after)
}

// Pools implements Source.
func (s *LogSource) Pools(ctx context.Context) (PoolsView, error) {
	if s.cfg.Kube == nil {
		return buildPools(nil, ""), nil
	}
	mode, err := s.cfg.Kube.Topology(ctx)
	if err != nil {
		return PoolsView{}, fmt.Errorf("read topology: %w", err)
	}
	ps, err := s.cfg.Kube.Pools(ctx)
	if err != nil {
		return PoolsView{}, fmt.Errorf("list pools: %w", err)
	}
	return buildPools(ps, mode), nil
}

// Actors implements Source. The log decides which actors exist; the store
// adds their name and role.
func (s *LogSource) Actors(ctx context.Context, q ActorQuery) (ActorPage, error) {
	if !slices.Contains(SortKeys, q.Sort) {
		return ActorPage{}, fmt.Errorf("%w: unknown sort key %q", ErrBadRequest, q.Sort)
	}
	if q.Limit < 1 || q.Limit > MaxLimit || q.Offset < 0 {
		return ActorPage{}, fmt.Errorf("%w: page limit %d offset %d", ErrBadRequest, q.Limit, q.Offset)
	}
	rows := s.cfg.Projection.Rows()
	n, err := s.names(ctx)
	if err != nil {
		return ActorPage{}, err
	}
	for i := range rows {
		rows[i].Name, rows[i].Role = n[rows[i].ID].Name, n[rows[i].ID].Role
		if rows[i].Profile == "" {
			rows[i].Profile = n[rows[i].ID].Profile
		}
	}
	return pageActors(rows, q), nil
}

// names returns the store's actor table by id, reusing a read for namesTTL.
func (s *LogSource) names(ctx context.Context) (map[uint64]store.ActorRow, error) {
	if s.cfg.Names == nil {
		return nil, nil
	}
	s.mu.Lock()
	if s.cached != nil && s.cfg.Now().Sub(s.cacheT) < namesTTL {
		c := s.cached
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	all := map[uint64]store.ActorRow{}
	for off := 0; ; off += store.MaxLimit {
		page, err := s.cfg.Names.Actors(ctx, store.ActorQuery{SortBy: "id", Offset: off, Limit: store.MaxLimit})
		if err != nil {
			return nil, fmt.Errorf("read actor names: %w", err)
		}
		for _, r := range page {
			all[r.ID] = r
		}
		if len(page) < store.MaxLimit {
			break
		}
	}
	s.mu.Lock()
	s.cached, s.cacheT = all, s.cfg.Now()
	s.mu.Unlock()
	return all, nil
}

// pageActors filters, sorts and slices rows. Sorting is stable on id, so a
// page boundary never repeats or skips a row.
func pageActors(rows []ActorRow, q ActorQuery) ActorPage {
	rows = slices.DeleteFunc(rows, func(r ActorRow) bool {
		f := q.Filter
		return (f.Profile != "" && r.Profile != f.Profile) ||
			(f.State != "" && r.State != f.State) ||
			(f.Role != "" && r.Role != f.Role)
	})
	slices.SortStableFunc(rows, func(a, b ActorRow) int {
		c := compareActors(q.Sort, a, b)
		if q.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return cmpUint(a.ID, b.ID)
	})
	out := ActorPage{Total: len(rows), Offset: q.Offset, Limit: q.Limit, Rows: []ActorRow{}}
	if q.Offset < len(rows) {
		out.Rows = rows[q.Offset:min(q.Offset+q.Limit, len(rows))]
	}
	return out
}

func compareActors(key string, a, b ActorRow) int {
	switch key {
	case "name":
		return strings.Compare(a.Name, b.Name)
	case "role":
		return strings.Compare(a.Role, b.Role)
	case "profile":
		return strings.Compare(a.Profile, b.Profile)
	case "state":
		return strings.Compare(a.State, b.State)
	case "intents":
		return a.Intents - b.Intents
	case "txs":
		return a.Txs - b.Txs
	case "retries":
		return a.Retries - b.Retries
	case "confirmed":
		return a.Confirmed - b.Confirmed
	case "released":
		return a.Released - b.Released
	}
	return cmpUint(a.ID, b.ID)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

var (
	_ Source = (*LogSource)(nil)
	_ Names  = (*store.DB)(nil)
)
