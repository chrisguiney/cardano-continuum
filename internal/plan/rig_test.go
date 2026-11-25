package plan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

const (
	rigLovelace   = 100_000_000
	rigBlockEvery = 2
	rigTTL        = 14 // slots, so seven blocks
	headroom      = 1_000_000
)

// intent is one payment the rig wants made.
type intent struct {
	id     IntentID
	from   population.ActorID
	to     population.ActorID
	amount uint64
}

// rig runs a ledger against the fake chain: real signed transactions,
// real blocks, chain-sync through a follower the ledger drains lazily, so its
// view lags the chain as it does in the engine.
type rig struct {
	t       testing.TB
	ks      *keys.Store
	owners  owners
	actors  []population.ActorID
	chain   *fakechain.Chain
	target  submit.Target
	fol     *fakechain.Follower
	builder *build.Builder
	disk    *memDisk
	l       *Ledger
	k       uint64

	intents  []*intent
	byTx     map[TxID]*intent
	cbor     map[TxID][]byte
	sentOnce map[TxID]bool // handed to a node at least once
	everDead map[TxID]bool
	settled  map[TxID]bool
	// used is every input an attempt ever reserved, by the attempt.
	used    map[Ref][]TxID
	floor   int // blocks that can never be rolled back
	genesis []Utxo
}

func newRig(t testing.TB, actors int, k uint64) *rig {
	t.Helper()
	ks, err := keys.New(rng.Seed{9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ks: ks, owners: owners{}, disk: &memDisk{}, k: k,
		byTx: map[TxID]*intent{}, cbor: map[TxID][]byte{}, sentOnce: map[TxID]bool{},
		everDead: map[TxID]bool{}, settled: map[TxID]bool{}, used: map[Ref][]TxID{}}
	var gen []fakechain.GenesisOutput
	var utxos []Utxo
	for i := 1; i <= actors; i++ {
		a := population.ActorID(i)
		r.actors = append(r.actors, a)
		addr := r.wallet(a).Address()
		b, err := addr.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		r.owners[string(b)] = a
		gen = append(gen, fakechain.GenesisOutput{Address: addr, Lovelace: rigLovelace})
		utxos = append(utxos, Utxo{Ref: Ref{Tx: common.Blake2b256Hash(b)}, Owner: a, Lovelace: rigLovelace})
	}
	r.chain, err = fakechain.New(fakechain.Config{Slots: slots, K: k, BlockEvery: rigBlockEvery, Genesis: gen})
	if err != nil {
		t.Fatal(err)
	}
	r.target = submit.NewFake(r.chain)
	r.fol = r.chain.Follow(0)
	pp := fakechain.DefaultParams()
	r.builder = build.New(build.Params{MinFeeA: uint64(pp.MinFeeA), MinFeeB: uint64(pp.MinFeeB),
		CoinsPerUtxoByte: uint64(pp.AdaPerUtxoByte), MaxTxSize: uint32(pp.MaxTxSize)})
	r.genesis = utxos
	r.l, err = New(r.cfg(), utxos)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) cfg() Config { return Config{K: r.k, Owners: r.owners, Store: r.disk} }

func (r *rig) wallet(a population.ActorID) keys.Wallet { return r.ks.Wallet(keys.ActorID(a)) }

// build makes a signed attempt at in spending ins.
func (r *rig) build(in *intent, ins []Utxo) (Attempt, []byte, error) {
	w := r.wallet(in.from)
	lu := make([]common.Utxo, len(ins))
	for i, u := range ins {
		o := babbage.BabbageTransactionOutput{OutputAddress: w.Address(),
			OutputAmount: mary.MaryTransactionOutputValue{Amount: u.Lovelace}}
		lu[i] = common.Utxo{Id: shelley.ShelleyTransactionInput{TxId: common.Blake2b256(u.Ref.Tx), OutputIndex: u.Ref.Index},
			Output: &o}
	}
	b, err := r.builder.Pay(context.Background(), w, build.Payment{
		Inputs: lu, To: r.wallet(in.to).Address(), Amount: in.amount, TTL: r.chain.Slot() + rigTTL,
	})
	if err != nil {
		return Attempt{}, nil, err
	}
	a := Attempt{Tx: TxID(b.ID), Actor: in.from, TTL: b.TTL}
	for _, u := range ins {
		a.Inputs = append(a.Inputs, u.Ref)
	}
	for _, o := range b.Outputs {
		if owner, ok := r.owners.Owner(o.Output.Address()); ok && owner == in.from {
			a.Change = append(a.Change, Utxo{Ref: chain.RefOf(o.Id), Owner: owner, Lovelace: o.Output.Amount().Uint64()})
		}
	}
	return a, b.Tx, nil
}

// reserve records a for in, checking the store's NoSelfConflict form: an
// input another intent's attempt reserved before is only reused once that
// attempt is dead.
func (r *rig) reserve(in *intent, a Attempt, tx []byte) error {
	if err := r.l.Reserve(context.Background(), in.id, a); err != nil {
		return err
	}
	for _, ref := range a.Inputs {
		for _, prev := range r.used[ref] {
			if r.byTx[prev].id != in.id && !r.everDead[prev] {
				r.t.Fatalf("input %v reused by intent %d while %x of intent %d is not dead",
					ref, in.id, prev[:4], r.byTx[prev].id)
			}
		}
		r.used[ref] = append(r.used[ref], a.Tx)
	}
	r.byTx[a.Tx], r.cbor[a.Tx] = in, tx
	return nil
}

// send submits tx. Only a rejection of its first send proves it never
// reached a node.
func (r *rig) send(tx TxID) {
	first := !r.sentOnce[tx]
	r.sentOnce[tx] = true
	o := submit.Send(context.Background(), r.target, fakechain.EraConway, r.cbor[tx])
	if !o.Dead() || !first {
		return
	}
	dead, err := r.l.Refused(context.Background(), tx)
	if err != nil {
		r.t.Fatalf("refused %x: %v", tx[:4], err)
	}
	r.markDead(dead)
}

func (r *rig) markDead(txs []TxID) {
	for _, tx := range txs {
		r.everDead[tx] = true
	}
}

// sync feeds the ledger up to n chain-sync events; n < 0 drains.
func (r *rig) sync(n int) {
	for ; n != 0; n-- {
		ev, ok := r.fol.Next()
		if !ok {
			return
		}
		res, err := r.l.Advance(context.Background(), r.event(ev))
		if err != nil {
			r.t.Fatalf("advance: %v", err)
		}
		r.markDead(res.Dead)
		for _, tx := range res.Settled {
			r.settled[tx] = true
		}
	}
}

func (r *rig) event(ev fakechain.Event) chain.Event {
	if ev.Kind == fakechain.RollBackward {
		p, ok := chain.PointFromWire(ev.Point)
		if !ok {
			r.t.Fatal("bad rollback point")
		}
		return chain.Event{Kind: chain.Backward, Point: p}
	}
	blk, err := conway.NewConwayBlockFromCbor(ev.Block.Cbor)
	if err != nil {
		r.t.Fatal(err)
	}
	b := chain.BlockFrom(blk)
	return chain.Event{Kind: chain.Forward, Block: b, Point: b.Point}
}

// restart reopens the ledger from disk and resumes chain-sync at its
// settled point. Drop notices are lost; unfinished attempts count as sent.
func (r *rig) restart() {
	l, err := Open(r.cfg(), r.disk.last)
	if r.disk.commits == 0 {
		l, err = New(r.cfg(), r.genesis) // nothing was ever committed
	}
	if err != nil {
		r.t.Fatalf("open: %v", err)
	}
	r.l = l
	for _, p := range l.Unfinished() {
		r.sentOnce[p.Tx] = true
	}
	fol, _, err := r.chain.FollowFrom([]ocommon.Point{l.ResumePoint().Wire()}, 0)
	if err != nil {
		r.t.Fatalf("resume at %v: %v", l.ResumePoint(), err)
	}
	r.fol = fol
}

// blocks advances the chain clock by n block intervals.
func (r *rig) blocks(n int) {
	if err := r.chain.AdvanceTo(r.chain.Slot() + uint64(n)*rigBlockEvery); err != nil {
		r.t.Fatal(err)
	}
	r.floor = max(r.floor, len(r.chain.Blocks())-int(r.k))
}

// truth checks the chain against the ledger's records: no intent paid
// twice, nothing the ledger wrote off on the chain, everything it settled
// in the immovable part.
func (r *rig) truth() {
	paid := map[IntentID]TxID{}
	onChain := map[TxID]int{}
	for i, b := range r.chain.Blocks() {
		for _, tx := range b.Txs {
			onChain[tx] = i
			in, ok := r.byTx[tx]
			if !ok {
				continue
			}
			if prev, twice := paid[in.id]; twice {
				r.t.Fatalf("intent %d paid twice: %x and %x", in.id, prev[:4], tx[:4])
			}
			paid[in.id] = tx
		}
	}
	for tx := range r.everDead {
		if _, ok := onChain[tx]; ok {
			r.t.Fatalf("%x is on the chain after the ledger wrote it off", tx[:4])
		}
	}
	for tx := range r.settled {
		if i, ok := onChain[tx]; !ok || i >= r.floor {
			r.t.Fatalf("settled %x not in the immovable chain (at %d, floor %d, ok %v)", tx[:4], i, r.floor, ok)
		}
	}
}

func (r *rig) newIntent(from, to population.ActorID, amount uint64) *intent {
	in := &intent{id: IntentID(len(r.intents) + 1), from: from, to: to, amount: amount}
	r.intents = append(r.intents, in)
	return in
}

// attempt selects, builds and reserves an attempt at in from Available.
func (r *rig) attempt(in *intent) (Attempt, error) {
	coins := Select(r.l.Available(in.from, in.id, Opts{Chain: true}), in.amount+headroom)
	if coins == nil {
		return Attempt{}, fmt.Errorf("intent %d: nothing to spend", in.id)
	}
	a, tx, err := r.build(in, coins)
	if err != nil {
		return Attempt{}, err
	}
	return a, r.reserve(in, a, tx)
}

// pick returns the outputs Available offers in's actor, split by whether
// a's inputs hold them.
func (r *rig) pick(in *intent, a Attempt) (shared, fresh []Utxo) {
	for _, u := range r.l.Available(in.from, in.id, Opts{Chain: true}) {
		if slices.Contains(a.Inputs, u.Ref) {
			shared = append(shared, u)
		} else {
			fresh = append(fresh, u)
		}
	}
	return shared, fresh
}

// The PlannerFreshInputsBug order: a block holding the first attempt is in
// flight, so the transaction looks dropped. A second attempt on other
// outputs (here the first's own change) is refused; one sharing the first's
// input is accepted, and the chain takes only one of the two.
func TestDropNoticeAfterInclusionDoesNotPayTwice(t *testing.T) {
	r := newRig(t, 2, 2)
	in := r.newIntent(1, 2, 10_000_000)
	first, err := r.attempt(in)
	if err != nil {
		t.Fatal(err)
	}
	r.send(first.Tx)
	r.blocks(1) // the block holding it is in flight: the ledger has not seen it
	if r.chain.HasTx(fakechain.TxID(first.Tx)) {
		t.Fatal("still in the mempool")
	}
	r.l.DropNotice(first.Tx)

	shared, fresh := r.pick(in, first)
	if len(shared) != 1 || len(fresh) == 0 {
		t.Fatalf("shared %v, fresh %v", shared, fresh)
	}
	a, tx, err := r.build(in, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.reserve(in, a, tx); !errors.Is(err, ErrNotExclusive) {
		t.Fatalf("second attempt on fresh inputs: %v", err)
	}
	b, tx, err := r.build(in, shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.reserve(in, b, tx); err != nil {
		t.Fatalf("second attempt sharing the input: %v", err)
	}
	r.send(b.Tx)
	r.blocks(int(r.k) + 2)
	r.sync(-1)
	r.truth()
	if !r.settled[first.Tx] || !r.everDead[b.Tx] {
		t.Fatalf("first settled %v, second dead %v", r.settled[first.Tx], r.everDead[b.Tx])
	}
}
