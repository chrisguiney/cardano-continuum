package fakechain

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// snap is the observable ledger state at one height.
type snap struct {
	utxo     []Ref
	outputs  [][]byte
	fees     uint64
	deposits uint64
	tip      uint64
	tipHash  []byte
	certs    *certState
}

func takeSnap(c *Chain) snap {
	tip := c.Tip()
	s := snap{fees: c.Fees(), deposits: c.Deposits(), tip: tip.Slot, tipHash: tip.Hash}
	for _, u := range c.AllUtxos() {
		s.utxo = append(s.utxo, refOf(u.Id))
		s.outputs = append(s.outputs, u.Output.Cbor())
	}
	c.mu.Lock()
	s.certs = c.certs.clone()
	c.mu.Unlock()
	return s
}

// payFrom pays 1 ADA from w's largest output to dest, or reports false when
// w has none large enough.
func (f *fixture) payFrom(w keys.Wallet, dest common.Address) ([]byte, bool) {
	us := f.chain.UtxosAt(w.Address())
	if len(us) == 0 {
		return nil, false
	}
	best := slices.MaxFunc(us, func(a, b common.Utxo) int {
		return int(a.Output.Amount().Uint64()>>10) - int(b.Output.Amount().Uint64()>>10)
	})
	in := best.Output.Amount().Uint64()
	if in < 10_000_000 {
		return nil, false
	}
	s := txSpec{
		ins:     []Ref{refOf(best.Id)},
		outs:    []out{{dest, 1_000_000}, {w.Address(), 0}},
		fee:     -1,
		signers: []keys.Wallet{w},
	}
	return s.balanced(f.t, in).build(f.t), true
}

// TestPropRollbackRestoresRecordedState: after any sequence of production,
// rollback, drop and include, a rollback leaves exactly the state recorded at
// the height it returns to, and one past the immutable point is ErrTooDeep
// and changes nothing.
func TestPropRollbackRestoresRecordedState(t *testing.T) {
	for seed := range uint64(30) {
		r := rand.New(rand.NewPCG(seed, 11))
		slots, err := sched.NewSlots(1000, 10)
		if err != nil {
			t.Fatal(err)
		}
		k := 1 + seed%4
		f := newFixture(t, func(c *Config) { c.Slots, c.K = slots, k })
		wallets := []keys.Wallet{f.alice, f.bob}
		at := []snap{takeSnap(f.chain)}
		for step := range 120 {
			switch n := r.IntN(10); {
			case n < 4:
				w := r.IntN(2)
				if tx, ok := f.payFrom(wallets[w], wallets[1-w].Address()); ok {
					_ = f.submit(tx) // a double spend is a legitimate rejection
				}
			case n < 7:
				for range 1 + r.IntN(4) {
					f.advance(f.chain.Slot() + 1)
					if len(f.chain.Blocks()) == len(at) {
						at = append(at, takeSnap(f.chain))
					}
				}
			case n < 8:
				if m := f.chain.Mempool(); len(m) > 0 {
					if err := f.chain.Inject(Drop{Tx: m[r.IntN(len(m))]}); err != nil {
						t.Fatalf("seed %d step %d: %v", seed, step, err)
					}
				}
			case n < 9:
				if m := f.chain.Mempool(); len(m) > 0 {
					if err := f.chain.Inject(Include{Txs: m[len(m)-1:]}); err != nil {
						t.Fatalf("seed %d step %d: %v", seed, step, err)
					}
				}
			default:
				at = rollbackStep(t, f.chain, r, at, seed, step)
			}
		}
	}
}

func rollbackStep(t *testing.T, c *Chain, r *rand.Rand, at []snap, seed uint64, step int) []snap {
	t.Helper()
	h, im := len(c.Blocks()), c.Immutable()
	depth := uint64(1 + r.IntN(h+2))
	err := c.Inject(Rollback{Depth: depth, Readd: r.IntN(2) == 0})
	if int(depth) > h-im {
		if !errors.Is(err, ErrTooDeep) {
			t.Fatalf("seed %d step %d: depth %d at height %d immutable %d: err = %v, want ErrTooDeep", seed, step, depth, h, im, err)
		}
		if got := len(c.Blocks()); got != h {
			t.Fatalf("seed %d step %d: refused rollback moved height %d to %d", seed, step, h, got)
		}
		return at
	}
	if err != nil {
		t.Fatalf("seed %d step %d: rollback %d at height %d immutable %d: %v", seed, step, depth, h, im, err)
	}
	to := h - int(depth)
	if got := takeSnap(c); !reflect.DeepEqual(got, at[to]) {
		t.Fatalf("seed %d step %d: state after rolling back %d to height %d differs from the recorded one", seed, step, depth, to)
	}
	return at[:to+1]
}
