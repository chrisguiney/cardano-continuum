package adversary

import (
	"context"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// bench is a fake chain holding one output for the adversary and one for
// its victim, and a valid payment from the adversary to build mutants from.
type bench struct {
	c         *fakechain.Chain
	adv, vic  keys.Wallet
	valid     []byte
	advCoin   common.Utxo
	vicCoin   common.Utxo
	params    build.Params
	ttl       uint64
	signers   []keys.Signer
	mistakeCx mistake.Ctx
}

func newBench(t testing.TB) *bench {
	t.Helper()
	ks, err := keys.New(rng.Seed{3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	b := &bench{adv: ks.Wallet(1), vic: ks.Wallet(2), ttl: 500}
	b.c, err = fakechain.New(fakechain.Config{Slots: slots, K: 3, BlockEvery: 2, Genesis: []fakechain.GenesisOutput{
		{Address: b.adv.Address(), Lovelace: 100_000_000}, {Address: b.vic.Address(), Lovelace: 50_000_000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pp := fakechain.DefaultParams()
	b.params = build.ParamsFromConway(&pp, 0)
	b.params.SystemStart, b.params.SlotLength = time.Unix(0, 0).UTC(), time.Second
	b.advCoin, b.vicCoin = b.c.UtxosAt(b.adv.Address())[0], b.c.UtxosAt(b.vic.Address())[0]
	built, err := build.New(b.params).Pay(context.Background(), b.adv, build.Payment{
		Inputs: []common.Utxo{b.advCoin}, TTL: b.ttl, To: b.adv.Address(), Amount: 2_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.valid = built.Tx
	b.signers = []keys.Signer{b.adv.PaymentSigner()}
	b.mistakeCx = mistake.Ctx{Params: b.params, Signers: append(b.signers, b.adv.StakeSigner()), Change: b.adv.Address()}
	return b
}

func (b *bench) send(t *testing.T, tx []byte) submit.Outcome {
	t.Helper()
	return submit.Send(context.Background(), submit.NewFake(b.c), fakechain.EraConway, tx)
}

// mutant makes m the way the engine does: the mistake catalog for the
// semantic mutations, the raw builder for a steal, byte edits otherwise.
func (b *bench) mutant(t *testing.T, m Mutation) ([]byte, mistake.Expected) {
	t.Helper()
	if ks, ok := m.Mistake(); ok {
		tx, _, err := mistake.Inject(b.valid, ks, b.mistakeCx)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		return tx, Expect(m)
	}
	if m == Steal {
		tx, err := RawSpend(RawSpendOf{
			Inputs:  []Coin{coinOf(b.advCoin), coinOf(b.vicCoin)},
			To:      b.adv.Address(),
			Fee:     StealFee,
			TTL:     b.ttl,
			Signers: b.signers,
		})
		if err != nil {
			t.Fatal(err)
		}
		return tx, Expect(m)
	}
	tx, err := Mutate(b.valid, m)
	if err != nil {
		t.Fatalf("%s: %v", m, err)
	}
	return tx, Expect(m)
}

func coinOf(u common.Utxo) Coin {
	return Coin{Ref: Ref{Tx: u.Id.Id(), Index: u.Id.Index()}, Lovelace: u.Output.Amount().Uint64()}
}

func TestMalformedCorpusIsRejected(t *testing.T) {
	b := newBench(t)
	if o := b.send(t, b.valid); o.Verdict != submit.Rejected && o.Verdict != submit.Accepted {
		t.Fatalf("the base transaction: %+v", o)
	}
	// A fresh chain per mutant: the base payment above spent the coin.
	for _, m := range Mutations() {
		b := newBench(t)
		tx, want := b.mutant(t, m)
		if want.Accept {
			t.Errorf("%s: expected verdict lets the node accept it", m)
		}
		o := b.send(t, tx)
		if !want.Pass(o) {
			t.Errorf("%s: node said %v %v, expected %s", m, o.Verdict, o.Failures, want)
		}
	}
}

func TestRawSpendIsWellFormed(t *testing.T) {
	b := newBench(t)
	tx, err := RawSpend(RawSpendOf{Inputs: []Coin{coinOf(b.advCoin)}, To: b.vic.Address(), Fee: StealFee, TTL: b.ttl, Signers: b.signers})
	if err != nil {
		t.Fatal(err)
	}
	if o := b.send(t, tx); o.Verdict != submit.Accepted {
		t.Fatalf("a raw spend of the adversary's own coin: %v %v", o.Verdict, o.Failures)
	}
}

func TestNonCanonicalKeepsTheValue(t *testing.T) {
	b := newBench(t)
	tx, err := Mutate(b.valid, NonCanonical)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := conway.NewConwayTransactionFromCbor(b.valid)
	if err != nil {
		t.Fatal(err)
	}
	mut, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		t.Fatalf("a non-canonical encoding must still decode: %v", err)
	}
	if mut.Fee().Cmp(orig.Fee()) != 0 {
		t.Fatalf("fee %v, want %v", mut.Fee(), orig.Fee())
	}
	if mut.Hash() == orig.Hash() {
		t.Fatal("the body hash ignores the bytes it was given")
	}
}

func FuzzMutantsDecode(f *testing.F) {
	b := newBench(f)
	for _, m := range Mutations() {
		if m.raw() {
			tx, err := Mutate(b.valid, m)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(tx)
		}
	}
	f.Fuzz(func(t *testing.T, tx []byte) {
		// Neither the decoder nor a mutation of what it read may panic.
		_, _ = conway.NewConwayTransactionFromCbor(tx)
		for _, m := range Mutations() {
			if m.raw() {
				_, _ = Mutate(tx, m)
			}
		}
	})
}
