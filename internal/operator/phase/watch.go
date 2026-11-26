package phase

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
	"github.com/chrisguiney/cardano-continuum/internal/poolwatch"
)

const (
	// watchSize is the blocks kept per pool. Only the newest own block and
	// the tip matter, and the newest own block is also kept in the pool's
	// status, so the window need only span the blocks of a few polls.
	watchSize = 256
	// watchPoll is how often the targets are re-read and a running pool is
	// reconciled again to refresh its status.
	watchPoll = 5 * time.Second
	// keysTimeout bounds the API reads Observe makes.
	keysTimeout = 5 * time.Second
)

// WatchOptions configure NewWatch.
type WatchOptions struct {
	// Live reads genesis ConfigMaps and keys Secrets, which the operator's
	// own cache does not hold.
	Live client.Reader
	// Dial opens node-to-node connections for a network magic; nil dials
	// real nodes.
	Dial func(magic uint32) poolwatch.Dialer
	// Poll is the target re-read and status refresh interval; 0 is watchPoll.
	Poll time.Duration
	Log  *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Watch follows the chain of every running pool pod over node-to-node
// chain-sync, through one poolwatch.Tracker, and tells the pool reconciler
// what it saw. Reconcile only registers a pod (Track) and reads the latest
// snapshot (Observe); dialing and following happen in Run, so no reconcile
// waits on a node.
//
//	```mermaid
//	flowchart LR
//	  rec[Pools.Reconcile] -- Track / Forget --> targets
//	  targets --> tr[poolwatch.Tracker.Run]
//	  tr -- chain-sync, one follower per pod --> pods[(pool pods)]
//	  rec -- Observe --> snap[Tips snapshot]
//	  tr --> snap
//	  rec -- Judge --> phase[status.phase, tip, lastBlock]
//	```
type Watch struct {
	tr   *poolwatch.Tracker
	poll time.Duration
	now  func() time.Time
	// genesis and issuers are read through the live reader and cached.
	genesis genesisFacts
	issuers issuerCache

	mu sync.Mutex // protects targets, magic
	// targets are the pods being followed, by key.
	targets map[string]poolwatch.Target
	// magic is each target address's network magic.
	magic map[string]uint32
}

// NewWatch returns a Watch. Add it to the manager, which Starts it.
func NewWatch(o WatchOptions) *Watch {
	log := o.Log.With("component", "watch")
	w := &Watch{
		poll: o.Poll, now: o.Now,
		genesis: genesisFacts{live: o.Live, log: log, byKey: map[genesisKey]Facts{}},
		issuers: issuerCache{live: o.Live, log: log, byPod: map[types.UID]common.Blake2b224{}},
		targets: map[string]poolwatch.Target{}, magic: map[string]uint32{},
	}
	if w.poll <= 0 {
		w.poll = watchPoll
	}
	if w.now == nil {
		w.now = time.Now
	}
	open := o.Dial
	if open == nil {
		open = func(m uint32) poolwatch.Dialer { return poolwatch.N2N{Magic: m} }
	}
	w.tr = poolwatch.New(magicDialer{w: w, open: open}, w.list,
		poolwatch.Config{Size: watchSize, Refresh: w.poll, Backoff: w.poll}, o.Log)
	return w
}

// Start follows the tracked pods until ctx ends, then returns once every
// follower has exited. It makes Watch a manager Runnable.
func (w *Watch) Start(ctx context.Context) error {
	if err := w.tr.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("pool watch: %w", err)
	}
	return nil
}

// Poll is how long a reconcile of a followed pool should wait before
// refreshing its status.
func (w *Watch) Poll() time.Duration { return w.poll }

type magicDialer struct {
	w    *Watch
	open func(uint32) poolwatch.Dialer
}

func (d magicDialer) Dial(ctx context.Context, addr string) (poolwatch.Conn, error) {
	d.w.mu.Lock()
	m, ok := d.w.magic[addr]
	d.w.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no network magic for %s", addr)
	}
	return d.open(m).Dial(ctx, addr)
}

func (w *Watch) list(context.Context) ([]poolwatch.Target, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Collect(maps.Values(w.targets)), nil
}

// watchKey is "<namespace>/<network>/<pool>": pools of one network share a
// prefix, which NetTip uses.
func watchKey(p *v1alpha1.ContinuumPool) string {
	return p.Namespace + "/" + p.Spec.NetworkRef + "/" + p.Name
}

// Forget stops following pool p. It is a no-op for a pool not followed.
func (w *Watch) Forget(p *v1alpha1.ContinuumPool) { w.forget(watchKey(p)) }

// ForgetNamed is Forget for a pool known only by name, as after its
// deletion; it drops the pool under whichever network it was followed.
func (w *Watch) ForgetNamed(namespace, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k := range w.targets {
		if ns, rest, _ := strings.Cut(k, "/"); ns == namespace && strings.HasSuffix(rest, "/"+name) {
			w.dropLocked(k)
		}
	}
}

func (w *Watch) forget(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dropLocked(key)
}

func (w *Watch) dropLocked(key string) {
	if t, ok := w.targets[key]; ok {
		delete(w.magic, t.Addr)
		delete(w.targets, key)
	}
}

// Observation is what Observe learned about one pool.
type Observation struct {
	Facts Facts
	Seen  Seen
	// Last is the pool's newest own block: the one seen now, or the one
	// already in status when that is newer.
	Last v1alpha1.TipStatus
}

// Observe registers pod, the running pod of pool p on network net, for
// following and returns the latest snapshot of its chain. It never waits on
// a node: before the first block arrives the Seen has no tip.
func (w *Watch) Observe(ctx context.Context, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork, pod *corev1.Pod) Observation {
	key := watchKey(p)
	w.track(key, p, net)
	ctx, cancel := context.WithTimeout(ctx, keysTimeout)
	defer cancel()
	facts := w.genesis.of(ctx, net)
	var issuer *common.Blake2b224
	if render.RoleOf(p).Keyed() {
		if h, ok := w.issuers.of(ctx, p, pod); ok {
			issuer = &h
		}
	}
	tips := w.tr.Tips()
	o := Observation{Facts: facts, Last: p.Status.LastBlock}
	o.Seen = withPriorTip(seenOf(tips[key], issuer, netTip(tips, p.Namespace+"/"+p.Spec.NetworkRef+"/")), p.Status.Tip)
	if o.Seen.HaveOwn && int64(o.Seen.Own.Slot) >= o.Last.Slot {
		o.Last = TipStatus(o.Seen.Own)
	} else if o.Last.Slot > 0 {
		o.Seen.Own, o.Seen.HaveOwn = chain.Mark{Point: chain.Point{Slot: uint64(o.Last.Slot)}}, true
	}
	return o
}

// Phase is Judge at the Watch's clock.
func (w *Watch) Phase(base v1alpha1.PoolPhase, p *v1alpha1.ContinuumPool, o Observation) v1alpha1.PoolPhase {
	return Judge(base, render.RoleOf(p), o.Facts, o.Seen, w.now())
}

func (w *Watch) track(key string, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork) {
	set := render.Name(p)
	addr := fmt.Sprintf("%s.%s.%s.svc.%s:%d", render.PodName(set), set, p.Namespace, render.ClusterDomain, render.N2NPort)
	w.mu.Lock()
	defer w.mu.Unlock()
	if old, ok := w.targets[key]; ok && old.Addr != addr {
		delete(w.magic, old.Addr)
	}
	w.targets[key] = poolwatch.Target{Name: key, Addr: addr}
	w.magic[addr] = net.Spec.Magic
}

// seenOf folds one pool's window into a Seen. issuer is nil when the pool's
// cold-key hash is unknown or the pool does not forge.
func seenOf(marks []chain.Mark, issuer *common.Blake2b224, net uint64) Seen {
	s := Seen{NetTip: net}
	if len(marks) == 0 {
		return s
	}
	s.Tip, s.HaveTip = marks[len(marks)-1], true
	if issuer == nil {
		return s
	}
	for i := len(marks) - 1; i >= 0; i-- {
		if marks[i].Issuer == *issuer {
			s.Own, s.HaveOwn = marks[i], true
			break
		}
	}
	return s
}

// withPriorTip gives a Seen with no tip the tip already in status. A
// window is emptied by a rollback to a point older than it holds, and a pool
// that was caught up a moment ago must not read as Syncing for that; the lag
// check still turns a tip that stops advancing into Syncing.
func withPriorTip(s Seen, prior v1alpha1.TipStatus) Seen {
	if !s.HaveTip && prior.Slot > 0 {
		s.Tip, s.HaveTip = chain.Mark{Height: uint64(prior.Height), Point: chain.Point{Slot: uint64(prior.Slot)}}, true
	}
	return s
}

// netTip is the highest tip slot among the windows whose key has prefix.
func netTip(tips map[string][]chain.Mark, prefix string) uint64 {
	var best uint64
	for k, ms := range tips {
		if len(ms) > 0 && strings.HasPrefix(k, prefix) {
			best = max(best, ms[len(ms)-1].Slot)
		}
	}
	return best
}

// TipStatus is m as the status recorded on a pool.
func TipStatus(m chain.Mark) v1alpha1.TipStatus {
	return v1alpha1.TipStatus{Slot: int64(m.Slot), Hash: m.Hash.String(), Height: int64(m.Height)}
}
