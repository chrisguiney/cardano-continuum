package intent

import (
	"bytes"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// How a script_call drives each contract template (build.Template). An
// honest actor locks with a datum naming itself and later claims only
// what is its own and, for vesting, due:
//
//	always_succeeds  datum ActorDatum; claim with a unit redeemer
//	vesting          datum (owner, lock_until); claim signed by the owner,
//	                 valid from a slot at or after lock_until
//	escrow           datum (depositor, beneficiary), both the actor; release
//	                 signed by the depositor, paid back to it in the change,
//	                 or refund signed by the beneficiary
//
// The actor is both escrow parties: one wallet signs each transaction, so
// either action passes for its own deposit.

// vestDelay is how many slots after its lock a vesting position opens.
const vestDelay = 60

// refundEvery makes every refundEvery-th escrow claim (by intent id) a
// refund and the rest releases, so both handlers run.
const refundEvery = 4

// lockDatum is the datum a lock at a contract of template carries for
// actor id, whose payment key hash is owner; until is a vesting
// position's opening time in POSIX ms.
func lockDatum(template string, id population.ActorID, owner keys.Credential, until int64) common.Datum {
	switch template {
	case "vesting":
		return common.Datum{Data: contract.VestingDatum{Owner: owner[:], LockUntil: until}.Data()}
	case "escrow":
		return common.Datum{Data: contract.EscrowDatum{Depositor: owner[:], Beneficiary: owner[:]}.Data()}
	}
	return ActorDatum(id)
}

// claimable reports whether actor id (payment key hash owner) may claim
// an output with datum d at a contract of template at POSIX ms now.
func claimable(template string, d *common.Datum, id population.ActorID, owner keys.Credential, now int64) bool {
	if d == nil {
		return false
	}

	switch template {
	case "vesting":
		v, ok := contract.ParseVestingDatum(d.Data)
		return ok && bytes.Equal(v.Owner, owner[:]) && v.LockUntil <= now
	case "escrow":
		e, ok := contract.ParseEscrowDatum(d.Data)
		return ok && bytes.Equal(e.Depositor, owner[:])
	}
	return bytes.Equal(DatumKey(*d), DatumKey(ActorDatum(id)))
}

// claim is what an unlock of a template's output needs beside the output.
type claim struct {
	// redeemer is the validator's argument.
	redeemer common.Datum
	// signers are the required signers the validator checks.
	signers []common.Blake2b224
	// from is the validity start slot, zero for none.
	from uint64
	// action names what the claim does, for the log.
	action string
}

// claimArgs is the claim of intent intent by the actor whose payment key
// hash is owner, at tip slot tip.
func claimArgs(template string, owner keys.Credential, intent uint64, tip uint64) claim {
	me := []common.Blake2b224{common.Blake2b224(owner)}
	switch template {
	case "vesting":
		return claim{redeemer: UnitDatum(), signers: me, from: tip, action: "claim"}
	case "escrow":
		act := contract.Release
		if intent%refundEvery == 0 {
			act = contract.Refund
		}
		return claim{redeemer: common.Datum{Data: act.Data()}, signers: me, action: act.String()}
	}
	return claim{redeemer: UnitDatum(), action: "claim"}
}
