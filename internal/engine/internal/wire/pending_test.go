package wire

import (
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// TestPendingRoundTrip: the snapshot records an intent as Pending itself,
// so a checkpoint restores every field, the inlined parts' included.
func TestPendingRoundTrip(t *testing.T) {
	ref := Ref{Tx: TxID{7}, Index: 2}
	p := Pending{
		ID:     1,
		Actor:  2,
		Refill: true,
		Attempt: Attempt{
			Tx:        TxID{3},
			TTL:       4,
			Cbor:      []byte{5},
			Awaiting:  true,
			M:         submit.Machine{State: submit.Pending, Attempts: 2, Sent: true},
			Txs:       []TxID{{3}},
			NotBefore: 8,
		},
		Args: Args{
			Kind:     "k",
			To:       6,
			Amount:   7,
			Locked:   &ref,
			Contract: 11,
			Epoch:    9,
			Register: true,
			Update:   true,
		},
		Gov: Gov{
			Action:  &ref,
			Choice:  1,
			GovType: "g",
			Power:   12,
			Probe:   true,
		},
		Faults: Faults{
			Injected: mistake.Of(mistake.DoubleSubmit),
			Carried:  mistake.Of(mistake.DoubleSubmit),
			Lost:     10,
		},
		Strike: Strike{
			Attack: "a",
			Want:   &Want{Classes: []string{"c"}, Accept: true},
			Rivals: []Rival{{ID: TxID{4}}},
			Jump:   true,
		},
	}
	assertNoZero(t, reflect.ValueOf(p), "Pending")

	b, err := eventlog.Encode(&p)
	if err != nil {
		t.Fatal(err)
	}

	var back Pending
	if err := eventlog.Decode(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip\n got %+v\nwant %+v", back, p)
	}
}

// assertNoZero fails if any field of v, walking into embedded parts, is
// zero: a fixture that leaves one zero cannot show it round-trips.
func assertNoZero(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	for i := range v.NumField() {
		f, sf := v.Field(i), v.Type().Field(i)
		if sf.Anonymous {
			assertNoZero(t, f, path+"."+sf.Name)
			continue
		}
		if f.IsZero() {
			t.Fatalf("fixture leaves %s.%s zero", path, sf.Name)
		}
	}
}
