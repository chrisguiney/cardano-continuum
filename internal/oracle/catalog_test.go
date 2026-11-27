package oracle

import (
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file runs the mistake catalog against the fake chain, as
// internal/mistake's catalog test does, but judges every answer with the
// oracle instead of stripping the fake's known divergence.

// redeemerSource succeeds only when its redeemer is the integer 7, so a wrong
// redeemer fails phase 2.
const redeemerSource = `(program 1.1.0 (lam ctx
  (force [(force (builtin ifThenElse))
    [(builtin equalsData)
      [(force (builtin headList)) [(force (builtin tailList))
        [(force (force (builtin sndPair))) [(builtin unConstrData) ctx]]]]
      (con data (I 7))]
    (delay (con unit ()))
    (delay (error))])))`

func redeemerScript(t testing.TB) build.Script {
	t.Helper()
	prog, err := syn.Parse(redeemerSource)
	if err != nil {
		t.Fatal(err)
	}
	db, err := syn.NameToDeBruijn(prog)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := syn.Encode(db)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := cbor.Encode(flat)
	if err != nil {
		t.Fatal(err)
	}
	return build.Script{Name: "redeemer-is-7", Source: redeemerSource, V3: common.PlutusV3Script(wrapped)}
}

type world struct {
	c      *fakechain.Chain
	b      *build.Builder
	p      build.Params
	w      keys.Wallet
	dest   common.Address
	script build.Script
	funds  []common.Utxo
	locked common.Utxo
	spent  shelley.ShelleyTransactionInput
}

func newWorld(t testing.TB) *world {
	t.Helper()
	store, err := keys.New(rng.Seed{9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	alice := store.Wallet(1)
	c := newChain(t, alice.Address())
	pp := fakechain.DefaultParams()
	p := build.ParamsFromConway(&pp, 0)
	p.SystemStart, p.SlotLength = time.Unix(0, 0).UTC(), time.Second
	return &world{c: c, b: build.New(p), p: p, w: alice, dest: store.Wallet(2).Address(),
		script: redeemerScript(t), funds: c.UtxosAt(alice.Address())}
}

// newChain is a fake chain funding addr. Two chains made from it are equal
// block for block until they are handed different transactions.
func newChain(t testing.TB, addr common.Address) *fakechain.Chain {
	t.Helper()
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	c, err := fakechain.New(fakechain.Config{
		Slots: slots, K: k, BlockEvery: 2,
		Genesis: []fakechain.GenesisOutput{{Address: addr, Lovelace: 1_000_000_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (wd *world) send(tx []byte) submit.Outcome {
	return submit.Send(context.Background(), submit.NewFake(wd.c), fakechain.EraConway, tx)
}

func (wd *world) land(t *testing.T, raw []byte) {
	t.Helper()
	id := txID(t, raw)
	for range 10 {
		if err := wd.c.AdvanceTo(wd.c.Slot() + 2); err != nil {
			t.Fatal(err)
		}
		if !wd.c.HasTx(id) {
			wd.funds = ownOutputs(t, raw, wd.w.Address())
			return
		}
	}
	t.Fatalf("tx %x never left the mempool", id[:4])
}

func (wd *world) spend() build.Spend {
	return build.Spend{Inputs: wd.funds, TTL: wd.c.Slot() + 1000}
}

// prepare locks an output at the redeemer script, which also leaves the
// genesis input spent for the stale-input mistake.
func (wd *world) prepare(t *testing.T) {
	t.Helper()
	wd.spent = wd.funds[0].Id.(shelley.ShelleyTransactionInput)
	lock, err := wd.b.Plutus().Lock(context.Background(), wd.w, build.Lock{
		Spend: wd.spend(), Script: wd.script, Amount: 10_000_000, Datum: common.Datum{Data: data.NewConstr(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if o := wd.send(lock.Tx); o.Verdict != submit.Accepted {
		t.Fatalf("lock: %+v", o)
	}
	wd.land(t, lock.Tx)
	wd.locked = lock.Outputs[0]
}

func (wd *world) base(t *testing.T, k mistake.Kind) build.Built {
	t.Helper()
	var (
		b   build.Built
		err error
	)
	if mistake.Script(k) {
		b, err = wd.b.Plutus().Unlock(context.Background(), wd.w, build.Unlock{
			Spend: wd.spend(), Script: wd.script, Locked: wd.locked,
			Redeemer: common.Datum{Data: data.NewInteger(big.NewInt(7))},
		})
	} else {
		b, err = wd.b.Pay(context.Background(), wd.w, build.Payment{
			Inputs: wd.funds, To: wd.dest, Amount: 5_000_000, TTL: wd.c.Slot() + 1000,
		})
	}
	if err != nil {
		t.Fatalf("%v: build: %v", k, err)
	}
	return b
}

func (wd *world) inject(t *testing.T, k mistake.Kind) ([]byte, mistake.Expected) {
	t.Helper()
	b := wd.base(t, k)
	spent := wd.spent
	tx, want, err := mistake.Inject(b.Tx, mistake.Of(k), mistake.Ctx{
		Params: wd.p, Signers: []keys.Signer{wd.w.PaymentSigner()},
		Change: wd.w.Address(), Slot: wd.c.Slot(), Stale: &spent,
	})
	if err != nil {
		t.Fatalf("%v: inject: %v", k, err)
	}
	return tx, want
}

func txID(t testing.TB, raw []byte) chain.TxID {
	t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tx.Hash()
}

func ownOutputs(t testing.TB, raw []byte, addr common.Address) []common.Utxo {
	t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out []common.Utxo
	for i, o := range tx.Outputs() {
		if o.Address().String() == addr.String() {
			out = append(out, common.Utxo{
				Id:     shelley.ShelleyTransactionInput{TxId: tx.Hash(), OutputIndex: uint32(i)},
				Output: o,
			})
		}
	}
	return out
}

var fakeNode = Node{Impl: "fakechain", Pool: "fakechain"}

// rejectedFirst orders the catalog so the mistakes a node rejects run first:
// they spend nothing, so every one builds on the same funds.
func rejectedFirst() []mistake.Kind {
	order := slices.Clone(mistake.Kinds())
	slices.SortStableFunc(order, func(a, b mistake.Kind) int {
		_, ra := mistake.ClassOf(a)
		_, rb := mistake.ClassOf(b)
		switch {
		case ra == rb:
			return 0
		case ra:
			return -1
		}
		return 1
	})
	return order
}

// TestCatalogOnFakeChain judges every catalog entry's answers. The fake chain
// agrees with the expectations, so there are no findings; an extra class from
// the fake (it once added MissingRedeemers beside BadInputs) would show here
// as a wrong-class finding against fakechain.
func TestCatalogOnFakeChain(t *testing.T) {
	t.Parallel()
	wd := newWorld(t)
	wd.prepare(t)
	var all []Finding
	byKind := map[mistake.Kind][]Finding{}
	for _, k := range rejectedFirst() {
		tx, want := wd.inject(t, k)
		sends := []*mistake.Expected{&want}
		if k == mistake.DoubleSubmit {
			again := mistake.Expect(mistake.Of(k), true)
			sends = append(sends, &again)
		}
		var first submit.Outcome
		for i, e := range sends {
			o := wd.send(tx)
			if i == 0 {
				first = o
			}
			fs := Judge(Attempt{Seq: uint64(i + 1), Tx: tx, ID: txID(t, tx), Expected: e,
				Answers: []Answer{{Node: fakeNode, Outcome: o}}}, run)
			byKind[k] = append(byKind[k], fs...)
			all = append(all, fs...)
		}
		if want.Accept && first.Verdict == submit.Accepted {
			wd.land(t, tx)
		}
	}
	for _, f := range all {
		t.Errorf("catalog finding: %s %+v", f.Kind, f.Facts)
	}
}

// A wrong expectation over a real answer fires.
func TestCatalogWrongExpectationFires(t *testing.T) {
	t.Parallel()
	wd := newWorld(t)
	wd.prepare(t)
	tx, _ := wd.inject(t, mistake.FeeTooSmall)
	o := wd.send(tx)
	wrong := mistake.Expect(mistake.Of(mistake.Expired), false)
	fs := Judge(Attempt{Seq: 1, Tx: tx, ID: txID(t, tx), Expected: &wrong,
		Answers: []Answer{{Node: fakeNode, Outcome: o}}}, run)
	if got := kinds(fs); !slices.Equal(got, []Kind{WrongClass}) {
		t.Fatalf("kinds = %v, want wrong class for %v", got, o.Failures)
	}
	ok := mistake.Expect(0, false)
	fs = Judge(Attempt{Seq: 2, Tx: tx, ID: txID(t, tx), Expected: &ok,
		Answers: []Answer{{Node: fakeNode, Outcome: o}}}, run)
	if got := kinds(fs); !slices.Equal(got, []Kind{UnexpectedReject}) {
		t.Fatalf("kinds = %v, want unexpected reject", got)
	}
}
