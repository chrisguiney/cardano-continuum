package submit

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const (
	testNetwork  = 0
	testLovelace = 100_000_000
	okFee        = 2_000_000
)

type fixture struct {
	t     *testing.T
	chain *fakechain.Chain
	alice keys.Wallet
	bob   keys.Wallet
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := keys.New(rng.Seed{1}, testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, alice: store.Wallet(1), bob: store.Wallet(2)}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	f.chain, err = fakechain.New(fakechain.Config{
		Slots:      slots,
		NetworkID:  testNetwork,
		K:          3,
		BlockEvery: 2,
		Genesis: []fakechain.GenesisOutput{
			{Address: f.alice.Address(), Lovelace: testLovelace},
			{Address: f.bob.Address(), Lovelace: testLovelace},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func genesisRef(t *testing.T, w keys.Wallet) [2]any {
	t.Helper()
	b, err := w.Address().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	h := common.Blake2b256Hash(b)
	return [2]any{h[:], uint32(0)}
}

// spec describes a one-input payment from the sender to dest with change
// back to the sender.
type spec struct {
	dest    common.Address
	pay     uint64
	fee     uint64
	change  int64 // added to the balanced change
	ttl     uint64
	network *uint8
	extraIn bool
	signers []keys.Wallet
}

func (f *fixture) good() spec {
	return spec{dest: f.bob.Address(), pay: 10_000_000, fee: okFee, signers: []keys.Wallet{f.alice}}
}

func (f *fixture) encode(s spec) []byte {
	return encodeTx(f.t, f.alice, testLovelace, s)
}

// encodeTx encodes a Conway transaction spending from's genesis output of
// total lovelace.
func encodeTx(t *testing.T, from keys.Wallet, total uint64, s spec) []byte {
	t.Helper()
	ins := []any{genesisRef(t, from)}
	if s.extraIn {
		ins = append(ins, [2]any{make([]byte, 32), uint32(3)})
	}
	addr := func(a common.Address) []byte {
		b, err := a.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	change := int64(total-s.pay-s.fee) + s.change
	body := map[uint]any{
		0: ins,
		1: []any{
			map[uint]any{0: addr(s.dest), 1: s.pay},
			map[uint]any{0: addr(from.Address()), 1: uint64(change)},
		},
		2: s.fee,
	}
	if s.ttl != 0 {
		body[3] = s.ttl
	}
	if s.network != nil {
		body[15] = *s.network
	}
	raw, err := cbor.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	var wits []any
	for _, w := range s.signers {
		sig := w.SignTxBody(raw)
		wits = append(wits, []any{sig.VKey, sig.Signature})
	}
	ws := map[uint]any{}
	if len(wits) > 0 {
		ws[0] = wits
	}
	tx, err := cbor.Encode([]any{cbor.RawMessage(raw), ws, true, nil})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
