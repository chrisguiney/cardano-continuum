package node

import (
	"encoding/binary"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// hash224 returns a credential or pool hash filled with b.
func hash224(b byte) common.Blake2b224 {
	var h common.Blake2b224
	for i := range h {
		h[i] = b
	}
	return h
}

func txid(n uint64) chain.TxID {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	return common.Blake2b256Hash(b[:])
}

func utxo(tx chain.TxID, i uint32, a common.Address, coin uint64) common.Utxo {
	return common.Utxo{
		Id: shelley.ShelleyTransactionInput{TxId: tx, OutputIndex: i},
		Output: babbage.BabbageTransactionOutput{
			OutputAddress: a,
			OutputAmount:  mary.MaryTransactionOutputValue{Amount: coin},
		},
	}
}

func testSlots(t testing.TB) sched.Slots {
	t.Helper()
	s, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustApply(t testing.TB, v *chain.View, b chain.Block) []chain.Effect {
	t.Helper()
	eff, err := v.Apply(b)
	if err != nil {
		t.Fatalf("apply block %d: %v", b.Number, err)
	}
	return eff
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
