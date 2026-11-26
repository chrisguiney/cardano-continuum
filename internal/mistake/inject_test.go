package mistake

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

type fake struct{ c *fakechain.Chain }

func (f fake) send(t *testing.T, tx []byte) submit.Outcome {
	t.Helper()
	return submit.Send(context.Background(), submit.NewFake(f.c), fakechain.EraConway, tx)
}

func (f fake) land(t *testing.T, id common.Blake2b256) {
	t.Helper()
	for range 10 {
		if err := f.c.AdvanceTo(f.c.Slot() + 2); err != nil {
			t.Fatal(err)
		}
		if !f.c.HasTx(fakechain.TxID(id)) {
			return
		}
	}
	t.Fatalf("tx %x never left the mempool", id[:4])
}

func (f fake) slot(*testing.T) uint64 { return f.c.Slot() }

const fund = 1_000_000_000

func fakeWorld(t testing.TB) (*world, *fakechain.Chain) {
	t.Helper()
	store, err := keys.New(rng.Seed{9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	alice := store.Wallet(1)
	c, err := fakechain.New(fakechain.Config{
		Slots: slots, K: 3, BlockEvery: 2,
		Genesis: []fakechain.GenesisOutput{{Address: alice.Address(), Lovelace: fund}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pp := fakechain.DefaultParams()
	p := build.ParamsFromConway(&pp, 0)
	p.SystemStart, p.SlotLength = time.Unix(0, 0).UTC(), time.Second
	return &world{
		c: fake{c}, b: build.New(p), p: p, w: alice, dest: store.Wallet(2).Address(),
		script: redeemerScript(t), funds: c.UtxosAt(alice.Address()), ahead: 1000,
	}, c
}

func TestCatalogOnFakeChain(t *testing.T) {
	wd, c := fakeWorld(t)
	for _, r := range runCatalog(t, wd) {
		t.Log(r)
	}
	// The audit's side of wrong address: the output landed where nobody
	// holds the key.
	lost := 0
	for _, u := range c.AllUtxos() {
		a := u.Output.Address().String()
		if a != wd.w.Address().String() && a != wd.dest.String() && a != wd.script.Address(0).String() {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("%d outputs at unexpected addresses, want the one wrong-address payment", lost)
	}
}

// A mutant's rejection names only its own class, so a node answering with
// any one failure under any rule order is judged correct.
func TestMutantFailuresAnyOrder(t *testing.T) {
	wd, _ := fakeWorld(t)
	wd.prepare(t)
	b := wd.base(t, FeeTooSmall)
	ks := Of(FeeTooSmall, StaleInput, OutputTooSmall, Expired, MissingSigner, WrongNetwork)
	tx, want, err := Inject(b.Tx, ks, wd.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	o := wd.c.send(t, tx)
	if !want.Pass(o) {
		t.Fatalf("expected %v, got %s", want, outcomeText(o))
	}
	for _, k := range ks.Kinds() {
		if c, _ := ClassOf(k); !o.Has(c) {
			t.Errorf("%v: %s not reported by the fake chain, which runs every rule", k, c)
		}
	}
	for _, order := range permutations(classesOf(o)) {
		if !want.Pass(rejected(order[0])) {
			t.Fatalf("first failure %s under order %v not expected", order[0], order)
		}
	}
}

func classesOf(o submit.Outcome) []submit.Class {
	var cs []submit.Class
	for _, f := range o.Failures {
		cs = append(cs, f.Class)
	}
	return cs
}

// A stale input and a resubmit of a transaction already in a block are the
// same answer.
func TestStaleMutantAndResubmitBothBadInputs(t *testing.T) {
	wd, _ := fakeWorld(t)
	wd.prepare(t)
	b := wd.base(t, StaleInput)
	tx, _, err := Inject(b.Tx, Of(StaleInput), wd.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	stale := wd.c.send(t, tx)
	if o := wd.c.send(t, b.Tx); o.Verdict != submit.Accepted {
		t.Fatalf("correct tx: %+v", o)
	}
	wd.c.land(t, b.ID)
	again := wd.c.send(t, b.Tx)
	for _, o := range []submit.Outcome{stale, again} {
		if o.Verdict != submit.Rejected || !o.Has(submit.BadInputs) {
			t.Fatalf("want %s, got %s", submit.BadInputs, outcomeText(o))
		}
	}
	if !Expect(0, true).Pass(again) || !Expect(Of(StaleInput), false).Pass(stale) {
		t.Fatal("oracle refused one of the two BadInputs answers")
	}
}

func TestFixForRemovesClass(t *testing.T) {
	for _, k := range Kinds() {
		c, ok := ClassOf(k)
		if !ok {
			continue
		}
		t.Run(k.String(), func(t *testing.T) {
			wd, _ := fakeWorld(t)
			wd.prepare(t)
			b := wd.base(t, k)
			bad, _, err := Inject(b.Tx, Of(k), wd.ctx(t))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(bad, b.Tx) {
				t.Fatal("mutant equals the correct transaction")
			}
			fixed, want, err := Inject(b.Tx, Carry(Of(k), []submit.Class{c}), wd.ctx(t))
			if err != nil {
				t.Fatal(err)
			}
			if o := wd.c.send(t, fixed); !want.Accept || o.Verdict != submit.Accepted {
				t.Fatalf("after FixFor(%s): expected %v, got %s", c, want, outcomeText(o))
			}
		})
	}
}

func TestInjectReportsWitnessRecompute(t *testing.T) {
	wd, _ := fakeWorld(t)
	wd.prepare(t)
	for _, k := range Kinds() {
		b := wd.base(t, k)
		tx, _, err := Inject(b.Tx, Of(k), wd.ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		same := txID(t, tx) == b.ID
		if Resigns(k) == same {
			t.Errorf("%v: Resigns %v but body unchanged %v", k, Resigns(k), same)
		}
	}
}

func TestInjectNotApplicable(t *testing.T) {
	wd, _ := fakeWorld(t)
	wd.prepare(t)
	pay := wd.base(t, FeeTooSmall)
	for _, k := range []Kind{CollateralShort, ExUnitsLow, WrongDatum} {
		if _, _, err := Inject(pay.Tx, Of(k), wd.ctx(t)); !errors.Is(err, ErrNotApplicable) {
			t.Errorf("%v on a payment: %v, want ErrNotApplicable", k, err)
		}
	}
	if got := Applicable(pay.Tx, wd.ctx(t)); got.Has(ExUnitsLow) || !got.Has(FeeTooSmall) {
		t.Errorf("Applicable(payment) = %v", got)
	}
	unlock := wd.base(t, ExUnitsLow)
	if got := Applicable(unlock.Tx, wd.ctx(t)); got != Of(Kinds()...) {
		t.Errorf("Applicable(unlock) = %v, want every kind", got)
	}
	if _, _, err := Inject([]byte{0x80}, Of(FeeTooSmall), wd.ctx(t)); err == nil {
		t.Error("garbage accepted")
	}
}

func FuzzInject(f *testing.F) {
	wd, _ := fakeWorld(f)
	pay, err := wd.b.Pay(context.Background(), wd.w, build.Payment{
		Inputs: wd.funds, To: wd.dest, Amount: payAmount, TTL: 5000,
	})
	if err != nil {
		f.Fatal(err)
	}
	base := [][]byte{pay.Tx}
	f.Add(uint16(0x7ff), uint64(3_000_000), uint64(10), uint8(0), []byte(nil))
	f.Add(uint16(1), uint64(2_000_000), uint64(0), uint8(1), []byte{0x00})
	f.Fuzz(func(t *testing.T, ks uint16, amount, slot uint64, flip uint8, tail []byte) {
		tx := base[0]
		if amount >= 1_000_000 && amount < fund/2 {
			b, err := wd.b.Pay(context.Background(), wd.w, build.Payment{
				Inputs: wd.funds, To: wd.dest, Amount: amount, TTL: 5000,
			})
			if err == nil {
				tx = b.Tx
			}
		}
		tx = slices.Clone(tx)
		if len(tail) > 0 {
			i := int(flip) % len(tx)
			tx[i] ^= tail[0]
		}
		c := Ctx{Params: wd.p, Signers: []keys.Signer{wd.w.PaymentSigner()}, Change: wd.w.Address(), Slot: slot}
		out, _, err := Inject(tx, KindSet(ks), c)
		if err == nil && len(out) == 0 {
			t.Fatal("no error and no transaction")
		}
	})
}
