package fakechain

import (
	"errors"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

func rejectClasses(t *testing.T, err error) []Class {
	t.Helper()
	var rej *RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("error %v is not a *RejectError", err)
	}
	var out []Class
	for _, f := range rej.Failures {
		out = append(out, f.Class)
	}
	return out
}

func hasClass(cs []Class, c Class) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// TestValidTxAccepted is the "does not fire" half of every class test: a
// well-formed payment raises no failure of any class.
func TestValidTxAccepted(t *testing.T) {
	f := newFixture(t, nil)
	tx := f.pay(f.alice, f.bob.Address(), 10_000_000).build(t)
	if err := f.submit(tx); err != nil {
		t.Fatalf("valid tx rejected: %v", err)
	}
	if !f.chain.HasTx(txID(t, tx)) {
		t.Fatal("accepted tx is not in the mempool")
	}
}

func TestRuleClasses(t *testing.T) {
	wrongNet := uint8(1)
	tests := []struct {
		name string
		want Class
		// broken returns a spec that must fail with want.
		broken func(f *fixture) txSpec
		// fixed returns the nearest spec that must not fail with want.
		fixed func(f *fixture) txSpec
	}{
		{
			name: "unknown input",
			want: BadInputs,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.ins = append(s.ins, Ref{TxID: TxID{0xee}, Index: 3})
				return s
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "fee one below a*size+b",
			want: FeeTooSmall,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.fee--
				s.outs[1].coin++
				return s
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "outputs exceed inputs",
			want: ValueNotConserved,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.outs[1].coin++
				return s
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "ttl already passed",
			want: OutsideValidityInterval,
			broken: func(f *fixture) txSpec {
				// The tip is at slot 10 and the mempool validates at the
				// next slot; a ttl is exclusive.
				f.advance(10)
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.ttl = 11
				return s.balanced(f.t, testLovelace)
			},
			fixed: func(f *fixture) txSpec {
				f.advance(10)
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.ttl = 12
				return s.balanced(f.t, testLovelace)
			},
		},
		{
			name: "validity start in the future",
			want: OutsideValidityInterval,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.start = 5
				return s.balanced(f.t, testLovelace)
			},
			fixed: func(f *fixture) txSpec {
				f.advance(5)
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.start = 5
				return s.balanced(f.t, testLovelace)
			},
		},
		{
			name: "output below min-UTxO",
			want: OutputTooSmall,
			broken: func(f *fixture) txSpec {
				return f.pay(f.alice, f.bob.Address(), 100_000)
			},
			fixed: func(f *fixture) txSpec {
				return f.pay(f.alice, f.bob.Address(), 1_000_000)
			},
		},
		{
			name: "input owner did not sign",
			want: MissingVKeyWitnesses,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.signers = []keys.Wallet{f.bob}
				return s
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "signature over another body",
			want: InvalidWitnesses,
			broken: func(f *fixture) txSpec {
				return f.pay(f.alice, f.bob.Address(), 10_000_000)
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "output address on another network",
			want: WrongNetwork,
			broken: func(f *fixture) txSpec {
				other, err := keys.New([32]byte{1}, 1)
				if err != nil {
					f.t.Fatal(err)
				}
				return f.pay(f.alice, other.Wallet(2).Address(), 10_000_000)
			},
			fixed: func(f *fixture) txSpec { return f.pay(f.alice, f.bob.Address(), 10_000_000) },
		},
		{
			name: "body network id differs",
			want: WrongNetworkInTxBody,
			broken: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				s.network = &wrongNet
				return s.balanced(f.t, testLovelace)
			},
			fixed: func(f *fixture) txSpec {
				s := f.pay(f.alice, f.bob.Address(), 10_000_000)
				n := uint8(testNetwork)
				s.network = &n
				return s.balanced(f.t, testLovelace)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/fires", func(t *testing.T) {
			f := newFixture(t, nil)
			s := tt.broken(f)
			var tx []byte
			if tt.want == InvalidWitnesses {
				tx = encodeTampered(t, s)
			} else {
				tx = s.build(t)
			}
			err := f.submit(tx)
			if err == nil {
				t.Fatalf("accepted, want %s", tt.want)
			}
			if cs := rejectClasses(t, err); !hasClass(cs, tt.want) {
				t.Fatalf("classes %v, want %s", cs, tt.want)
			}
			if f.chain.HasTx(txID(t, tx)) {
				t.Fatal("rejected tx entered the mempool")
			}
		})
		t.Run(tt.name+"/quiet", func(t *testing.T) {
			f := newFixture(t, nil)
			if err := f.submit(tt.fixed(f).build(t)); err != nil {
				t.Fatalf("rejected: %v", err)
			}
		})
	}
}

// encodeTampered signs the body s describes, then transmits a body with a
// different fee, so the witness key is right but its signature is not.
func encodeTampered(t *testing.T, s txSpec) []byte {
	t.Helper()
	signed := s
	good := signed.build(t)
	tampered := signed
	tampered.fee++
	tampered.outs = append([]out(nil), signed.outs...)
	tampered.outs[len(tampered.outs)-1].coin--
	bad := tampered.build(t)
	return spliceWitnesses(t, bad, good)
}

// TestRejectAlsoReportsConsequences mirrors the real ledger, which reports
// every failing predicate: an unknown input is also missing from the
// consumed side of the balance.
func TestRejectAlsoReportsConsequences(t *testing.T) {
	f := newFixture(t, nil)
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	s.ins = []Ref{{TxID: TxID{0xee}, Index: 0}}
	cs := rejectClasses(t, f.submit(s.build(t)))
	if !hasClass(cs, BadInputs) || !hasClass(cs, ValueNotConserved) {
		t.Fatalf("classes %v, want BadInputsUTxO and ValueNotConservedUTxO", cs)
	}
}

func TestUndecodableTx(t *testing.T) {
	f := newFixture(t, nil)
	err := f.submit([]byte{0x01, 0x02})
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

func TestUnsupportedEra(t *testing.T) {
	f := newFixture(t, nil)
	tx := f.pay(f.alice, f.bob.Address(), 10_000_000).build(t)
	if err := f.chain.Submit(t.Context(), 5, tx); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestUnsupportedFeature(t *testing.T) {
	f := newFixture(t, nil)
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	// A treasury donation (body key 22).
	s.bodyHook = func(m map[uint]any) { m[22] = 1 }
	if err := f.submit(s.build(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// spliceWitnesses returns tx with the witness set of donor.
func spliceWitnesses(t *testing.T, tx, donor []byte) []byte {
	t.Helper()
	var a, b []cbor.RawMessage
	if _, err := cbor.Decode(tx, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := cbor.Decode(donor, &b); err != nil {
		t.Fatal(err)
	}
	a[1] = b[1]
	out, err := cbor.Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A node reports a missing input as BadInputsUTxO alone. gouroboros'
// required-redeemers rule resolves every input and returns the resolution
// error, which must not surface as a second class.
func TestMissingInputIsOnlyBadInputs(t *testing.T) {
	f := newFixture(t, nil)
	s := f.pay(f.alice, f.bob.Address(), 10_000_000)
	s.ins = append(s.ins, Ref{TxID: TxID{0xee}, Index: 3})
	cs := rejectClasses(t, f.submit(s.build(t)))
	if !hasClass(cs, BadInputs) {
		t.Fatalf("classes %v, want BadInputsUTxO", cs)
	}
	if hasClass(cs, MissingRedeemers) {
		t.Fatalf("classes %v: MissingRedeemers must not accompany a missing input", cs)
	}
}

// TestUnresolvedAuxInputsAreBadInputs: collateral (body key 13) and reference
// (key 18) inputs missing from the UTxO set are BadInputsUTxO, reported once
// even when a spent input is also missing.
func TestUnresolvedAuxInputsAreBadInputs(t *testing.T) {
	missing := []any{[]any{make([]byte, 32), uint64(7)}}
	for _, tc := range []struct {
		name      string
		key       uint
		spentGone bool
	}{
		{"collateral", 13, false},
		{"reference", 18, false},
		{"collateral and spent", 13, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			s := f.pay(f.alice, f.bob.Address(), 10_000_000)
			if tc.spentGone {
				s.ins = []Ref{{TxID: TxID{0xee}, Index: 0}}
			}
			s.bodyHook = func(m map[uint]any) { m[tc.key] = missing }
			n := 0
			for _, c := range rejectClasses(t, f.submit(s.build(t))) {
				if c == BadInputs {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("BadInputsUTxO reported %d times, want 1", n)
			}
		})
	}
}
