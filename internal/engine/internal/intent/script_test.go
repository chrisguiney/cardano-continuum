package intent

import (
	"bytes"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

func TestLockDatumByTemplate(t *testing.T) {
	t.Parallel()
	me, them := keys.Credential{1, 2, 3}, keys.Credential{4, 5, 6}
	if got := lockDatum("always_succeeds", 7, me, them, 99); !bytes.Equal(DatumKey(got), DatumKey(ActorDatum(7))) {
		t.Fatal("the shared lock does not carry the actor datum")
	}
	v, ok := contract.ParseVestingDatum(lockDatum("vesting", 7, me, them, 99).Data)
	if !ok || !bytes.Equal(v.Owner, me[:]) || v.LockUntil != 99 {
		t.Fatalf("vesting datum %+v, %v", v, ok)
	}
	e, ok := contract.ParseEscrowDatum(lockDatum("escrow", 7, me, them, 99).Data)
	if !ok || !bytes.Equal(e.Depositor, me[:]) || !bytes.Equal(e.Beneficiary, them[:]) || e.Deadline != 99 {
		t.Fatalf("escrow datum %+v, %v", e, ok)
	}
}

func TestCallFor(t *testing.T) {
	t.Parallel()
	me, other := keys.Credential{1}, keys.Credential{2}
	id := population.ActorID(7)
	paid := lockDatum("escrow", id, other, me, 100)
	held := lockDatum("escrow", id, me, other, 100)
	tests := []struct {
		name     string
		template string
		datum    common.Datum
		now      int64
		approved bool
		want     string
	}{
		{"own shared lock", "always_succeeds", ActorDatum(id), 0, true, callClaim},
		{"another's shared lock", "always_succeeds", ActorDatum(8), 0, true, ""},
		{"vesting due", "vesting", lockDatum("vesting", id, me, other, 100), 100, true, callClaim},
		{"vesting not yet due", "vesting", lockDatum("vesting", id, me, other, 100), 99, true, ""},
		{"another's vesting", "vesting", lockDatum("vesting", id, other, me, 100), 200, true, ""},
		{"escrow paid to me, approved", "escrow", paid, 0, true, callRelease},
		{"escrow paid to me, past its deadline", "escrow", paid, 200, true, callRelease},
		{"escrow paid to me, disputed", "escrow", paid, 0, false, ""},
		{"my escrow before its deadline", "escrow", held, 99, true, ""},
		{"my escrow at its deadline", "escrow", held, 100, false, callRefund},
		{"someone else's escrow", "escrow", lockDatum("escrow", id, other, other, 0), 200, true, ""},
		{"a vesting datum at an escrow", "escrow", lockDatum("vesting", id, me, other, 0), 200, true, ""},
	}
	for _, tt := range tests {
		d := tt.datum
		if got := callFor(tt.template, &d, id, me, tt.now, tt.approved); got != tt.want {
			t.Errorf("%s: callFor = %q, want %q", tt.name, got, tt.want)
		}
	}
	if callFor("vesting", nil, id, me, 0, true) != "" {
		t.Error("an output without a datum is claimable")
	}
}

func TestClaimArgs(t *testing.T) {
	t.Parallel()
	me := keys.Credential{1}
	if c := claimArgs("always_succeeds", callClaim, me, 50); len(c.signers) != 0 || c.from != 0 || !c.redeemer.Data.Equal(UnitDatum().Data) {
		t.Fatalf("shared claim %+v", c)
	}
	c := claimArgs("vesting", callClaim, me, 50)
	if len(c.signers) != 1 || c.signers[0] != common.Blake2b224(me) || c.from != 50 {
		t.Fatalf("vesting claim %+v", c)
	}
	release, refund := claimArgs("escrow", callRelease, me, 50), claimArgs("escrow", callRefund, me, 50)
	if !release.redeemer.Data.Equal(contract.Release.Data()) || len(release.signers) != 0 || release.from != 0 {
		t.Fatalf("release %+v: the depositor co-signs, the beneficiary need not", release)
	}
	if !refund.redeemer.Data.Equal(contract.Refund.Data()) || len(refund.signers) != 1 || refund.from != 50 {
		t.Fatalf("refund %+v: the depositor signs, valid from the tip", refund)
	}
}

// A share of escrows is disputed: the depositor never approves, so the
// beneficiary leaves it and the depositor refunds it.
func TestApproved(t *testing.T) {
	t.Parallel()
	var yes, no int
	for i := range 400 {
		if approved(wire.Ref{Tx: wire.TxID{byte(i), byte(i >> 8)}, Index: uint32(i % 3)}) {
			yes++
		} else {
			no++
		}
	}
	if no == 0 || yes < 2*no {
		t.Fatalf("approved %d, disputed %d", yes, no)
	}
}
