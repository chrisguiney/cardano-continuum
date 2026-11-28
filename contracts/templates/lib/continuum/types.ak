//// Datum and redeemer types the templates share with the off-chain
//// builders in internal/contract. The field order is the on-chain
//// encoding: Constr 0 with the fields in declaration order.

use aiken/crypto.{VerificationKeyHash}

/// A vesting position: `owner` may claim from `lock_until` (POSIX ms).
pub type VestingDatum {
  owner: VerificationKeyHash,
  lock_until: Int,
}

/// An escrow: `depositor` releases to `beneficiary`, who may refund.
pub type EscrowDatum {
  depositor: VerificationKeyHash,
  beneficiary: VerificationKeyHash,
}

/// What an escrow spend does: Release is Constr 0, Refund Constr 1.
pub type EscrowAction {
  Release
  Refund
}
