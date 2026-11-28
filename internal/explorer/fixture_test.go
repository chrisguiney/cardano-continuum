package explorer

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/ouroboros-mock/fixtures"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// chainGen builds a linear Conway chain of real gouroboros blocks.
type chainGen struct {
	t      *testing.T
	prev   common.Blake2b256
	number uint64
	slot   uint64
	issuer common.IssuerVkey
	blocks []common.Blake2b256
}

func newGen(t *testing.T, issuer byte) *chainGen {
	g := &chainGen{t: t}
	g.issuer = common.IssuerVkey(bytes.Repeat([]byte{issuer}, 32))
	return g
}

func testSlots(t *testing.T) sched.Slots {
	s, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// next builds the next block, slot slots after the last, holding txs.
func (g *chainGen) next(txs ...[]byte) []byte {
	g.t.Helper()
	var gtxs []common.Transaction
	for _, raw := range txs {
		tx, err := conway.NewConwayTransactionFromCbor(raw)
		if err != nil {
			g.t.Fatalf("decode fixture tx: %v", err)
		}
		gtxs = append(gtxs, tx)
	}
	g.number++
	g.slot += 5
	b, err := fixtures.NewBlockBuilder(conway.EraConway).
		WithBlockNumber(g.number).WithSlot(g.slot).WithPreviousHash(g.prev).
		WithIssuerVkey(g.issuer).WithProtocolVersion(10, 0).
		WithTransactions(gtxs...).Build()
	if err != nil {
		g.t.Fatal(err)
	}
	g.prev = b.Hash()
	g.blocks = append(g.blocks, b.Hash())
	return b.Cbor()
}

func (g *chainGen) hash(i int) string { return g.blocks[i].String() }

func enc(t *testing.T, v any) []byte {
	t.Helper()
	b, err := cbor.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fill(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

// shelleyAddr is an enterprise address (key payment credential) for network 0.
func shelleyAddr(b byte) []byte { return append([]byte{0x60}, fill(b, 28)...) }

// txSpec describes a Conway transaction body field by field.
type txSpec struct {
	body    map[uint]any
	wits    map[uint]any
	invalid bool
}

func (s txSpec) cbor(t *testing.T) []byte {
	t.Helper()
	wits := s.wits
	if wits == nil {
		wits = map[uint]any{}
	}
	return enc(t, []any{cbor.RawMessage(enc(t, s.body)), wits, !s.invalid, nil})
}

// pay is a one-input, two-output payment tx.
func pay(t *testing.T, in []byte, idx uint32, to byte, coin uint64) []byte {
	return txSpec{body: map[uint]any{
		0: []any{[]any{in, idx}},
		1: []any{map[uint]any{0: shelleyAddr(to), 1: coin}, map[uint]any{0: shelleyAddr(0xAA), 1: uint64(5_000_000)}},
		2: uint64(170_000),
		3: uint64(9_999),
	}}.cbor(t)
}

// rich is a Conway tx with a stake delegation, a pool retirement, a DRep
// registration, a vote, a proposal, a Plutus V2 script with a spend and a
// mint redeemer, a mint, a required signer and a reference input.
func rich(t *testing.T, in []byte) (raw []byte, policy []byte) {
	policy = fill(0x33, 28)
	script := []byte{0x4e, 0x4d, 0x01, 0x00, 0x00, 0x22, 0x22, 0x01}
	// Votes are a map keyed by arrays, which Go cannot hold as a map key, so
	// the map is concatenated by hand.
	votes := append([]byte{0xa1}, enc(t, []any{uint8(2), fill(0x99, 28)})...)
	votes = append(votes, 0xa1)
	votes = append(votes, enc(t, []any{fill(0x11, 32), uint32(0)})...)
	votes = append(votes, enc(t, []any{uint8(1), nil})...)
	body := map[uint]any{
		0: []any{[]any{in, uint32(0)}},
		1: []any{map[uint]any{0: shelleyAddr(0xB1), 1: uint64(3_000_000)}},
		2: uint64(300_000),
		3: uint64(10_000),
		4: []any{
			[]any{uint(2), []any{uint(0), fill(0x44, 28)}, fill(0x55, 28)},
			[]any{uint(4), fill(0x55, 28), uint64(7)},
			[]any{uint(16), []any{uint(0), fill(0x66, 28)}, uint64(500_000_000), nil},
		},
		9:  map[cbor.ByteString]map[cbor.ByteString]int64{cbor.NewByteString(policy): {cbor.NewByteString([]byte("tok")): 7}},
		14: []any{fill(0x77, 28)},
		18: []any{[]any{fill(0x88, 32), uint32(1)}},
		19: cbor.RawMessage(votes),
		20: []any{[]any{uint64(100_000_000_000), append([]byte{0xe0}, fill(0xC1, 28)...), []any{uint(6)}, []any{"https://example.test/a", fill(0x12, 32)}}},
	}
	raw = txSpec{body: body, wits: map[uint]any{
		6: []any{script},
		5: []any{
			[]any{uint(0), uint(0), uint(0), []any{uint64(1000), uint64(2000)}},
			[]any{uint(1), uint(0), uint(0), []any{uint64(300), uint64(400)}},
		},
	}}.cbor(t)
	return raw, policy
}

func (g *chainGen) slotOf(i int) uint64 { return uint64(i+1) * 5 }

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

func fillHex(n int) []byte { return bytes.Repeat([]byte{'a'}, n) }
