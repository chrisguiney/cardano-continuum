package intent

import (
	"bytes"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

func TestLockDatumByTemplate(t *testing.T) {
	t.Parallel()
	me := keys.Credential{1, 2, 3}
	if got := lockDatum("always_succeeds", 7, me, 99); !bytes.Equal(DatumKey(got), DatumKey(ActorDatum(7))) {
		t.Fatal("the shared lock does not carry the actor datum")
	}
	v, ok := contract.ParseVestingDatum(lockDatum("vesting", 7, me, 99).Data)
	if !ok || !bytes.Equal(v.Owner, me[:]) || v.LockUntil != 99 {
		t.Fatalf("vesting datum %+v, %v", v, ok)
	}
	e, ok := contract.ParseEscrowDatum(lockDatum("escrow", 7, me, 99).Data)
	if !ok || !bytes.Equal(e.Depositor, me[:]) || !bytes.Equal(e.Beneficiary, me[:]) {
		t.Fatalf("escrow datum %+v, %v", e, ok)
	}
}

func TestClaimable(t *testing.T) {
	t.Parallel()
	me, other := keys.Credential{1}, keys.Credential{2}
	id := population.ActorID(7)
	tests := []struct {
		name     string
		template string
		datum    common.Datum
		now      int64
		want     bool
	}{
		{"own shared lock", "always_succeeds", ActorDatum(id), 0, true},
		{"another's shared lock", "always_succeeds", ActorDatum(8), 0, false},
		{"vesting due", "vesting", lockDatum("vesting", id, me, 100), 100, true},
		{"vesting not yet due", "vesting", lockDatum("vesting", id, me, 100), 99, false},
		{"another's vesting", "vesting", lockDatum("vesting", id, other, 100), 200, false},
		{"own escrow", "escrow", lockDatum("escrow", id, me, 0), 0, true},
		{"another's escrow", "escrow", lockDatum("escrow", id, other, 0), 0, false},
		{"a vesting datum at an escrow", "escrow", lockDatum("vesting", id, me, 0), 0, false},
	}
	for _, tt := range tests {
		d := tt.datum
		if got := claimable(tt.template, &d, id, me, tt.now); got != tt.want {
			t.Errorf("%s: claimable = %v, want %v", tt.name, got, tt.want)
		}
	}
	if claimable("vesting", nil, id, me, 0) {
		t.Error("an output without a datum is claimable")
	}
}

func TestClaimArgs(t *testing.T) {
	t.Parallel()
	me := keys.Credential{1}
	if c := claimArgs("always_succeeds", me, 4, 50); len(c.signers) != 0 || c.from != 0 || !c.redeemer.Data.Equal(UnitDatum().Data) {
		t.Fatalf("shared claim %+v", c)
	}
	c := claimArgs("vesting", me, 4, 50)
	if len(c.signers) != 1 || c.signers[0] != common.Blake2b224(me) || c.from != 50 {
		t.Fatalf("vesting claim %+v", c)
	}
	release, refund := claimArgs("escrow", me, 1, 50), claimArgs("escrow", me, refundEvery, 50)
	if !release.redeemer.Data.Equal(contract.Release.Data()) || !refund.redeemer.Data.Equal(contract.Refund.Data()) || release.from != 0 {
		t.Fatalf("escrow claims %+v %+v", release, refund)
	}
	if release.action != "release" || refund.action != "refund" || c.action != "claim" {
		t.Fatalf("actions %q %q %q", release.action, refund.action, c.action)
	}
}
