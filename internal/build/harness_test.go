package build

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// harness is a fake chain funding alice, and a Builder on its parameters.
// Every builder test validates its transaction with gouroboros' Conway rules
// through the fake chain, which is the "correct" oracle.
type harness struct {
	t     *testing.T
	store *keys.Store
	alice keys.Wallet
	chain *fakechain.Chain
	pp    conway.ConwayProtocolParameters
	b     *Builder
}

const fund = 800_000_000_000

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

// newHarnessWith is newHarness on parameters mod changes; the chain and
// the builder share them.
func newHarnessWith(t *testing.T, mod func(*conway.ConwayProtocolParameters)) *harness {
	t.Helper()
	var seed rng.Seed
	seed[31] = 7
	store, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	cold, _ := store.Committee(0)
	h := &harness{t: t, store: store, alice: store.Wallet(1), pp: fakechain.DefaultParams()}
	if mod != nil {
		mod(&h.pp)
	}
	pp := h.pp
	h.chain, err = fakechain.New(fakechain.Config{
		Params: &pp,
		Slots:  slots, K: 3, BlockEvery: 2,
		Genesis:   []fakechain.GenesisOutput{{Address: h.alice.Address(), Lovelace: fund}},
		Committee: []fakechain.CommitteeSeat{{Cold: common.Blake2b224(cold), Expiry: 1000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := ParamsFromConway(&h.pp, 0)
	p.SystemStart, p.SlotLength = time.Unix(0, 0).UTC(), time.Second
	h.b = New(p)
	return h
}

// spend offers every UTxO alice holds on the confirmed chain.
func (h *harness) spend() Spend {
	return Spend{Inputs: h.chain.UtxosAt(h.alice.Address()), TTL: h.chain.Slot() + 1000}
}

// commit submits got and advances past the next block.
func (h *harness) commit(got Built) {
	h.t.Helper()
	if err := h.chain.Submit(context.Background(), fakechain.EraConway, got.Tx); err != nil {
		h.t.Fatalf("fake chain rejected the transaction: %v", err)
	}
	if err := h.chain.AdvanceTo(h.chain.Slot() + 2); err != nil {
		h.t.Fatal(err)
	}
}

// rejects asserts the fake chain refuses tx with class c.
func (h *harness) rejects(tx []byte, c fakechain.Class) {
	h.t.Helper()
	err := h.chain.Submit(context.Background(), fakechain.EraConway, tx)
	var rej *fakechain.RejectError
	if !errors.As(err, &rej) {
		h.t.Fatalf("want rejection %s, got %v", c, err)
	}
	if !rej.Has(c) {
		h.t.Fatalf("want rejection %s, got %v", c, err)
	}
}

// want is what a built transaction must account for beyond inputs and
// outputs.
type want struct {
	deposit, refund uint64
	signers         []keys.Credential
}

// check runs the per-builder properties: the transaction decodes with
// gouroboros, the fee is the ledger minimum for its size and execution
// units, value is conserved with deposits and refunds, every listed key
// witnesses it with a valid signature, and rebuilding gives the same bytes.
func (h *harness) check(got Built, in []common.Utxo, w want, rebuild func() (Built, error)) *conway.ConwayTransaction {
	h.t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(got.Tx)
	if err != nil {
		h.t.Fatalf("gouroboros cannot decode the transaction: %v", err)
	}
	minFee, err := conway.MinFeeTx(tx, &h.pp)
	if err != nil {
		h.t.Fatal(err)
	}
	if got.Fee != minFee || tx.Fee().Uint64() != minFee {
		h.t.Fatalf("fee %d (tx %d), want the ledger minimum %d", got.Fee, tx.Fee().Uint64(), minFee)
	}
	h.checkValue(tx, in, w)
	h.checkWitnesses(tx, got, w.signers)
	again, err := rebuild()
	if err != nil {
		h.t.Fatal(err)
	}
	if !bytes.Equal(again.Tx, got.Tx) {
		h.t.Fatal("same request gave different CBOR")
	}
	return tx
}

func (h *harness) checkValue(tx *conway.ConwayTransaction, in []common.Utxo, w want) {
	h.t.Helper()
	byRef := map[string]uint64{}
	for _, u := range in {
		byRef[u.Id.String()] = u.Output.Amount().Uint64()
	}
	var consumed, produced uint64
	for _, i := range tx.Inputs() {
		v, ok := byRef[i.String()]
		if !ok {
			h.t.Fatalf("input %s was not offered", i)
		}
		consumed += v
	}
	for _, a := range tx.Withdrawals() {
		consumed += a.Uint64()
	}
	for _, o := range tx.Outputs() {
		produced += o.Amount().Uint64()
	}
	consumed += w.refund
	produced += tx.Fee().Uint64() + w.deposit
	if consumed != produced {
		h.t.Fatalf("consumed %d != produced %d (deposit %d refund %d)", consumed, produced, w.deposit, w.refund)
	}
}

func (h *harness) checkWitnesses(tx *conway.ConwayTransaction, got Built, signers []keys.Credential) {
	h.t.Helper()
	id := tx.Hash()
	if got.ID != id {
		h.t.Fatal("Built.ID is not the body hash")
	}
	have := map[keys.Credential]bool{}
	for _, wit := range tx.Witnesses().Vkey() {
		if err := common.VerifyVKeySignature(wit.Vkey, wit.Signature, id[:]); err != nil {
			h.t.Fatalf("witness does not verify: %v", err)
		}
		have[keys.Credential(common.Blake2b224Hash(wit.Vkey))] = true
	}
	for _, s := range signers {
		if !have[s] {
			h.t.Fatalf("no witness for key %x", s)
		}
	}
	if len(got.Signers) != len(have) {
		h.t.Fatalf("Built.Signers has %d keys, the tx %d witnesses", len(got.Signers), len(have))
	}
}
