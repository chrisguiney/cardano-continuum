//// Escrow: funds a depositor holds for a beneficiary.
////
//// Release: the depositor signs and an output pays the beneficiary's key
//// at least the escrowed lovelace. Refund: the beneficiary signs, giving
//// the deposit up. `nonce` only separates deployed instances, as in
//// vesting.

use aiken/collection/list
use cardano/address.{VerificationKey}
use cardano/assets
use cardano/transaction.{Output, OutputReference, Transaction}
use continuum/types.{EscrowAction, EscrowDatum, Refund, Release}

validator escrow(_nonce: Int) {
  spend(
    datum: Option<EscrowDatum>,
    action: EscrowAction,
    own_ref: OutputReference,
    self: Transaction,
  ) {
    expect Some(EscrowDatum { depositor, beneficiary }) = datum
    when action is {
      Release -> {
        expect Some(own) = transaction.find_input(self.inputs, own_ref)
        let held = assets.lovelace_of(own.output.value)
        and {
          list.has(self.extra_signatories, depositor),
          list.any(self.outputs, fn(o) { pays(o, beneficiary, held) }),
        }
      }
      Refund -> list.has(self.extra_signatories, beneficiary)
    }
  }

  else(_) {
    fail
  }
}

fn pays(o: Output, key: ByteArray, at_least: Int) -> Bool {
  and {
    o.address.payment_credential == VerificationKey(key),
    assets.lovelace_of(o.value) >= at_least,
  }
}

test refund_needs_beneficiary() fail {
  let datum = EscrowDatum { depositor: #"00", beneficiary: #"01" }
  let tx = Transaction { ..transaction.placeholder, extra_signatories: [#"00"] }
  escrow.spend(0, Some(datum), Refund, placeholder_ref(), tx)
}

test refund_by_beneficiary() {
  let datum = EscrowDatum { depositor: #"00", beneficiary: #"01" }
  let tx = Transaction { ..transaction.placeholder, extra_signatories: [#"01"] }
  escrow.spend(0, Some(datum), Refund, placeholder_ref(), tx)
}

fn placeholder_ref() -> OutputReference {
  OutputReference { transaction_id: #"", output_index: 0 }
}
