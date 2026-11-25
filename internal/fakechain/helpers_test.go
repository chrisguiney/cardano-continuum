package fakechain

import (
	"context"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const (
	testNetwork  = 0
	testLovelace = 100_000_000
	// feePlaceholder has the same CBOR width as any realistic fee, so a body
	// sized with it is the size of the body carrying the real fee.
	feePlaceholder = 1_000_000
)

type fixture struct {
	t     *testing.T
	store *keys.Store
	chain *Chain
	alice keys.Wallet
	bob   keys.Wallet
}

// newFixture returns a chain whose genesis gives alice and bob one output of
// testLovelace each, one block every 2 slots, k = 3.
func newFixture(t *testing.T, mod func(*Config)) *fixture {
	t.Helper()
	store, err := keys.New(rng.Seed{1}, testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: store, alice: store.Wallet(1), bob: store.Wallet(2)}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Slots:      slots,
		NetworkID:  testNetwork,
		K:          3,
		BlockEvery: 2,
		Genesis: []GenesisOutput{
			{Address: f.alice.Address(), Lovelace: testLovelace},
			{Address: f.bob.Address(), Lovelace: testLovelace},
		},
	}
	if mod != nil {
		mod(&cfg)
	}
	f.chain, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) genesisRef(w keys.Wallet) Ref {
	f.t.Helper()
	b, err := w.Address().Bytes()
	if err != nil {
		f.t.Fatal(err)
	}
	return Ref{TxID: common.Blake2b256Hash(b), Index: 0}
}

type out struct {
	addr common.Address
	coin uint64
}

// txSpec describes a Conway transaction for a test. Zero fields are left
// out of the body. fee < 0 means "the minimum fee".
type txSpec struct {
	ins     []Ref
	outs    []out
	fee     int64
	ttl     uint64
	start   uint64
	network *uint8
	signers []keys.Wallet
	// bodyHook may change the encoded body map before signing.
	bodyHook func(map[uint]any)
}

func (s txSpec) body(fee uint64) map[uint]any {
	ins := make([]any, len(s.ins))
	for i, r := range s.ins {
		ins[i] = []any{r.TxID[:], r.Index}
	}
	outs := make([]any, len(s.outs))
	for i, o := range s.outs {
		b, err := o.addr.Bytes()
		if err != nil {
			panic(err)
		}
		outs[i] = map[uint]any{0: b, 1: o.coin}
	}
	m := map[uint]any{0: ins, 1: outs, 2: fee}
	if s.ttl != 0 {
		m[3] = s.ttl
	}
	if s.start != 0 {
		m[8] = s.start
	}
	if s.network != nil {
		m[15] = *s.network
	}
	if s.bodyHook != nil {
		s.bodyHook(m)
	}
	return m
}

func (s txSpec) encode(t *testing.T, fee uint64) []byte {
	t.Helper()
	body, err := cbor.Encode(s.body(fee))
	if err != nil {
		t.Fatal(err)
	}
	var wits []any
	for _, w := range s.signers {
		sig := w.SignTxBody(body)
		wits = append(wits, []any{sig.VKey, sig.Signature})
	}
	ws := map[uint]any{}
	if len(wits) > 0 {
		ws[0] = wits
	}
	tx, err := cbor.Encode([]any{cbor.RawMessage(body), ws, true, nil})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// build encodes the transaction. With fee < 0 it computes the minimum fee
// with gouroboros' Conway MinFeeTx on a placeholder-fee encoding.
func (s txSpec) build(t *testing.T) []byte {
	t.Helper()
	if s.fee >= 0 {
		return s.encode(t, uint64(s.fee))
	}
	return s.encode(t, minFee(t, s.encode(t, feePlaceholder)))
}

func minFee(t *testing.T, txCbor []byte) uint64 {
	t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		t.Fatal(err)
	}
	pp := DefaultParams()
	fee, err := conway.MinFeeTx(tx, &pp)
	if err != nil {
		t.Fatal(err)
	}
	return fee
}

// pay builds a minimum-fee payment of amount from w's genesis output to
// dest, with change back to w.
func (f *fixture) pay(w keys.Wallet, dest common.Address, amount uint64) txSpec {
	s := txSpec{
		ins:     []Ref{f.genesisRef(w)},
		outs:    []out{{dest, amount}, {w.Address(), 0}},
		fee:     -1,
		signers: []keys.Wallet{w},
	}
	return s.balanced(f.t, testLovelace)
}

// balanced sets the last output to inputs - other outputs - min fee.
func (s txSpec) balanced(t *testing.T, inputs uint64) txSpec {
	t.Helper()
	last := len(s.outs) - 1
	rest := uint64(0)
	for _, o := range s.outs[:last] {
		rest += o.coin
	}
	s.outs[last].coin = inputs - rest - feePlaceholder
	fee := minFee(t, s.encode(t, feePlaceholder))
	s.outs[last].coin = inputs - rest - fee
	s.fee = int64(fee)
	return s
}

func (f *fixture) submit(tx []byte) error {
	return f.chain.Submit(context.Background(), EraConway, tx)
}

func (f *fixture) mustSubmit(tx []byte) TxID {
	f.t.Helper()
	if err := f.submit(tx); err != nil {
		f.t.Fatalf("submit: %v", err)
	}
	return txID(f.t, tx)
}

func (f *fixture) advance(slot uint64) {
	f.t.Helper()
	if err := f.chain.AdvanceTo(slot); err != nil {
		f.t.Fatalf("advance to %d: %v", slot, err)
	}
}

func txID(t *testing.T, txCbor []byte) TxID {
	t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		t.Fatal(err)
	}
	return tx.Hash()
}
