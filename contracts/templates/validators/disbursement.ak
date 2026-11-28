//// Disbursement: a treasury award its recipient draws down in milestones
//// under an administrator's oversight.
////
//// A draw needs the beneficiary's and the administrator's signatures and
//// a validity range starting at or after the datum's `next`. It takes at
//// most `tranche`: unless the award is down to its last tranche, an
//// output pays the rest back to this address with the datum's `next`
//// moved on by `period`. One draw spends one award, so two awards cannot
//// share a continuing output. `nonce` only separates deployed instances.

use aiken/collection/list
use aiken/fuzz
use aiken/interval.{Finite}
use cardano/address.{Address, Script, VerificationKey}
use cardano/assets
use cardano/transaction.{
  InlineDatum, Input, NoDatum, Output, OutputReference, Transaction,
}
use continuum/types.{DisbursementDatum}

validator disbursement(_nonce: Int) {
  spend(
    datum: Option<DisbursementDatum>,
    _redeemer: Data,
    own_ref: OutputReference,
    self: Transaction,
  ) {
    expect Some(d) = datum
    expect Some(own) = transaction.find_input(self.inputs, own_ref)
    let held = assets.lovelace_of(own.output.value)
    let rest = DisbursementDatum { ..d, next: d.next + d.period }
    and {
      list.has(self.extra_signatories, d.beneficiary),
      list.has(self.extra_signatories, d.administrator),
      when self.validity_range.lower_bound.bound_type is {
        Finite(start) -> start >= d.next
        _ -> False
      },
      list.count(self.inputs, fn(i) { i.output.address == own.output.address }) == 1,
      or {
        held <= d.tranche,
        list.any(
          self.outputs,
          fn(o) { continues(o, own.output.address, held - d.tranche, rest) },
        ),
      },
    }
  }

  else(_) {
    fail
  }
}

fn continues(o: Output, at: Address, at_least: Int, datum: DisbursementDatum) {
  let expected: Data = datum
  and {
    o.address == at,
    assets.lovelace_of(o.value) >= at_least,
    o.datum == InlineDatum(expected),
  }
}

const beneficiary = #"01"

const administrator = #"02"

const script =
  Address { payment_credential: Script(#"aa"), stake_credential: None }

fn award(next: Int) -> DisbursementDatum {
  DisbursementDatum {
    beneficiary,
    administrator,
    next,
    period: 100,
    tranche: 10,
  }
}

fn own_ref() -> OutputReference {
  OutputReference { transaction_id: #"", output_index: 0 }
}

fn locked(lovelace: Int, datum: DisbursementDatum) -> Output {
  let d: Data = datum
  Output {
    address: script,
    value: assets.from_lovelace(lovelace),
    datum: InlineDatum(d),
    reference_script: None,
  }
}

fn to_beneficiary(lovelace: Int) -> Output {
  Output {
    address: Address {
      payment_credential: VerificationKey(beneficiary),
      stake_credential: None,
    },
    value: assets.from_lovelace(lovelace),
    datum: NoDatum,
    reference_script: None,
  }
}

/// A draw from an award of `held` at `next`, valid from `from`, signed by
/// `signers`, paying `outputs`.
fn draw(held: Int, from: Int, signers: List<ByteArray>, outputs: List<Output>) {
  Transaction {
    ..transaction.placeholder,
    inputs: [
      Input { output_reference: own_ref(), output: locked(held, award(50)) },
    ],
    outputs,
    extra_signatories: signers,
    validity_range: interval.after(from),
  }
}

fn spend(tx: Transaction) -> Bool {
  disbursement.spend(0, Some(award(50)), Void, own_ref(), tx)
}

test draw_a_tranche() {
  spend(
    draw(
      30,
      50,
      [beneficiary, administrator],
      [locked(20, award(150)), to_beneficiary(10)],
    ),
  )
}

test draw_the_last_tranche() {
  spend(draw(10, 50, [beneficiary, administrator], [to_beneficiary(10)]))
}

test draw_before_the_milestone() fail {
  spend(draw(30, 49, [beneficiary, administrator], [locked(20, award(150))]))
}

test draw_without_the_administrator() fail {
  spend(draw(30, 50, [beneficiary], [locked(20, award(150))]))
}

test draw_without_moving_the_milestone() fail {
  spend(draw(30, 50, [beneficiary, administrator], [locked(20, award(50))]))
}

// Drawing more than a tranche leaves too little locked, whatever the
// award and the excess.
test prop_overdraw_fails(
  amounts via fuzz.both(
    fuzz.int_between(11, 1_000_000_000),
    fuzz.int_between(1, 10),
  ),
) {
  let (held, excess) = amounts
  let rest = held - 10 - excess
  !spend(
    draw(held, 50, [beneficiary, administrator], [locked(rest, award(150))]),
  )
}

// Any award above one tranche can be drawn by exactly one tranche.
test prop_one_tranche_draws(held via fuzz.int_between(11, 1_000_000_000)) {
  spend(
    draw(
      held,
      50,
      [beneficiary, administrator],
      [locked(held - 10, award(150))],
    ),
  )
}
