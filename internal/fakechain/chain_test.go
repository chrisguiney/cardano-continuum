package fakechain

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func TestBlocksOnVirtualClock(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(10)
	bs := f.chain.Blocks()
	var slots []uint64
	for _, b := range bs {
		slots = append(slots, b.Slot)
	}
	if want := []uint64{2, 4, 6, 8, 10}; !slices.Equal(slots, want) {
		t.Fatalf("block slots %v, want %v", slots, want)
	}
	for i, b := range bs {
		if b.Number != uint64(i+1) {
			t.Fatalf("block %d has number %d", i, b.Number)
		}
		if i > 0 && b.PrevHash != bs[i-1].Hash {
			t.Fatalf("block %d does not link to its parent", i)
		}
	}
	if tip := f.chain.Tip(); tip.Slot != 10 || !bytes.Equal(tip.Hash, bs[4].Hash[:]) {
		t.Fatalf("tip %v is not the last block", tip)
	}
	if err := f.chain.AdvanceTo(9); err == nil {
		t.Fatal("moving the clock backwards succeeded")
	}
}

func TestEpochs(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(250)
	if got := f.chain.Epoch(); got != 2 {
		t.Fatalf("epoch %d at slot 250 with 100-slot epochs, want 2", got)
	}
	f2 := newFixture(t, nil)
	if err := f2.chain.AdvanceToTime(f2.chain.cfg.Slots.SlotStart(7)); err != nil {
		t.Fatal(err)
	}
	if f2.chain.Slot() != 7 {
		t.Fatalf("slot %d, want 7", f2.chain.Slot())
	}
}

func TestIncludedTxMovesUtxo(t *testing.T) {
	f := newFixture(t, nil)
	id := f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	f.advance(2)
	if f.chain.HasTx(id) {
		t.Fatal("included tx still in the mempool")
	}
	if _, ok := f.chain.Utxo(f.genesisRef(f.alice)); ok {
		t.Fatal("spent genesis output still unspent")
	}
	u, ok := f.chain.Utxo(Ref{TxID: id, Index: 0})
	if !ok || u.Output.Amount().Uint64() != 10_000_000 {
		t.Fatalf("payment output missing or wrong: %v", u)
	}
	// The block decodes through the same gouroboros path a real node's
	// blocks take, and carries the submitted transaction.
	b := f.chain.Blocks()[0]
	dec, err := conway.NewConwayBlockFromCbor(b.Cbor)
	if err != nil {
		t.Fatal(err)
	}
	txs := dec.Transactions()
	if len(txs) != 1 || txs[0].Hash() != id {
		t.Fatalf("block transactions %v, want [%s]", txs, id)
	}
	if dec.Hash() != b.Hash {
		t.Fatal("decoded block hash differs")
	}
}

func TestDeterministic(t *testing.T) {
	run := func() []Block {
		f := newFixture(t, nil)
		f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
		f.advance(9)
		return f.chain.Blocks()
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatal("block counts differ")
	}
	for i := range a {
		if a[i].Hash != b[i].Hash || !bytes.Equal(a[i].Cbor, b[i].Cbor) {
			t.Fatalf("block %d differs between identical runs", i)
		}
	}
}

// population is a set of wallets the property test pays between.
type population struct {
	f       *fixture
	wallets []keys.Wallet
	byAddr  map[string]keys.Wallet
}

func newPopulation(t *testing.T, n int) *population {
	store, err := keys.New(rng.Seed{7}, testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	p := &population{byAddr: map[string]keys.Wallet{}}
	var genesis []GenesisOutput
	for i := range n {
		w := store.Wallet(keys.ActorID(i + 1))
		p.wallets = append(p.wallets, w)
		p.byAddr[w.Address().String()] = w
		genesis = append(genesis, GenesisOutput{Address: w.Address(), Lovelace: testLovelace})
	}
	p.f = newFixture(t, func(c *Config) { c.Genesis = genesis })
	return p
}

// randomTx spends one random UTxO of a random funded wallet to another
// wallet, with change.
func (p *population) randomTx(t *testing.T, r *rand.Rand) []byte {
	t.Helper()
	w := p.wallets[r.IntN(len(p.wallets))]
	us := p.f.chain.UtxosAt(w.Address())
	if len(us) == 0 {
		return nil
	}
	u := us[r.IntN(len(us))]
	have := u.Output.Amount().Uint64()
	if have < 5_000_000 {
		return nil
	}
	amount := 1_000_000 + r.Uint64N(have/2)
	dest := p.wallets[r.IntN(len(p.wallets))]
	s := txSpec{
		ins:     []Ref{refOf(u.Id)},
		outs:    []out{{dest.Address(), amount}, {w.Address(), 0}},
		signers: []keys.Wallet{w},
	}
	return s.balanced(t, have).build(t)
}

// TestPropValueConservation submits random valid transactions and checks
// after every block that unspent lovelace plus collected fees equals the
// genesis supply.
func TestPropValueConservation(t *testing.T) {
	p := newPopulation(t, 6)
	supply := uint64(6 * testLovelace)
	r := rng.Seed{3}.Rand()
	accepted := 0
	for slot := uint64(1); slot <= 60; slot++ {
		for range 3 {
			tx := p.randomTx(t, r)
			if tx != nil && p.f.submit(tx) == nil {
				accepted++
			}
		}
		p.f.advance(slot)
		if got := p.f.chain.UnspentLovelace() + p.f.chain.Fees(); got != supply {
			t.Fatalf("slot %d: unspent+fees = %d, want %d", slot, got, supply)
		}
	}
	if accepted < 30 {
		t.Fatalf("only %d transactions accepted; the property was barely exercised", accepted)
	}
}

func TestRejectedTxNeverInBlock(t *testing.T) {
	f := newFixture(t, nil)
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	s.fee--
	s.outs[1].coin++
	bad := s.build(t)
	if f.submit(bad) == nil {
		t.Fatal("bad tx accepted")
	}
	f.advance(8)
	id := txID(t, bad)
	for _, b := range f.chain.Blocks() {
		if slices.Contains(b.Txs, id) {
			t.Fatalf("rejected tx in block %d", b.Number)
		}
	}
}

func TestMempoolConflict(t *testing.T) {
	f := newFixture(t, nil)
	f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	cs := rejectClasses(t, f.submit(f.pay(f.alice, f.bob.Address(), 20_000_000).build(t)))
	if !hasClass(cs, BadInputs) {
		t.Fatalf("double spend in mempool: classes %v, want BadInputsUTxO", cs)
	}
}

func TestMempoolChaining(t *testing.T) {
	f := newFixture(t, nil)
	first := f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	s := txSpec{
		ins:     []Ref{{TxID: first, Index: 0}},
		outs:    []out{{f.alice.Address(), 0}},
		signers: []keys.Wallet{f.bob},
	}
	second := f.mustSubmit(s.balanced(t, 10_000_000).build(t))
	f.advance(2)
	if got := f.chain.Blocks()[0].Txs; !slices.Equal(got, []TxID{first, second}) {
		t.Fatalf("block txs %v, want parent then child", got)
	}
}

func TestMempoolCapacity(t *testing.T) {
	tx1Size := 0
	{
		f := newFixture(t, nil)
		tx1Size = len(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	}
	f := newFixture(t, func(c *Config) { c.MempoolBytes = uint64(tx1Size + 10) })
	f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	err := f.submit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
	if !errors.Is(err, ErrMempoolFull) {
		t.Fatalf("err = %v, want ErrMempoolFull", err)
	}
	f.advance(2)
	f.mustSubmit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
}

func TestBlockBodyLimit(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		pp := DefaultParams()
		pp.MaxBlockBodySize = 400
		c.Params = &pp
	})
	a := f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	b := f.mustSubmit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
	f.advance(4)
	bs := f.chain.Blocks()
	if !slices.Equal(bs[0].Txs, []TxID{a}) || !slices.Equal(bs[1].Txs, []TxID{b}) {
		t.Fatalf("blocks %v / %v, want one tx each in submission order", bs[0].Txs, bs[1].Txs)
	}
}

func equalUtxos(a, b []common.Utxo) bool {
	return slices.EqualFunc(a, b, func(x, y common.Utxo) bool {
		return refOf(x.Id) == refOf(y.Id) && bytes.Equal(x.Output.Cbor(), y.Output.Cbor())
	})
}

func TestRollbackRestores(t *testing.T) {
	f := newFixture(t, nil)
	f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	f.advance(2)
	wantUtxo := f.chain.AllUtxos()
	wantFees, wantTip := f.chain.Fees(), f.chain.Tip()
	f.mustSubmit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
	f.advance(6)
	if err := f.chain.Inject(Rollback{Depth: 2}); err != nil {
		t.Fatal(err)
	}
	if got := f.chain.AllUtxos(); !equalUtxos(got, wantUtxo) {
		t.Fatal("UTxO set after rollback differs from the set at the rollback point")
	}
	if f.chain.Fees() != wantFees {
		t.Fatalf("fees %d, want %d", f.chain.Fees(), wantFees)
	}
	if tip := f.chain.Tip(); tip.Slot != wantTip.Slot || !bytes.Equal(tip.Hash, wantTip.Hash) {
		t.Fatalf("tip %v, want %v", tip, wantTip)
	}
	if len(f.chain.Mempool()) != 0 {
		t.Fatal("rolled-back tx returned to the mempool without Readd")
	}
}

// A block whose second transaction spends the first's change: unwinding it
// must not bring back the change, which did not exist before the block.
func TestRollbackUnwindsChainedBlock(t *testing.T) {
	f := newFixture(t, nil)
	want := f.chain.AllUtxos()
	first := f.pay(f.alice, f.bob.Address(), 10_000_000)
	id := f.mustSubmit(first.build(t))
	change := first.outs[1].coin
	child := txSpec{ins: []Ref{{TxID: id, Index: 1}}, outs: []out{{f.bob.Address(), 5_000_000}, {f.alice.Address(), 0}},
		fee: -1, signers: []keys.Wallet{f.alice}}
	f.mustSubmit(child.balanced(t, change).build(t))
	f.advance(2)
	if got := f.chain.Blocks(); len(got) != 1 || len(got[0].Txs) != 2 {
		t.Fatalf("blocks %+v, want one block of two", got)
	}
	if err := f.chain.Inject(Rollback{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.chain.AllUtxos(); !equalUtxos(got, want) {
		t.Fatalf("UTxO set after rollback has %d outputs, genesis had %d", len(got), len(want))
	}
}

func TestRollbackReadd(t *testing.T) {
	f := newFixture(t, nil)
	id := f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	f.advance(2)
	if err := f.chain.Inject(Rollback{Depth: 1, Readd: true}); err != nil {
		t.Fatal(err)
	}
	if !f.chain.HasTx(id) {
		t.Fatal("Readd did not return the tx to the mempool")
	}
	f.advance(4)
	if got := f.chain.Blocks(); len(got) != 1 || !slices.Equal(got[0].Txs, []TxID{id}) {
		t.Fatal("re-added tx was not included on the new branch")
	}
}

func TestRollbackDepthBound(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(10)
	if err := f.chain.Inject(Rollback{Depth: 4}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("rollback past k: err = %v, want ErrTooDeep", err)
	}
	f2 := newFixture(t, nil)
	f2.advance(2)
	if err := f2.chain.Inject(Rollback{Depth: 2}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("rollback past genesis: err = %v, want ErrTooDeep", err)
	}
}

func TestForkDiffersFromAbandonedBranch(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(4)
	old := f.chain.Blocks()[1].Hash
	if err := f.chain.Inject(Rollback{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	f.advance(6)
	bs := f.chain.Blocks()
	if len(bs) != 2 || bs[1].Hash == old || bs[1].Slot != 6 {
		t.Fatalf("new branch block %+v should replace the abandoned one at a later slot", bs[1])
	}
}

func TestDropFault(t *testing.T) {
	f := newFixture(t, nil)
	tx := f.pay(f.alice, f.bob.Address(), 10_000_000).build(t)
	id := txID(t, tx)
	f = newFixture(t, func(c *Config) { c.Faults = []Scheduled{{Slot: 1, Fault: Drop{Tx: id}}} })
	f.mustSubmit(tx)
	f.advance(4)
	if f.chain.HasTx(id) || len(f.chain.Blocks()[0].Txs) != 0 {
		t.Fatal("dropped tx survived")
	}
	ev := f.chain.Evictions()
	if len(ev) != 1 || ev[0].Tx != id || ev[0].Reason != ReasonDropped || ev[0].Slot != 1 {
		t.Fatalf("evictions %+v", ev)
	}
}

func TestDropUnknownTx(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.chain.Inject(Drop{Tx: TxID{1}}); !errors.Is(err, ErrNotInMempool) {
		t.Fatalf("err = %v, want ErrNotInMempool", err)
	}
}

func TestDelayAndExpiryEviction(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Faults = []Scheduled{{Slot: 1, Fault: Delay{Slots: 3}}} })
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	s.ttl = 4
	id := f.mustSubmit(s.balanced(t, testLovelace).build(t))
	f.advance(7)
	bs := f.chain.Blocks()
	if bs[0].Slot != 5 || bs[1].Slot != 7 {
		t.Fatalf("delayed schedule: first blocks at %d, %d, want 5, 7", bs[0].Slot, bs[1].Slot)
	}
	if len(bs[0].Txs) != 0 {
		t.Fatal("expired tx was included")
	}
	ev := f.chain.Evictions()
	if len(ev) != 1 || ev[0].Tx != id || ev[0].Reason != ReasonInvalid ||
		!hasClass(classesOf(ev[0].Failures), OutsideValidityInterval) {
		t.Fatalf("evictions %+v, want the expired tx evicted as OutsideValidityIntervalUTxO", ev)
	}
}

func classesOf(fs []Failure) []Class {
	var out []Class
	for _, f := range fs {
		out = append(out, f.Class)
	}
	return out
}

func TestIncludeFault(t *testing.T) {
	f := newFixture(t, nil)
	a := f.mustSubmit(f.pay(f.alice, f.bob.Address(), 10_000_000).build(t))
	b := f.mustSubmit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
	if err := f.chain.Inject(Include{Txs: []TxID{b}}); err != nil {
		t.Fatal(err)
	}
	f.advance(4)
	bs := f.chain.Blocks()
	if !slices.Equal(bs[0].Txs, []TxID{b}) || !slices.Equal(bs[1].Txs, []TxID{a}) {
		t.Fatalf("blocks %v / %v, want [b] then [a]", bs[0].Txs, bs[1].Txs)
	}
	if err := f.chain.Inject(Include{Txs: []TxID{{9}}}); !errors.Is(err, ErrNotInMempool) {
		t.Fatalf("include unknown: err = %v", err)
	}
}

func TestScheduledFaultError(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Faults = []Scheduled{{Slot: 3, Fault: Rollback{Depth: 3}}} })
	if err := f.chain.AdvanceTo(5); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v, want the scheduled fault's ErrTooDeep", err)
	}
}

func TestNewValidates(t *testing.T) {
	store, _ := keys.New(rng.Seed{1}, testNetwork)
	a := store.Wallet(1).Address()
	f := newFixture(t, nil)
	base := f.chain.cfg
	tests := []struct {
		name string
		mod  func(*Config)
	}{
		{"zero block interval", func(c *Config) { c.BlockEvery = 0 }},
		{"zero k", func(c *Config) { c.K = 0 }},
		{"duplicate genesis address", func(c *Config) {
			c.Genesis = []GenesisOutput{{a, 1}, {a, 2}}
		}},
		{"genesis on another network", func(c *Config) {
			other, _ := keys.New(rng.Seed{1}, 1)
			c.Genesis = []GenesisOutput{{other.Wallet(1).Address(), 1}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			tt.mod(&c)
			if _, err := New(c); err == nil {
				t.Fatal("New accepted an invalid config")
			}
		})
	}
}

// TestRevalidateAfterBlock: after each block the mempool is revalidated at
// the slot after the new tip, so a tx whose ttl is that slot leaves when the
// block is made, not when the next block is assembled.
func TestRevalidateAfterBlock(t *testing.T) {
	f := newFixture(t, nil)
	other := f.mustSubmit(f.pay(f.bob, f.alice.Address(), 10_000_000).build(t))
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	s.ttl = 3
	id := f.mustSubmit(s.balanced(t, testLovelace).build(t))
	if err := f.chain.Inject(Include{Txs: []TxID{other}}); err != nil {
		t.Fatal(err)
	}
	f.advance(2)
	if f.chain.HasTx(id) {
		t.Fatal("tx expiring at the next slot is still in the mempool")
	}
	ev := f.chain.Evictions()
	if len(ev) != 1 || ev[0].Tx != id || ev[0].Slot != 2 {
		t.Fatalf("evictions %+v, want %s evicted at slot 2", ev, id)
	}
}

// The immutable point is the block k deep when it became immutable; it never
// moves back, so two rollbacks in a row cannot together pass it.
func TestConsecutiveRollbacksWithinImmutablePoint(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(10) // 5 blocks, k = 3: the first two are immutable
	if err := f.chain.Inject(Rollback{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.chain.Inject(Rollback{Depth: 2}); err != nil {
		t.Fatalf("second rollback within the remaining k-d: %v", err)
	}
	if got := len(f.chain.Blocks()); got != 2 {
		t.Fatalf("height %d, want 2", got)
	}
}

func TestConsecutiveRollbacksPastImmutablePoint(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(10)
	if err := f.chain.Inject(Rollback{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.chain.Inject(Rollback{Depth: 3}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("second rollback past the immutable point: err = %v, want ErrTooDeep", err)
	}
	if got := len(f.chain.Blocks()); got != 4 {
		t.Fatalf("a refused rollback changed the chain: height %d, want 4", got)
	}
}
