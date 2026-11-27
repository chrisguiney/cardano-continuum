package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// poolCluster is the cluster as a live run's pool plan uses it: the
// network's ContinuumPools, and a merge patch of one pool's spec.
type poolCluster interface {
	Pools(ctx context.Context, network string) ([]v1alpha1.ContinuumPool, error)
	PatchPool(ctx context.Context, name string, spec map[string]any) error
}

// livePools applies the engine's pool plan to the ContinuumPools of a
// network and reports their pods' health. Genesis pool i is the pool whose
// spec.poolId is fmt.Sprintf(format, i); pools the engine registered have
// no ContinuumPool and are left alone.
type livePools struct {
	cl      poolCluster
	network string
	format  string
	actors  map[int]uint64 // genesis index to pool actor
	every   sched.Time     // pod status poll interval in simulated time
	log     *slog.Logger

	mu      sync.Mutex // protects polled, running
	polled  sched.Time
	running map[uint64]bool
}

func newLivePools(cl poolCluster, network, format string, actors map[int]uint64, log *slog.Logger) *livePools {
	return &livePools{cl: cl, network: network, format: format, actors: actors, every: 10_000, log: log,
		running: map[uint64]bool{}, polled: -1 << 62}
}

// byIndex maps the network's ContinuumPools to genesis indexes.
func (l *livePools) byIndex(ctx context.Context) (map[int]v1alpha1.ContinuumPool, error) {
	ps, err := l.cl.Pools(ctx, l.network)
	if err != nil {
		return nil, err
	}
	out := map[int]v1alpha1.ContinuumPool{}
	for i := range l.actors {
		want := fmt.Sprintf(l.format, i)
		for _, p := range ps {
			if p.Spec.PoolID == want {
				out[i] = p
			}
		}
	}
	return out, nil
}

// Sync implements engine.PoolWriter: dark and image are patched where the
// pool's spec differs from the plan.
func (l *livePools) Sync(ctx context.Context, plan []engine.PoolPlan) error {
	pools, err := l.byIndex(ctx)
	if err != nil {
		return err
	}
	for _, want := range plan {
		p, ok := pools[want.Genesis]
		if !ok || want.Genesis < 0 {
			continue
		}
		spec := map[string]any{}
		if p.Spec.Dark != want.Dark {
			spec["dark"] = want.Dark
		}
		if want.Image != "" && p.Spec.Image != want.Image {
			spec["image"] = want.Image
		}
		if len(spec) == 0 {
			continue
		}
		if err := l.cl.PatchPool(ctx, p.Name, spec); err != nil {
			return err
		}
		l.log.Info("pool plan applied", "pool", p.Name, "dark", want.Dark, "image", want.Image)
	}
	return nil
}

// Changes implements engine.PodSource: every interval it reads the pools'
// phases and reports each genesis pool whose pod started or stopped
// running. Syncing, Forging and Following are running; Pending, Dark and anything else
// are not.
func (l *livePools) Changes(ctx context.Context, now sched.Time) ([]engine.PoolStatus, error) {
	l.mu.Lock()
	due := now-l.polled >= l.every
	l.mu.Unlock()
	if !due {
		return nil, nil
	}
	pools, err := l.byIndex(ctx)
	if err != nil {
		// A cluster read that fails reports nothing: the last known state
		// stands, and the next poll tries again.
		l.log.Warn("pool status", "err", err)
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.polled = now
	var out []engine.PoolStatus
	for i := range len(l.actors) {
		actor, ok := l.actors[i]
		p, seen := pools[i]
		if !ok || !seen {
			continue
		}
		run := p.Status.Phase.Running()
		if was, known := l.running[actor]; !known && run || known && was == run {
			l.running[actor] = run
			continue
		}
		l.running[actor] = run
		out = append(out, engine.PoolStatus{Pool: actor, Running: run})
	}
	return out, nil
}
