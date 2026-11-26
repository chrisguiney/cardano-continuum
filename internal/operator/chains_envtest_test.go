//go:build envtest

package operator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator/phase"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
	"github.com/chrisguiney/cardano-continuum/internal/poolwatch"
)

// fakeChain serves each pod a chain of blocks, one every 50ms at the clock's
// slot, issued by who[workload].
type fakeChain struct {
	start time.Time
	// open counts connections dialed and not yet closed.
	open atomic.Int32
	mu   sync.Mutex // protects who
	who  map[string]common.Blake2b224
}

func (f *fakeChain) Dial(_ context.Context, addr string) (poolwatch.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for set, issuer := range f.who {
		if strings.HasPrefix(addr, render.PodName(set)+".") {
			f.open.Add(1)
			return &fakeNode{f: f, issuer: issuer}, nil
		}
	}
	return nil, fmt.Errorf("no fake node at %s", addr)
}

type fakeNode struct {
	f      *fakeChain
	issuer common.Blake2b224
	height uint64
}

func (n *fakeNode) mark(issuer common.Blake2b224) chain.Mark {
	n.height++
	slot := uint64(time.Since(n.f.start) / time.Second)
	return chain.Mark{Height: n.height, Point: chain.Point{Slot: slot, Hash: common.Blake2b256{byte(n.height)}}, Issuer: issuer}
}

func (n *fakeNode) Start(context.Context) (chain.Mark, error) {
	return n.mark(common.Blake2b224{}), nil
}

func (n *fakeNode) Next(ctx context.Context) (poolwatch.Event, error) {
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return poolwatch.Event{}, ctx.Err()
	case <-t.C:
		m := n.mark(n.issuer)
		return poolwatch.Event{Forward: &m}, nil
	}
}

func (n *fakeNode) Close() error { n.f.open.Add(-1); return nil }

// chainHarness starts the operator with fake nodes. The genesis has f=0.5
// and 1s slots, so a pod is caught up within 12 slots of the clock and a
// pool with the whole stake must have forged within 14.
func chainHarness(t *testing.T) (*harness, *fakeChain) {
	t.Helper()
	h := newHarness(t, nil)
	fc := &fakeChain{start: time.Now(), who: map[string]common.Blake2b224{}}
	doc := fmt.Sprintf(`{"activeSlotsCoeff":0.5,"epochLength":500,"securityParam":10,"slotLength":1,
"systemStart":%q,"staking":{"pools":{"aa":{}},"stake":{}}}`, fc.start.UTC().Format(time.RFC3339))
	var cm corev1.ConfigMap
	h.must(h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: "genesis"}, &cm))
	cm.Data[genesis.ShelleyFile] = doc
	h.must(h.c.Update(h.ctx, &cm))
	return h, fc
}

func (h *harness) startChains(fc *fakeChain) {
	h.t.Helper()
	mgr := h.manager()
	h.must(Setup(h.ctx, mgr, Options{Drivers: render.Drivers(), Images: fakeResolver{}, Log: quiet(), Poll: 100 * time.Millisecond,
		Dial: func(uint32) poolwatch.Dialer { return fc }}))
	h.run("manager", mgr.Start)
}

func fixtureOpCert(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../poolwatch/testdata/opcert.cert")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func fixtureIssuer(t *testing.T) common.Blake2b224 {
	t.Helper()
	id, err := phase.IssuerOf(string(fixtureText(t, "poolid.hex")), nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestForgerThatForgesReportsForging(t *testing.T) {
	h, fc := chainHarness(t)
	h.secret("a-keys", fixtureOpCert(t))
	p := h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.PoolID = "continuum-pool-0" })
	set := render.Name(p)
	fc.who[set] = fixtureIssuer(t)
	h.startChains(fc)
	h.running(render.PodName(set), v1alpha1.CardanoNode)
	h.eventually("Forging with a tip and a last block", func() bool {
		s := h.get("a").Status
		return s.Phase == v1alpha1.PhaseForging && s.Tip.Slot > 0 && s.LastBlock.Slot > 0 && s.LastBlock.Hash != "" && s.LastBlock.Height > 0
	})
}

func TestForgerThatNeverForgesStaysSyncing(t *testing.T) {
	h, fc := chainHarness(t)
	h.secret("a-keys", fixtureOpCert(t))
	p := h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.PoolID = "continuum-pool-0" })
	set := render.Name(p)
	fc.who[set] = common.Blake2b224{9}
	h.startChains(fc)
	h.running(render.PodName(set), v1alpha1.CardanoNode)
	h.eventually("tip observed", func() bool { return h.get("a").Status.Tip.Slot > 0 })
	h.consistently("Syncing: the blocks are another pool's", 1500*time.Millisecond, func() bool {
		s := h.get("a").Status
		return s.Phase == v1alpha1.PhaseSyncing && s.LastBlock.Slot == 0
	})
}

func TestRelayThatIsCaughtUpIsFollowing(t *testing.T) {
	h, fc := chainHarness(t)
	p := h.pool("r", v1alpha1.CardanoNode, v1alpha1.TierValidating, nil)
	set := render.Name(p)
	fc.who[set] = common.Blake2b224{9}
	h.startChains(fc)
	h.running(render.PodName(set), v1alpha1.CardanoNode)
	h.eventually("Following", func() bool { return h.get("r").Status.Phase == v1alpha1.PhaseFollowing })
}

func TestDarkPoolStopsBeingFollowed(t *testing.T) {
	h, fc := chainHarness(t)
	p := h.pool("r", v1alpha1.CardanoNode, v1alpha1.TierValidating, nil)
	set := render.Name(p)
	fc.who[set] = common.Blake2b224{9}
	h.startChains(fc)
	h.running(render.PodName(set), v1alpha1.CardanoNode)
	h.eventually("Following", func() bool { return h.get("r").Status.Phase == v1alpha1.PhaseFollowing })
	h.edit("r", func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = true })
	h.eventually("Dark", func() bool { return h.get("r").Status.Phase == v1alpha1.PhaseDark })
	h.eventually("connection closed", func() bool { return fc.open.Load() == 0 })
}

func fixtureText(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../poolwatch/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(raw)))
}
