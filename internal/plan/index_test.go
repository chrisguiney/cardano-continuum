package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// goldenBlob is the SHA-256 of the blob encodeWorld produces. The encoding
// is stored in checkpoints, so a change here is a format change.
const goldenBlob = "21778a44f2b56d4cf87d78b4f144c4519d23e4bf82ebfc4e4b7954da7501c0d9"

func encodeWorld(t *testing.T) store.Blob {
	t.Helper()
	w := newWorld(t)
	a := w.attempt(1, 1, 500, g1)
	w.reserve(1, a)
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1), g2))
	w.reserve(3, w.attempt(3, 2, farTTL, g3))
	w.block(10, w.txOf(a))
	w.block(20)
	w.block(30)
	w.block(40)
	w.block(50)
	w.block(60)
	b, err := w.l.Blob()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBlobBytesArePinned(t *testing.T) {
	b := encodeWorld(t)
	sum := sha256.Sum256(b.Data)
	if got := hex.EncodeToString(sum[:]); b.Version != BlobVersion || got != goldenBlob {
		t.Fatalf("blob version %d sha256 %s, want %s", b.Version, got, goldenBlob)
	}
}

// skipDisk is a Committer that declares it keeps nothing.
type skipDisk struct{ calls int }

func (d *skipDisk) Commit(context.Context, store.Blob) error { d.calls++; return nil }
func (*skipDisk) Discards() bool                             { return true }

// TestDiscardingCommitterGetsNoBlob: a ledger over a discarding Committer
// is never handed a blob, yet ends in the same state as one over a disk.
func TestDiscardingCommitterGetsNoBlob(t *testing.T) {
	want := encodeWorld(t)
	w := newWorld(t)
	d := &skipDisk{}
	cfg := w.cfg()
	cfg.Store = d
	l, err := New(cfg, w.l.Settled())
	if err != nil {
		t.Fatal(err)
	}
	w.l = l
	a := w.attempt(1, 1, 500, g1)
	w.reserve(1, a)
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1), g2))
	w.reserve(3, w.attempt(3, 2, farTTL, g3))
	for i, slot := range []uint64{10, 20, 30, 40, 50, 60} {
		if i == 0 {
			w.block(slot, w.txOf(a))
			continue
		}
		w.block(slot)
	}
	if d.calls != 0 {
		t.Fatalf("%d commits reached a discarding committer", d.calls)
	}
	got, err := w.l.Blob()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || string(got.Data) != string(want.Data) {
		t.Fatal("state differs from the disk-backed ledger's")
	}
}

// TestPropOwnerIndexMatchesScan drives the ledger as TestPropPlannerMatchesModel
// does and compares the owner-indexed Available with a full scan after every
// step, for every actor, both Chain settings and several intents.
func TestPropOwnerIndexMatchesScan(t *testing.T) {
	seeds := 12
	if testing.Short() {
		seeds = 3
	}
	for seed := range uint64(seeds) {
		p := &prop{r: newRig(t, 3, 2), rnd: rand.New(rand.NewPCG(seed, 29))}
		for step := range 400 {
			p.step()
			st := p.r.l.st
			if err := st.indexConsistent(); err != nil {
				t.Fatalf("seed %d step %d (%s): %v", seed, step, p.last, err)
			}
			for _, a := range p.r.actors {
				for _, o := range []Opts{{}, {Chain: true}, {Chain: true, MaxDepth: 2}} {
					for id := IntentID(0); id <= 3; id++ {
						got, want := st.available(a, id, o), st.availableScan(a, id, o)
						if !equalUtxos(got, want) {
							t.Fatalf("seed %d step %d (%s): actor %d intent %d %+v: index %v, scan %v",
								seed, step, p.last, a, id, o, refsOf(got), refsOf(want))
						}
					}
				}
			}
		}
	}
}
