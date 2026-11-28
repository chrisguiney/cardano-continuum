//// Datum and redeemer types the templates share with the off-chain
//// builders in internal/contract. The field order is the on-chain
//// encoding: Constr 0 with the fields in declaration order.

use aiken/crypto.{VerificationKeyHash}

/// A vesting position: `owner` may claim from `lock_until` (POSIX ms).
pub type VestingDatum {
  owner: VerificationKeyHash,
  lock_until: Int,
}

/// An escrow between two parties: `depositor` pays `beneficiary` once it
/// approves (Release), or takes the deposit back from `deadline` (POSIX
/// ms) on (Refund).
pub type EscrowDatum {
  depositor: VerificationKeyHash,
  beneficiary: VerificationKeyHash,
  deadline: Int,
}

/// What an escrow spend does: Release is Constr 0, Refund Constr 1.
pub type EscrowAction {
  Release
  Refund
}

/// A treasury award drawn down in milestones: from `next` (POSIX ms) the
/// beneficiary draws `tranche` lovelace with the administrator's
/// signature, and the rest stays locked until `next + period`.
pub type DisbursementDatum {
  beneficiary: VerificationKeyHash,
  administrator: VerificationKeyHash,
  next: Int,
  period: Int,
  tranche: Int,
}

/// A constant-product market (a liquidity pool) of ADA and one token: `owner` provides and
/// retires its liquidity; anyone trades against `ada` lovelace and
/// `tokens` of (`policy`, `name`), which the pool output holds.
pub type MarketDatum {
  owner: VerificationKeyHash,
  policy: ByteArray,
  name: ByteArray,
  ada: Int,
  tokens: Int,
}

/// What a market spend does: Trade is Constr 0, Provide 1, Retire 2.
pub type MarketAction {
  Trade
  Provide
  Retire
}
