package contract

import (
	"bytes"
	"testing"

	"github.com/blinklabs-io/plutigo/data"
)

func TestVestingDatumRoundTrip(t *testing.T) {
	t.Parallel()
	owner := bytes.Repeat([]byte{7}, 28)
	d := VestingDatum{Owner: owner, LockUntil: 1_700_000}
	got, ok := ParseVestingDatum(d.Data())
	if !ok || !bytes.Equal(got.Owner, owner) || got.LockUntil != 1_700_000 {
		t.Fatalf("ParseVestingDatumDatum(%v) = %+v, %v", d.Data(), got, ok)
	}
	if _, ok := ParseVestingDatum(data.NewConstr(0)); ok {
		t.Fatal("parsed an empty constructor as a vesting datum")
	}
	if _, ok := ParseVestingDatum(EscrowDatum{Depositor: owner, Beneficiary: owner}.Data()); ok {
		t.Fatal("parsed an escrow datum as a vesting datum")
	}
}

func TestEscrowDatumRoundTrip(t *testing.T) {
	t.Parallel()
	a, b := bytes.Repeat([]byte{1}, 28), bytes.Repeat([]byte{2}, 28)
	got, ok := ParseEscrowDatum(EscrowDatum{Depositor: a, Beneficiary: b}.Data())
	if !ok || !bytes.Equal(got.Depositor, a) || !bytes.Equal(got.Beneficiary, b) {
		t.Fatalf("ParseEscrowDatum = %+v, %v", got, ok)
	}
}

func TestEscrowActions(t *testing.T) {
	t.Parallel()
	// Release is the first constructor of EscrowAction, Refund the second
	// (contracts/templates/lib/continuum/types.ak).
	if !Release.Data().Equal(data.NewConstr(0)) || !Refund.Data().Equal(data.NewConstr(1)) {
		t.Fatalf("actions %v %v", Release.Data(), Refund.Data())
	}
	if Release.String() != "release" || Refund.String() != "refund" {
		t.Fatalf("names %q %q", Release, Refund)
	}
	if a, ok := ParseEscrowAction(data.NewConstr(1)); !ok || a != Refund {
		t.Fatalf("ParseEscrowAction(constr 1) = %v, %v", a, ok)
	}
	if _, ok := ParseEscrowAction(data.NewConstr(2)); ok {
		t.Fatal("parsed constructor 2 as an escrow action")
	}
}

func TestOwner(t *testing.T) {
	t.Parallel()
	k := bytes.Repeat([]byte{9}, 28)
	for _, d := range []data.PlutusData{VestingDatum{Owner: k, LockUntil: 5}.Data(), EscrowDatum{Depositor: k, Beneficiary: k}.Data()} {
		if got, ok := Owner(d); !ok || !bytes.Equal(got, k) {
			t.Fatalf("Owner(%v) = %x, %v", d, got, ok)
		}
	}
	if _, ok := Owner(data.NewByteString(k)); ok {
		t.Fatal("found an owner in a bare byte string")
	}
}
