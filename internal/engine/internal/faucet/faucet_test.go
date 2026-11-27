package faucet

import (
	"context"
	"errors"
	"iter"
	"maps"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const faucetActor = population.ActorID(9000)

type fakeClock struct{ at []sched.Time }

func (c *fakeClock) Refill(at sched.Time) { c.at = append(c.at, at) }

type emitted struct {
	kind string
	body any
}

type fakeJournal struct{ got []emitted }

func (j *fakeJournal) Emit(_ sched.Time, kind string, _ population.ActorID, body any) {
	j.got = append(j.got, emitted{kind, body})
}

type fakeBook struct {
	pending map[uint64]*wire.Pending
	next    uint64
	coins   []wire.Coin
	held    map[population.ActorID]uint64
	unawait []uint64
	refused bool
}

func (b *fakeBook) Next() uint64                                        { return b.next + 1 }
func (b *fakeBook) Claim(id uint64)                                     { b.next = id }
func (b *fakeBook) Coins(population.ActorID, plan.IntentID) []wire.Coin { return b.coins }
func (b *fakeBook) Reserve(context.Context, uint64, population.ActorID, wire.TxID, []wire.Ref, uint64) error {
	return nil
}
func (b *fakeBook) Add(p *wire.Pending)               { b.pending[p.ID] = p }
func (b *fakeBook) Unawait(id uint64)                 { b.unawait = append(b.unawait, id) }
func (b *fakeBook) All() iter.Seq[*wire.Pending]      { return maps.Values(b.pending) }
func (b *fakeBook) Held(id population.ActorID) uint64 { return b.held[id] }
func (b *fakeBook) Refuse(context.Context, sched.Time, *wire.Pending, wire.SubmitResult) error {
	b.refused = true
	return nil
}
func (b *fakeBook) Refusable(r wire.SubmitResult) bool { return !r.Resend }

type pickAll struct{}

func (pickAll) Select(avail []wire.Coin, need uint64) []wire.Coin {
	var sum uint64
	for _, c := range avail {
		sum += c.Lovelace
	}
	if sum < need {
		return nil
	}
	return avail
}

type fakeBuilder struct{ outs []build.Output }

func (b *fakeBuilder) FanOut(_ context.Context, _ keys.Wallet, _ build.Spend, outs []build.Output) (build.Built, error) {
	b.outs = outs
	return build.Built{Fee: 170_000}, nil
}

type fakeWallets struct {
	budget map[population.ActorID]uint64
	skip   map[population.ActorID]bool
}

func (w fakeWallets) IDs() []population.ActorID {
	return slices.Sorted(maps.Keys(w.budget))
}
func (w fakeWallets) Account(id population.ActorID) (Account, bool) {
	return Account{Budget: w.budget[id]}, !w.skip[id]
}
func (w fakeWallets) Utxos(keys.Wallet, []wire.Coin) []common.Utxo { return nil }

type fakeTTL struct{}

func (fakeTTL) TTL(sched.Time, bool, bool) uint64 { return 77 }

type rig struct {
	f  *Faucet
	b  *fakeBook
	j  *fakeJournal
	c  *fakeClock
	bu *fakeBuilder
}

func newRig(t *testing.T, coin uint64) rig {
	t.Helper()
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := rig{b: &fakeBook{pending: map[uint64]*wire.Pending{}, held: map[population.ActorID]uint64{1: 10, 2: 60, 3: 0}, coins: []wire.Coin{{Lovelace: coin}}},
		j: &fakeJournal{}, c: &fakeClock{}, bu: &fakeBuilder{}}
	w := fakeWallets{budget: map[population.ActorID]uint64{1: 100, 2: 100, 3: 100, 4: 100}, skip: map[population.ActorID]bool{4: true}}
	r.f = New(Spec{Actor: faucetActor, Slots: slots, Every: 8, Headroom: 5},
		Env{Clock: r.c, Journal: r.j, Book: r.b, Selector: pickAll{}, Builder: r.bu, Wallets: w, TTL: fakeTTL{}})
	return r
}

func TestRefillTopsUpOnlyWalletsUnderHalf(t *testing.T) {
	t.Parallel()
	r := newRig(t, 1000)
	if err := r.f.Refill(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	if len(r.bu.outs) != 2 || r.bu.outs[0].Amount != 90 || r.bu.outs[1].Amount != 100 {
		t.Fatalf("outputs %+v, want wallets 1 and 3 refilled to budget", r.bu.outs)
	}
	if len(r.j.got) != 1 || r.j.got[0].kind != wire.KindRefill || r.b.next != 1 || !r.b.pending[1].Refill {
		t.Fatalf("round not started: %+v, next %d", r.j.got, r.b.next)
	}
	if want := r.f.Save(); len(r.c.at) != 1 || r.c.at[0] != want || want != 9000 {
		t.Fatalf("next round %d, queued %v", want, r.c.at)
	}
}

func TestRefillWaitsForTheRoundInFlight(t *testing.T) {
	t.Parallel()
	r := newRig(t, 1000)
	r.b.pending[1] = &wire.Pending{ID: 1, Refill: true}
	if err := r.f.Refill(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(r.j.got) != 0 || r.bu.outs != nil || len(r.c.at) != 1 {
		t.Fatalf("a second round started: %+v", r.j.got)
	}
}

func TestRefillSkipsWhenItCannotCover(t *testing.T) {
	t.Parallel()
	r := newRig(t, 10)
	if err := r.f.Refill(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(r.j.got) != 1 || r.j.got[0].kind != wire.KindSkipped || r.bu.outs != nil {
		t.Fatalf("journal %+v", r.j.got)
	}
}

func TestResult(t *testing.T) {
	t.Parallel()
	r := newRig(t, 1000)
	ctx := context.Background()
	p := &wire.Pending{ID: 4, Refill: true}
	p.Awaiting, p.Cbor = true, []byte{1}
	if err := r.f.Result(ctx, 0, p, wire.SubmitResult{Verdict: wire.VerdictRejected}); err != nil || !r.b.refused || !p.Awaiting {
		t.Fatalf("first-send rejection: refused %v awaiting %v, %v", r.b.refused, p.Awaiting, err)
	}
	r.b.refused = false
	if err := r.f.Result(ctx, 0, p, wire.SubmitResult{Verdict: wire.VerdictRejected, Resend: true}); err != nil || r.b.refused || p.Awaiting || p.Cbor != nil {
		t.Fatalf("resent rejection stays reserved: refused %v awaiting %v, %v", r.b.refused, p.Awaiting, err)
	}
	if !slices.Equal(r.b.unawait, []uint64{4}) {
		t.Fatalf("unawait %v", r.b.unawait)
	}
	if err := r.f.Result(ctx, 0, p, wire.SubmitResult{}); !errors.Is(err, wire.ErrUnknownVerdict) {
		t.Fatalf("no verdict: %v", err)
	}
}

func TestStartAndLoad(t *testing.T) {
	t.Parallel()
	r := newRig(t, 1)
	r.f.Start()
	r.f.Load(5000)
	if r.f.Save() != 5000 || !slices.Equal(r.c.at, []sched.Time{0, 5000}) {
		t.Fatalf("at %d queued %v", r.f.Save(), r.c.at)
	}
}
