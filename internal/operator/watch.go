package operator

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
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
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
	live client.Reader
	poll time.Duration
	log  *slog.Logger
	now  func() time.Time

	mu sync.Mutex // protects targets, magic, genesis, issuers
	// targets are the pods being followed, by key.
	targets map[string]poolwatch.Target
	// magic is each target address's network magic.
	magic   map[string]uint32
	genesis map[genesisKey]Facts
	// issuers caches a pod's cold-key hash by pod UID: the keys do not
	// change while the pod runs.
	issuers map[types.UID]common.Blake2b224
}

type genesisKey struct {
	net types.UID
	ref string
}

// NewWatch returns a Watch. Add it to the manager, which Starts it.
func NewWatch(o WatchOptions) *Watch {
	w := &Watch{
		live: o.Live, poll: o.Poll, log: o.Log.With("component", "watch"), now: o.Now,
		targets: map[string]poolwatch.Target{}, magic: map[string]uint32{},
		genesis: map[genesisKey]Facts{}, issuers: map[types.UID]common.Blake2b224{},
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
	facts := w.facts(ctx, net)
	var issuer *common.Blake2b224
	if RoleOf(p).Keyed() {
		if h, ok := w.issuer(ctx, p, pod); ok {
			issuer = &h
		}
	}
	tips := w.tr.Tips()
	o := Observation{Facts: facts, Last: p.Status.LastBlock}
	o.Seen = seenOf(tips[key], issuer, netTip(tips, p.Namespace+"/"+p.Spec.NetworkRef+"/"))
	if o.Seen.HaveOwn && int64(o.Seen.Own.Slot) >= o.Last.Slot {
		o.Last = tipStatus(o.Seen.Own)
	} else if o.Last.Slot > 0 {
		o.Seen.Own, o.Seen.HaveOwn = chain.Mark{Point: chain.Point{Slot: uint64(o.Last.Slot)}}, true
	}
	return o
}

// Phase is Judge at the Watch's clock.
func (w *Watch) Phase(base v1alpha1.PoolPhase, p *v1alpha1.ContinuumPool, o Observation) v1alpha1.PoolPhase {
	return Judge(base, RoleOf(p), o.Facts, o.Seen, w.now())
}

func (w *Watch) track(key string, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork) {
	set := Name(p)
	addr := fmt.Sprintf("%s.%s.%s.svc.%s:%d", PodName(set), set, p.Namespace, ClusterDomain, N2NPort)
	w.mu.Lock()
	defer w.mu.Unlock()
	if old, ok := w.targets[key]; ok && old.Addr != addr {
		delete(w.magic, old.Addr)
	}
	w.targets[key] = poolwatch.Target{Name: key, Addr: addr}
	w.magic[addr] = net.Spec.Magic
}

// facts reads the network's genesis once per network and genesis
// reference. A failed read is not cached, so the next reconcile retries and
// meanwhile the phase uses the best tip as the clock.
func (w *Watch) facts(ctx context.Context, net *v1alpha1.ContinuumNetwork) Facts {
	k := genesisKey{net: net.UID, ref: net.Spec.GenesisRef}
	w.mu.Lock()
	f, ok := w.genesis[k]
	w.mu.Unlock()
	if ok {
		return f
	}
	var cm corev1.ConfigMap
	err := w.live.Get(ctx, types.NamespacedName{Namespace: net.Namespace, Name: net.Spec.GenesisRef}, &cm)
	if err == nil {
		f, err = FactsOf([]byte(cm.Data[genesis.ShelleyFile]))
	}
	if err != nil {
		w.log.Warn("network facts unavailable, using the best tip as the clock", "network", net.Name, "err", err)
		return Facts{Epoch: uint64(max(net.Spec.EpochLength, 0)), K: uint64(max(net.Spec.SecurityParam, 0))}
	}
	f.Epoch, f.K = uint64(max(net.Spec.EpochLength, 0)), uint64(max(net.Spec.SecurityParam, 0))
	w.mu.Lock()
	w.genesis[k] = f
	w.mu.Unlock()
	return f
}

// issuer is the cold-key hash of the pool in pod, read once per pod.
func (w *Watch) issuer(ctx context.Context, p *v1alpha1.ContinuumPool, pod *corev1.Pod) (common.Blake2b224, bool) {
	w.mu.Lock()
	h, ok := w.issuers[pod.UID]
	w.mu.Unlock()
	if ok {
		return h, true
	}
	h, err := IssuerOf(p.Spec.PoolID, nil)
	if err != nil {
		var s corev1.Secret
		if err := w.live.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.KeysSecret}, &s); err != nil {
			w.log.Warn("read keys secret", "pool", p.Name, "err", err)
			return h, false
		}
		h, err = IssuerOf(p.Spec.PoolID, s.Data)
	}
	if err != nil {
		w.log.Warn("pool issuer unknown", "pool", p.Name, "err", err)
		return h, false
	}
	w.mu.Lock()
	w.issuers[pod.UID] = h
	w.mu.Unlock()
	return h, true
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

func tipStatus(m chain.Mark) v1alpha1.TipStatus {
	return v1alpha1.TipStatus{Slot: int64(m.Slot), Hash: m.Hash.String(), Height: int64(m.Height)}
}
