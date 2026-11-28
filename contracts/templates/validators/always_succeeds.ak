//// Always succeeds: a lock anyone may spend with any redeemer.
////
//// The engine's shared contract 0: honest actors lock at it and
//// adversaries race to spend what they lock.

use cardano/transaction.{OutputReference, Transaction}

validator always_succeeds {
  spend(
    _datum: Option<Data>,
    _redeemer: Data,
    _own_ref: OutputReference,
    _self: Transaction,
  ) {
    True
  }

  else(_) {
    fail
  }
}
