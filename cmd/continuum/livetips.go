package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/poolwatch"
)

// liveTips follows every running pool of the network over node-to-node
// chain-sync and serves what it saw to the Pools API (httpapi.Tips) and to
// the fork monitor (engine.TipSource). The operator does not record tips:
// the fork monitor needs a window of blocks per pool, not one tip.
type liveTips struct {
	tr *poolwatch.Tracker
	cl poolCluster
	nw string
	// kube reads the network's topology for the Pools API.
	kube httpapi.Kube
}

// forkDepth is the number of blocks two pools must differ on for the Pools
// view to mark a fork; one block is a normal slot battle.
const forkDepth = 2

// poolView is the live Pools API's wiring; the zero value (a nil receiver)
// is a run without a network, whose view is empty.
func (l *liveTips) poolView(k uint64) poolView {
	if l == nil {
		return poolView{}
	}
	return poolView{Kube: l.kube, Tips: l, K: k, Depth: forkDepth}
}

// forkWindow is how many blocks of each pool's chain are kept: well beyond
// k, so a fork's depth is measured rather than only detected.
func forkWindow(k uint64) int { return int(2*k) + 16 }

func newLiveTips(cl poolCluster, network string, magic uint32, k uint64, log *slog.Logger) *liveTips {
	l := &liveTips{cl: cl, nw: network}
	targets := func(ctx context.Context) ([]poolwatch.Target, error) {
		ps, err := cl.Pools(ctx, network)
		return podTargets(ps), err
	}
	l.tr = poolwatch.New(poolwatch.N2N{Magic: magic}, targets,
		poolwatch.Config{Size: forkWindow(k), Refresh: 10 * time.Second, Backoff: 5 * time.Second}, log)
	return l
}

// podTargets lists the pools with a running pod, each at its pod's
// node-to-node address.
func podTargets(ps []v1alpha1.ContinuumPool) []poolwatch.Target {
	var out []poolwatch.Target
	for _, p := range ps {
		run := p.Status.Phase == v1alpha1.PhaseSyncing || p.Status.Phase == v1alpha1.PhaseForging
		if !run || p.Spec.Dark || p.Spec.Tier == v1alpha1.TierLogical || p.Status.Workload == "" {
			continue
		}
		w := p.Status.Workload
		host := fmt.Sprintf("%s-0.%s.%s.svc.%s", w, w, p.Namespace, operator.ClusterDomain)
		out = append(out, poolwatch.Target{Name: p.Name, Addr: fmt.Sprintf("%s:%d", host, operator.N2NPort)})
	}
	slices.SortFunc(out, func(a, b poolwatch.Target) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Tips implements httpapi.Tips.
func (l *liveTips) Tips() map[string][]chain.Mark { return l.tr.Tips() }

// Branches implements engine.TipSource.
func (l *liveTips) Branches(ctx context.Context) []oracle.Branch {
	ps, err := l.cl.Pools(ctx, l.nw)
	if err != nil {
		return nil
	}
	return branchesOf(ps, l.tr.Tips())
}

// branchesOf pairs each followed pool's blocks with its implementation and
// build from its ContinuumPool.
func branchesOf(ps []v1alpha1.ContinuumPool, tips map[string][]chain.Mark) []oracle.Branch {
	var out []oracle.Branch
	for _, p := range ps {
		ms := tips[p.Name]
		if len(ms) == 0 {
			continue
		}
		b := oracle.Branch{Node: oracle.Node{
			Impl: string(p.Spec.Implementation), Pool: p.Name, Digest: p.Status.Image.Digest,
			Dirty: p.Status.Image.Build.Dirty, Revisions: p.Status.Image.Build.Revisions,
		}}
		for _, m := range ms {
			b.Points = append(b.Points, m.Point)
		}
		out = append(out, b)
	}
	return out
}

// start runs the tracker until ctx ends or the returned stop is called.
func (l *liveTips) start(ctx context.Context, log *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := l.tr.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("pool tracker", "err", err)
		}
	}()
	return func() { cancel(); wg.Wait() }
}
