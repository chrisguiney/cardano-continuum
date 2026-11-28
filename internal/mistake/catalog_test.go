package mistake

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file runs the mistake catalog against a chain: the fake chain in
// inject_test.go, a cardano-node devnet in devnet_test.go. Both share it so
// any disagreement between them is a difference of the chains, not of the
// test.

// chain is what the catalog needs from the chain it runs against.
type chain interface {
	send(t *testing.T, tx []byte) submit.Outcome
	// land returns once transaction id is in a block.
	land(t *testing.T, id common.Blake2b256)
	// slot is a slot the chain has reached.
	slot(t *testing.T) uint64
}

// redeemerSource is a PlutusV3 validator that succeeds only when its
// redeemer is the integer 7: ScriptContext is Constr 0 [txInfo, redeemer,
// scriptInfo], so the redeemer is the head of the fields' tail. The
// builder's registry scripts ignore their redeemer, which would make a
// wrong redeemer harmless.
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
	return build.Script{Name: "redeemer-is-7", Source: redeemerSource, Language: 3, Bytes: wrapped}
}

func seven() common.Datum     { return common.Datum{Data: data.NewInteger(big.NewInt(7))} }
func unitDatum() common.Datum { return common.Datum{Data: data.NewConstr(0)} }

// world is an actor with funds on a chain and a script output to unlock.
type world struct {
	c      chain
	b      *build.Builder
	p      build.Params
	w      keys.Wallet
	dest   common.Address
	script build.Script
	funds  []common.Utxo
	locked common.Utxo
	spent  shelley.ShelleyTransactionInput
	// ahead is how many slots past the tip a transaction stays valid. A
	// node translates the bound to POSIX time for a script context only
	// within its stability window (3k/f slots), so a devnet needs it short.
	ahead uint64
}

const payAmount = 5_000_000

// prepare locks an output at the redeemer script, which also leaves the
// genesis input spent for the stale-input mistake.
func (wd *world) prepare(t *testing.T) {
	t.Helper()
	wd.spent = wd.funds[0].Id.(shelley.ShelleyTransactionInput)
	lock, err := wd.b.Plutus().Lock(context.Background(), wd.w, build.Lock{
		Spend: wd.spend(t), Script: wd.script, Amount: 10_000_000, Datum: unitDatum(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if o := wd.c.send(t, lock.Tx); o.Verdict != submit.Accepted {
		t.Fatalf("lock: %+v", o)
	}
	wd.c.land(t, lock.ID)
	wd.locked = lock.Outputs[0]
	wd.funds = ownOutputs(t, lock.Tx, wd.w.Address())
}

func (wd *world) spend(t *testing.T) build.Spend {
	return build.Spend{Inputs: wd.funds, TTL: wd.c.slot(t) + wd.ahead}
}

// base builds the correct transaction a mistake of kind k is injected into.
func (wd *world) base(t *testing.T, k Kind) build.Built {
	t.Helper()
	var (
		b   build.Built
		err error
	)
	if Script(k) {
		b, err = wd.b.Plutus().Unlock(context.Background(), wd.w, build.Unlock{
			Spend: wd.spend(t), Script: wd.script, Locked: wd.locked, Redeemer: seven(),
		})
	} else {
		b, err = wd.b.Pay(context.Background(), wd.w, build.Payment{
			Inputs: wd.funds, To: wd.dest, Amount: payAmount, TTL: wd.c.slot(t) + wd.ahead,
		})
	}
	if err != nil {
		t.Fatalf("%v: build: %v", k, err)
	}
	return b
}

func (wd *world) ctx(t *testing.T) Ctx {
	spent := wd.spent
	return Ctx{
		Params: wd.p, Signers: []keys.Signer{wd.w.PaymentSigner()},
		Change: wd.w.Address(), Slot: wd.c.slot(t), Stale: &spent,
	}
}

// result is one catalog row: what the oracle expected and what each send
// got.
type result struct {
	kind Kind
	want []Expected
	got  []submit.Outcome
}

func (r result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s", r.kind)
	for i, o := range r.got {
		fmt.Fprintf(&b, " | send %d: expected %v got %v", i+1, r.want[i], outcomeText(o))
	}
	return b.String()
}

func outcomeText(o submit.Outcome) string {
	if o.Verdict != submit.Rejected {
		return o.Verdict.String()
	}
	cs := make([]string, len(o.Failures))
	for i, f := range o.Failures {
		cs[i] = string(f.Class)
		if f.Class == submit.Unknown {
			cs[i] += "(" + f.Detail + ")"
		}
	}
	return "rejected " + strings.Join(cs, ",")
}

// runCatalog injects every mistake into a fresh correct transaction, sends
// it and judges each answer with the oracle. Rejected mistakes run first:
// they spend nothing, so every one builds on the same funds.
func runCatalog(t *testing.T, wd *world) []result {
	t.Helper()
	wd.prepare(t)
	order := slices.Clone(Kinds())
	slices.SortStableFunc(order, func(a, b Kind) int {
		_, ra := ClassOf(a)
		_, rb := ClassOf(b)
		switch {
		case ra == rb:
			return 0
		case ra:
			return -1
		}
		return 1
	})
	var out []result
	for _, k := range order {
		if Withheld(k) != "" {
			continue
		}
		out = append(out, wd.row(t, k))
	}
	return out
}

func (wd *world) row(t *testing.T, k Kind) result {
	t.Helper()
	b := wd.base(t, k)
	ks := Of(k)
	tx, want, err := Inject(b.Tx, ks, wd.ctx(t))
	if err != nil {
		t.Fatalf("%v: inject: %v", k, err)
	}
	r := result{kind: k}
	sends := 1
	if k == DoubleSubmit {
		sends = 2
	}
	for i := range sends {
		e := want
		if i > 0 {
			e = Expect(ks, true)
		}
		o := wd.c.send(t, tx)
		r.want, r.got = append(r.want, e), append(r.got, o)
		if !e.Pass(o) {
			t.Errorf("%v send %d: expected %v, got %s", k, i+1, e, outcomeText(o))
		}
	}
	if want.Accept && r.got[0].Verdict == submit.Accepted {
		id := txID(t, tx)
		wd.c.land(t, id)
		wd.funds = ownOutputs(t, tx, wd.w.Address())
	}
	if k == WrongAddress && want.Lost != payAmount {
		t.Errorf("wrong address: lost %d, want %d", want.Lost, payAmount)
	}
	return r
}

func txID(t testing.TB, raw []byte) common.Blake2b256 {
	t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tx.Hash()
}

// ownOutputs are raw's outputs paying addr.
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
	if len(out) == 0 {
		t.Fatal("no change output")
	}
	return out
}
