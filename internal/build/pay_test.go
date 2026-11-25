package build

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func testParams() Params {
	return Params{MinFeeA: 44, MinFeeB: 155381, CoinsPerUtxoByte: 4310, MaxTxSize: 16384, NetworkID: 0}
}

func testWallets(t *testing.T) (alice, bob keys.Wallet) {
	t.Helper()
	var seed rng.Seed
	seed[31] = 7
	s, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s.Wallet(1), s.Wallet(2)
}

func utxo(owner common.Address, idx uint32, lovelace uint64) common.Utxo {
	var h common.Blake2b256
	h[0], h[31] = 0xab, byte(idx)
	out := babbage.BabbageTransactionOutput{
		OutputAddress: owner,
		OutputAmount:  mary.MaryTransactionOutputValue{Amount: lovelace},
	}
	return common.Utxo{Id: shelley.ShelleyTransactionInput{TxId: h, OutputIndex: idx}, Output: &out}
}

func buildPay(t *testing.T, amount uint64, ins ...uint64) (Built, keys.Wallet, keys.Wallet, []common.Utxo) {
	t.Helper()
	alice, bob := testWallets(t)
	var us []common.Utxo
	for i, v := range ins {
		us = append(us, utxo(alice.Address(), uint32(i), v))
	}
	b := New(testParams())
	got, err := b.Pay(context.Background(), alice, Payment{Inputs: us, To: bob.Address(), Amount: amount, TTL: 5000})
	if err != nil {
		t.Fatal(err)
	}
	return got, alice, bob, us
}

func decode(t *testing.T, raw []byte) ledger.Transaction {
	t.Helper()
	tx, err := ledger.NewTransactionFromCbor(6, raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tx
}

func TestApolloVersionPinned(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "github.com/Salvionied/apollo/v2").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "github.com/Salvionied/apollo/v2 v2.2.1" {
		t.Fatalf("apollo drifted from the reviewed version: %s", got)
	}
}

func TestPayFeeIsLinearInSize(t *testing.T) {
	got, _, _, _ := buildPay(t, 10_000_000, 50_000_000)
	tx := decode(t, got.Tx)
	size, err := common.TxSize(tx)
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	if want := p.MinFeeA*uint64(size) + p.MinFeeB; got.Fee != want || tx.Fee().Uint64() != want {
		t.Fatalf("fee %d (tx %d), want %d for size %d", got.Fee, tx.Fee().Uint64(), want, size)
	}
}

func TestPayConservesValueAndPaysChangeBack(t *testing.T) {
	got, alice, bob, _ := buildPay(t, 10_000_000, 30_000_000, 40_000_000)
	tx := decode(t, got.Tx)
	var out, paid, change uint64
	for _, o := range tx.Outputs() {
		out += o.Amount().Uint64()
		switch o.Address().String() {
		case bob.Address().String():
			paid += o.Amount().Uint64()
		case alice.Address().String():
			change += o.Amount().Uint64()
		}
	}
	if len(tx.Inputs()) != 2 {
		t.Fatalf("want both offered inputs spent, got %d", len(tx.Inputs()))
	}
	const in = 70_000_000
	if in != out+got.Fee {
		t.Fatalf("in %d != out %d + fee %d", in, out, got.Fee)
	}
	if paid != 10_000_000 || change == 0 {
		t.Fatalf("paid %d change %d", paid, change)
	}
	if got.TTL != 5000 || tx.TTL() != 5000 {
		t.Fatalf("ttl %d / %d", got.TTL, tx.TTL())
	}
}

func TestPayChangeAtOrAboveMinUTxO(t *testing.T) {
	got, _, _, _ := buildPay(t, 10_000_000, 50_000_000)
	tx := decode(t, got.Tx)
	for _, o := range tx.Outputs() {
		min := uint64(len(o.Cbor())+160) * testParams().CoinsPerUtxoByte
		if o.Amount().Uint64() < min {
			t.Fatalf("output %d below min-UTxO %d", o.Amount().Uint64(), min)
		}
	}
}

// Change too small to stand alone is folded into the fee: one output, and
// fee + payment account for the whole input.
func TestPayDustChangeFoldedIntoFee(t *testing.T) {
	// Pay almost everything: leftover after the fee is below min-UTxO.
	got, _, bob, _ := buildPay(t, 9_700_000, 10_000_000)
	tx := decode(t, got.Tx)
	outs := tx.Outputs()
	if len(outs) != 1 || outs[0].Address().String() != bob.Address().String() {
		t.Fatalf("want a single payment output, got %d", len(outs))
	}
	if outs[0].Amount().Uint64()+got.Fee != 10_000_000 {
		t.Fatalf("fee %d does not absorb the dust", got.Fee)
	}
	size, _ := common.TxSize(tx)
	if min := testParams().MinFeeA*uint64(size) + testParams().MinFeeB; got.Fee < min {
		t.Fatalf("fee %d below min %d", got.Fee, min)
	}
}

func TestPayWitnessVerifies(t *testing.T) {
	got, alice, _, _ := buildPay(t, 10_000_000, 50_000_000)
	tx := decode(t, got.Tx)
	ws := tx.Witnesses().Vkey()
	if len(ws) != 1 {
		t.Fatalf("want 1 vkey witness, got %d", len(ws))
	}
	id := tx.Hash()
	if err := common.VerifyVKeySignature(ws[0].Vkey, ws[0].Signature, id[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ws[0].Vkey, alice.PaymentVKey()) {
		t.Fatal("witness is not alice's key")
	}
	if got.ID != id {
		t.Fatal("Built.ID is not the body hash")
	}
	if len(got.Signers) != 1 || got.Signers[0] != alice.PaymentKeyHash() {
		t.Fatalf("signers %v", got.Signers)
	}
}

func TestPayUsesOnlyOffered(t *testing.T) {
	got, _, _, us := buildPay(t, 10_000_000, 30_000_000, 40_000_000, 50_000_000)
	offered := map[string]bool{}
	for _, u := range us {
		offered[u.Id.String()] = true
	}
	if len(got.Inputs) == 0 {
		t.Fatal("no inputs")
	}
	for _, in := range got.Inputs {
		if !offered[in] {
			t.Fatalf("input %s was not offered", in)
		}
	}
}

func TestPayDeterministic(t *testing.T) {
	a, _, _, _ := buildPay(t, 10_000_000, 30_000_000, 40_000_000)
	b, _, _, _ := buildPay(t, 10_000_000, 30_000_000, 40_000_000)
	if !bytes.Equal(a.Tx, b.Tx) {
		t.Fatal("same inputs gave different CBOR")
	}
}

func TestPayInsufficientIsTyped(t *testing.T) {
	alice, bob := testWallets(t)
	b := New(testParams())
	_, err := b.Pay(context.Background(), alice, Payment{
		Inputs: []common.Utxo{utxo(alice.Address(), 0, 3_000_000)}, To: bob.Address(), Amount: 10_000_000, TTL: 1,
	})
	var ins *InsufficientError
	if !errorsAs(err, &ins) || ins.Shortfall == 0 {
		t.Fatalf("want InsufficientError, got %v", err)
	}
}

func errorsAs(err error, target **InsufficientError) bool { return errors.As(err, target) }

func TestPayAcceptedByFakeChain(t *testing.T) {
	alice, bob := testWallets(t)
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	pp := fakechain.DefaultParams()
	chain, err := fakechain.New(fakechain.Config{
		Slots: slots, K: 3, BlockEvery: 2,
		Genesis: []fakechain.GenesisOutput{{Address: alice.Address(), Lovelace: 100_000_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := New(Params{
		MinFeeA: uint64(pp.MinFeeA), MinFeeB: uint64(pp.MinFeeB),
		CoinsPerUtxoByte: uint64(pp.AdaPerUtxoByte), MaxTxSize: uint32(pp.MaxTxSize), NetworkID: 0,
	})
	got, err := b.Pay(context.Background(), alice, Payment{
		Inputs: chain.UtxosAt(alice.Address()), To: bob.Address(), Amount: 10_000_000, TTL: chain.Slot() + 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.Submit(context.Background(), fakechain.EraConway, got.Tx); err != nil {
		t.Fatalf("fake chain rejected the payment: %v", err)
	}
}
