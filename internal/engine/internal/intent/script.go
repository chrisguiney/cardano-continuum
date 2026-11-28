package intent

import (
	"bytes"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// How a script_call drives each contract template (build.Template). An
// honest actor locks with a datum naming itself and later calls only
// what its role allows:
//
//	always_succeeds  datum ActorDatum; the locker claims with a unit redeemer
//	vesting          datum (owner, lock_until); the owner claims, valid
//	                 from a slot at or after lock_until
//	escrow           datum (depositor, beneficiary, deadline); the depositor
//	                 is the locker and the beneficiary another script user.
//	                 The beneficiary releases it to itself with the
//	                 depositor's signature beside its own; the depositor
//	                 refunds it from the deadline on
//
//	disbursement     datum (beneficiary, administrator, next, period,
//	                 tranche); a treasury recipient's award, which the
//	                 recipient locks as it withdraws it (callAward) and
//	                 draws a tranche of from each milestone on, with the
//	                 governance administrator's signature beside its own
//
// A depositor approves most escrows (approved): the rest are disputes,
// which the beneficiary leaves alone and the depositor refunds.

// What a script call does, as Args.Call records it.
const (
	callLock    = "lock"
	callClaim   = "claim"
	callRelease = "release"
	callRefund  = "refund"
	callAward   = "award"
	callDraw    = "draw"
)

// vestDelay is how many slots after its lock a vesting position opens;
// escrowWindow how many after its lock an escrow may be refunded.
const (
	vestDelay    = 60
	escrowWindow = 600
)

// milestones is how many tranches an award is drawn in, milestoneEvery
// the slots between milestones, and minTranche the smallest tranche worth
// locking: a smaller award is taken at once.
const (
	milestones     = 4
	milestoneEvery = 600
	minTranche     = 5_000_000
)

// disputeEvery makes one escrow in disputeEvery a dispute.
const disputeEvery = 4

// lockDatum is the datum a lock at a contract of template carries for
// actor id, whose payment key hash is owner, with counterparty the
// escrow's beneficiary; until is a vesting position's opening time or an
// escrow's deadline, in POSIX ms.
func lockDatum(template string, id population.ActorID, owner, counterparty keys.Credential, until int64) common.Datum {
	switch template {
	case "vesting":
		return common.Datum{Data: contract.VestingDatum{Owner: owner[:], LockUntil: until}.Data()}
	case "escrow":
		return common.Datum{Data: contract.EscrowDatum{Depositor: owner[:], Beneficiary: counterparty[:], Deadline: until}.Data()}
	}
	return ActorDatum(id)
}

// approved reports whether an escrow's depositor approves its release.
// The locked output's transaction hash is uniform, so the share of
// disputes is 1/disputeEvery, the same for every party.
func approved(ref wire.Ref) bool { return ref.Tx[0]%disputeEvery != 0 }

// callFor is what actor id (payment key hash owner) may do at POSIX ms
// now with an output with datum d at a contract of template; empty for
// nothing. approved is the escrow depositor's decision on the output.
func callFor(template string, d *common.Datum, id population.ActorID, owner keys.Credential, now int64, approved bool) string {
	if d == nil {
		return ""
	}

	switch template {
	case "vesting":
		v, ok := contract.ParseVestingDatum(d.Data)
		if ok && bytes.Equal(v.Owner, owner[:]) && v.LockUntil <= now {
			return callClaim
		}
		return ""
	case "escrow":
		return escrowCall(d, owner, now, approved)
	case "disbursement":
		a, ok := contract.ParseDisbursementDatum(d.Data)
		if ok && bytes.Equal(a.Beneficiary, owner[:]) && a.Next <= now {
			return callDraw
		}
		return ""
	}

	if bytes.Equal(DatumKey(*d), DatumKey(ActorDatum(id))) {
		return callClaim
	}
	return ""
}

func escrowCall(d *common.Datum, owner keys.Credential, now int64, approved bool) string {
	e, ok := contract.ParseEscrowDatum(d.Data)
	switch {
	case !ok:
		return ""
	case bytes.Equal(e.Beneficiary, owner[:]) && !bytes.Equal(e.Depositor, owner[:]) && approved:
		return callRelease
	case bytes.Equal(e.Depositor, owner[:]) && e.Deadline <= now:
		return callRefund
	}
	return ""
}

// claim is what an unlock of a template's output needs beside the output.
type claim struct {
	// redeemer is the validator's argument.
	redeemer common.Datum
	// signers are the required signers the validator checks that the
	// wallet signs for.
	signers []common.Blake2b224
	// from is the validity start slot, zero for none.
	from uint64
}

// claimArgs is the unlock that makes call on a template's output by the
// actor whose payment key hash is owner, at tip slot tip. A release's
// depositor signature is the builder's co-signer, not the wallet's.
func claimArgs(template, call string, owner keys.Credential, tip uint64) claim {
	me := []common.Blake2b224{common.Blake2b224(owner)}
	switch {
	case template == "vesting":
		return claim{redeemer: UnitDatum(), signers: me, from: tip}
	case call == callRelease:
		return claim{redeemer: common.Datum{Data: contract.Release.Data()}}
	case call == callRefund:
		return claim{redeemer: common.Datum{Data: contract.Refund.Data()}, signers: me, from: tip}
	case call == callDraw:
		return claim{redeemer: UnitDatum(), signers: me, from: tip}
	}
	return claim{redeemer: UnitDatum()}
}

// drawRest is what a draw of the award a, holding held lovelace, pays
// back to the contract and the datum it carries: the award less a tranche
// with the milestone moved on, or nothing for the last tranche.
func drawRest(a contract.DisbursementDatum, held uint64) (uint64, common.Datum) {
	if held <= uint64(a.Tranche) {
		return 0, common.Datum{}
	}
	a.Next += a.Period
	return held - uint64(a.Tranche), common.Datum{Data: a.Data()}
}
