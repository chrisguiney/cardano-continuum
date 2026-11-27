package build

import (
	"context"
	"errors"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func TestFanOutPaysEveryOutputAndChangeBack(t *testing.T) {
	t.Parallel()
	var seed rng.Seed
	seed[31] = 9
	s, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	faucet := s.Faucet()
	outs := []Output{
		{To: s.Wallet(1).Address(), Amount: 5_000_000},
		{To: s.Wallet(2).Address(), Amount: 7_000_000},
		{To: s.Wallet(3).Address(), Amount: 9_000_000},
	}
	const in = 100_000_000
	got, err := New(testParams()).FanOut(context.Background(), faucet, Spend{
		Inputs: []common.Utxo{utxo(faucet.Address(), 0, in)}, TTL: 900,
	}, outs)
	if err != nil {
		t.Fatal(err)
	}
	tx := decode(t, got.Tx)
	paid := map[string]uint64{}
	var total uint64
	for _, o := range tx.Outputs() {
		paid[o.Address().String()] += o.Amount().Uint64()
		total += o.Amount().Uint64()
	}
	for _, o := range outs {
		if paid[o.To.String()] != o.Amount {
			t.Errorf("%s paid %d, want %d", o.To, paid[o.To.String()], o.Amount)
		}
	}
	if paid[faucet.Address().String()] == 0 {
		t.Error("no change back to the payer")
	}
	if total+got.Fee != in {
		t.Fatalf("in %d != out %d + fee %d", in, total, got.Fee)
	}
	if got.TTL != 900 {
		t.Fatalf("ttl %d", got.TTL)
	}
}

func TestFanOutInsufficientIsTyped(t *testing.T) {
	t.Parallel()
	alice, bob := testWallets(t)
	_, err := New(testParams()).FanOut(context.Background(), alice, Spend{
		Inputs: []common.Utxo{utxo(alice.Address(), 0, 3_000_000)}, TTL: 900,
	}, []Output{{To: bob.Address(), Amount: 2_000_000}, {To: bob.Address(), Amount: 2_000_000}})
	var ie *InsufficientError
	if !errors.As(err, &ie) || ie.Need != 4_000_000 {
		t.Fatalf("err = %v, want InsufficientError needing 4000000", err)
	}
}

func TestFanOutNeedsAnOutput(t *testing.T) {
	t.Parallel()
	alice, _ := testWallets(t)
	if _, err := New(testParams()).FanOut(context.Background(), alice, Spend{
		Inputs: []common.Utxo{utxo(alice.Address(), 0, 30_000_000)}, TTL: 900,
	}, nil); err == nil {
		t.Fatal("fan-out with no outputs built")
	}
}
