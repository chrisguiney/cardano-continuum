//// Vesting: funds locked for an owner until a deadline.
////
//// The owner claims once the transaction's validity range starts at or
//// after `lock_until`, and only with the owner's signature. The `nonce`
//// parameter does nothing on chain; applying a different one gives each
//// deployed instance its own script hash and address.

use aiken/collection/list
use aiken/interval.{Finite}
use cardano/transaction.{OutputReference, Transaction}
use continuum/types.{VestingDatum}

validator vesting(_nonce: Int) {
  spend(
    datum: Option<VestingDatum>,
    _redeemer: Data,
    _own_ref: OutputReference,
    self: Transaction,
  ) {
    expect Some(VestingDatum { owner, lock_until }) = datum
    and {
      list.has(self.extra_signatories, owner),
      when self.validity_range.lower_bound.bound_type is {
        Finite(start) -> start >= lock_until
        _ -> False
      },
    }
  }

  else(_) {
    fail
  }
}

test claim_after_deadline() {
  let datum = VestingDatum { owner: #"00", lock_until: 10 }
  let tx =
    Transaction {
      ..transaction.placeholder,
      extra_signatories: [#"00"],
      validity_range: interval.after(10),
    }
  vesting.spend(0, Some(datum), Void, placeholder_ref(), tx)
}

test claim_before_deadline() fail {
  let datum = VestingDatum { owner: #"00", lock_until: 10 }
  let tx =
    Transaction {
      ..transaction.placeholder,
      extra_signatories: [#"00"],
      validity_range: interval.after(9),
    }
  vesting.spend(0, Some(datum), Void, placeholder_ref(), tx)
}

test claim_without_owner() fail {
  let datum = VestingDatum { owner: #"00", lock_until: 10 }
  let tx =
    Transaction {
      ..transaction.placeholder,
      extra_signatories: [#"01"],
      validity_range: interval.after(10),
    }
  vesting.spend(0, Some(datum), Void, placeholder_ref(), tx)
}

fn placeholder_ref() -> OutputReference {
  OutputReference { transaction_id: #"", output_index: 0 }
}
