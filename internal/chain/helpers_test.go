package chain

import (
	"encoding/binary"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"go.uber.org/goleak"
)

// hash224 returns a credential or pool hash filled with b.
func hash224(b byte) common.Blake2b224 {
	var h common.Blake2b224
	for i := range h {
		h[i] = b
	}
	return h
}

// addrOf returns a base address paying key hash n, staked to key hash n.
func addrOf(t testing.TB, n byte) common.Address {
	t.Helper()
	p, s := hash224(n), hash224(n)
	a, err := common.NewAddressFromParts(common.AddressTypeKeyKey, common.AddressNetworkTestnet, p[:], s[:])
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func txid(n uint64) TxID {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	return common.Blake2b256Hash(b[:])
}

func output(a common.Address, coin uint64) common.TransactionOutput {
	return babbage.BabbageTransactionOutput{
		OutputAddress: a,
		OutputAmount:  mary.MaryTransactionOutputValue{Amount: coin},
	}
}

func utxo(tx TxID, i uint32, a common.Address, coin uint64) common.Utxo {
	return common.Utxo{
		Id:     shelley.ShelleyTransactionInput{TxId: tx, OutputIndex: i},
		Output: output(a, coin),
	}
}

// pays builds a transaction spending ins and creating one output per pay.
type pay struct {
	to   common.Address
	coin uint64
}

func mkTx(id TxID, ins []Ref, pays ...pay) Tx {
	tx := Tx{Hash: id, Spends: ins}
	for i, p := range pays {
		tx.Creates = append(tx.Creates, utxo(id, uint32(i), p.to, p.coin))
	}
	return tx
}

// next returns a block extending prev (nil for the first block) one slot
// later, with salt distinguishing forks at the same height.
func next(prev *Block, salt uint64, txs ...Tx) Block {
	b := Block{Number: 1, Point: Point{Slot: 1}, Txs: txs}
	if prev != nil {
		b.Number = prev.Number + 1
		b.Point.Slot = prev.Point.Slot + 1
		b.Prev = prev.Point.Hash
	}
	var buf [56]byte
	copy(buf[:32], b.Prev[:])
	binary.BigEndian.PutUint64(buf[32:], b.Point.Slot)
	binary.BigEndian.PutUint64(buf[40:], salt)
	binary.BigEndian.PutUint64(buf[48:], uint64(len(txs)))
	b.Point.Hash = common.Blake2b256Hash(buf[:])
	return b
}

func testSlots(t testing.TB) sched.Slots {
	t.Helper()
	s, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// actors 1 and 2 own addresses 1 and 2, stake credentials 1 and 2; actor 9
// operates pool 9.
func testBook(t testing.TB) *Book {
	t.Helper()
	b := NewBook()
	for _, n := range []byte{1, 2} {
		if err := b.AddAddress(addrOf(t, n), population.ActorID(n)); err != nil {
			t.Fatal(err)
		}
		b.AddStake(hash224(n), population.ActorID(n))
	}
	b.AddPool(hash224(9), 9)
	b.AddDRep(hash224(8), 8)
	return b
}

func newView(t testing.TB, k uint64, genesis ...common.Utxo) *View {
	t.Helper()
	v, err := New(Config{K: k, Slots: testSlots(t), Book: testBook(t)}, State{Utxos: genesis})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustApply(t testing.TB, v *View, b Block) []Effect {
	t.Helper()
	eff, err := v.Apply(b)
	if err != nil {
		t.Fatalf("apply block %d: %v", b.Number, err)
	}
	return eff
}

func kinds(eff []Effect) []EffectKind {
	out := make([]EffectKind, len(eff))
	for i, e := range eff {
		out[i] = e.Kind
	}
	return out
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
