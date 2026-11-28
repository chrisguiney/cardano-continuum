//// Escrow between two parties: a depositor holds funds for a beneficiary.
////
//// Release: the depositor signs (its approval) and an output pays the
//// beneficiary's key at least the escrowed lovelace. The beneficiary
//// builds and submits the release, with the depositor's signature beside
//// its own. Refund: the depositor signs and the transaction's validity
//// range starts at or after the deadline. `nonce` only separates deployed
//// instances, as in vesting.

use aiken/collection/list
use aiken/fuzz
use aiken/interval.{Finite}
use cardano/address.{Address, VerificationKey}
use cardano/assets
use cardano/transaction.{Input, Output, OutputReference, Transaction}
use continuum/types.{EscrowAction, EscrowDatum, Refund, Release}

validator escrow(_nonce: Int) {
  spend(
    datum: Option<EscrowDatum>,
    action: EscrowAction,
    own_ref: OutputReference,
    self: Transaction,
  ) {
    expect Some(EscrowDatum { depositor, beneficiary, deadline }) = datum
    and {
      list.has(self.extra_signatories, depositor),
      when action is {
        Release -> {
          expect Some(own) = transaction.find_input(self.inputs, own_ref)
          let held = assets.lovelace_of(own.output.value)
          list.any(self.outputs, fn(o) { pays(o, beneficiary, held) })
        }
        Refund ->
          when self.validity_range.lower_bound.bound_type is {
            Finite(start) -> start >= deadline
            _ -> False
          }
      },
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

const depositor = #"00"

const beneficiary = #"01"

fn datum(deadline: Int) -> Option<EscrowDatum> {
  Some(EscrowDatum { depositor, beneficiary, deadline })
}

fn own_ref() -> OutputReference {
  OutputReference { transaction_id: #"", output_index: 0 }
}

fn output(key: ByteArray, lovelace: Int) -> Output {
  Output {
    address: Address {
      payment_credential: VerificationKey(key),
      stake_credential: None,
    },
    value: assets.from_lovelace(lovelace),
    datum: transaction.NoDatum,
    reference_script: None,
  }
}

/// A release of `held` paying `paid` to `to`, signed by `signers`.
fn release(held: Int, to: ByteArray, paid: Int, signers: List<ByteArray>) {
  Transaction {
    ..transaction.placeholder,
    inputs: [Input { output_reference: own_ref(), output: output(#"ff", held) }],
    outputs: [output(to, paid)],
    extra_signatories: signers,
  }
}

/// A refund signed by `signers`, valid from `from`.
fn refund(from: Int, signers: List<ByteArray>) {
  Transaction {
    ..transaction.placeholder,
    extra_signatories: signers,
    validity_range: interval.after(from),
  }
}

test release_signed_by_both() {
  let tx = release(5_000_000, beneficiary, 5_000_000, [beneficiary, depositor])
  escrow.spend(0, datum(10), Release, own_ref(), tx)
}

test release_without_depositor() fail {
  let tx = release(5_000_000, beneficiary, 5_000_000, [beneficiary])
  escrow.spend(0, datum(10), Release, own_ref(), tx)
}

test release_to_someone_else() fail {
  let tx = release(5_000_000, #"02", 5_000_000, [beneficiary, depositor])
  escrow.spend(0, datum(10), Release, own_ref(), tx)
}

test refund_at_deadline() {
  escrow.spend(0, datum(10), Refund, own_ref(), refund(10, [depositor]))
}

test refund_before_deadline() fail {
  escrow.spend(0, datum(10), Refund, own_ref(), refund(9, [depositor]))
}

test refund_by_beneficiary() fail {
  escrow.spend(0, datum(10), Refund, own_ref(), refund(10, [beneficiary]))
}

// Paying the beneficiary less than the escrow holds never releases it.
test prop_underpaid_release_fails(
  amounts via fuzz.both(
    fuzz.int_between(1, 1_000_000_000),
    fuzz.int_at_least(1),
  ),
) {
  let (paid, short) = amounts
  let tx = release(paid + short, beneficiary, paid, [beneficiary, depositor])
  !escrow.spend(0, datum(10), Release, own_ref(), tx)
}

// A refund opens exactly at the deadline, whatever the deadline is.
test prop_refund_opens_at_deadline(
  times via fuzz.both(
    fuzz.int_between(0, 1_000_000_000),
    fuzz.int_between(-1000, 1000),
  ),
) {
  let (deadline, offset) = times
  let ok = escrow.spend(
    0,
    datum(deadline),
    Refund,
    own_ref(),
    refund(deadline + offset, [depositor]),
  )
  ok == ( offset >= 0 )
}
