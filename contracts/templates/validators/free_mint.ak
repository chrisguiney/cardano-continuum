//// Free mint: a minting policy anyone may mint or burn under.

use cardano/assets.{PolicyId}
use cardano/transaction.{Transaction}

validator free_mint {
  mint(_redeemer: Data, _policy_id: PolicyId, _self: Transaction) {
    True
  }

  else(_) {
    fail
  }
}
