//// Market: a constant-product market of ADA and one token, owned by a
//// liquidity provider.
////
//// The pool is one output whose value holds at least the datum's `ada`
//// and exactly its `tokens`. Trade: anyone swaps; the one output paid
//// back here holds the new reserves its datum names, the owner and token
//// unchanged, and the reserves' product does not fall (the trader's 0.3%
//// fee makes it grow). Provide: the owner adds to both reserves. Retire:
//// the owner takes what it likes, closing the pool. One pool is spent at
//// a time, so two cannot share a continuing output. `nonce` only
//// separates deployed instances.

use aiken/collection/list
use aiken/fuzz
use cardano/address.{Address, Script}
use cardano/assets
use cardano/transaction.{
  InlineDatum, Input, Output, OutputReference, Transaction,
}
use continuum/types.{MarketAction, MarketDatum, Provide, Retire, Trade}

validator market(_nonce: Int) {
  spend(
    datum: Option<MarketDatum>,
    action: MarketAction,
    own_ref: OutputReference,
    self: Transaction,
  ) {
    expect Some(d) = datum
    expect Some(own) = transaction.find_input(self.inputs, own_ref)
    let at = own.output.address
    and {
      holds(own.output, d),
      list.count(self.inputs, fn(i) { i.output.address == at }) == 1,
      when action is {
        Trade -> {
          expect [next] = continuing(self.outputs, at, d)
          next.ada * next.tokens >= d.ada * d.tokens
        }
        Provide -> {
          expect [next] = continuing(self.outputs, at, d)
          and {
            list.has(self.extra_signatories, d.owner),
            next.ada >= d.ada,
            next.tokens >= d.tokens,
          }
        }
        Retire -> list.has(self.extra_signatories, d.owner)
      },
    }
  }

  else(_) {
    fail
  }
}

/// holds reports that o holds the reserves d names.
fn holds(o: Output, d: MarketDatum) -> Bool {
  and {
    assets.lovelace_of(o.value) >= d.ada,
    assets.quantity_of(o.value, d.policy, d.name) == d.tokens,
  }
}

/// continuing is the datum of each output paid back to `at` that keeps
/// d's owner and token and holds the reserves it names. Any output to `at`
/// that does not is a failure: a trade must leave exactly one pool.
fn continuing(
  outputs: List<Output>,
  at: Address,
  d: MarketDatum,
) -> List<MarketDatum> {
  list.filter_map(
    outputs,
    fn(o) {
      if o.address == at {
        expect InlineDatum(raw) = o.datum
        expect next: MarketDatum = raw
        expect and {
            next.owner == d.owner,
            next.policy == d.policy,
            next.name == d.name,
            holds(o, next),
          }
        Some(next)
      } else {
        None
      }
    },
  )
}

const owner = #"01"

const token = #"cc"

const script =
  Address { payment_credential: Script(#"aa"), stake_credential: None }

fn reserves(ada: Int, tokens: Int) -> MarketDatum {
  MarketDatum { owner, policy: token, name: #"", ada, tokens }
}

fn own_ref() -> OutputReference {
  OutputReference { transaction_id: #"", output_index: 0 }
}

fn at_pool(d: MarketDatum, ada: Int, tokens: Int) -> Output {
  let raw: Data = d
  Output {
    address: script,
    value: assets.from_lovelace(ada) |> assets.add(token, #"", tokens),
    datum: InlineDatum(raw),
    reference_script: None,
  }
}

/// A spend of the pool holding (ada, tokens) paying `outputs`, signed by
/// `signers`.
fn spend(
  action: MarketAction,
  outputs: List<Output>,
  signers: List<ByteArray>,
) -> Bool {
  let d = reserves(1_000, 1_000)
  let tx = Transaction {
    ..transaction.placeholder,
    inputs: [
      Input { output_reference: own_ref(), output: at_pool(d, 1_000, 1_000) },
    ],
    outputs,
    extra_signatories: signers,
  }
  market.spend(0, Some(d), action, own_ref(), tx)
}

fn next(ada: Int, tokens: Int) -> Output {
  at_pool(reserves(ada, tokens), ada, tokens)
}

test trade_ada_for_tokens() {
  // 100 ADA in, 90 tokens out: 1100 * 910 >= 1000 * 1000.
  spend(Trade, [next(1_100, 910)], [])
}

test trade_that_drains_the_product() fail {
  spend(Trade, [next(1_100, 900)], [])
}

test trade_leaving_two_pools() fail {
  spend(Trade, [next(1_100, 910), next(10, 0)], [])
}

test trade_with_a_datum_the_value_does_not_hold() fail {
  spend(Trade, [at_pool(reserves(1_100, 910), 1_100, 900)], [])
}

test provide_by_the_owner() {
  spend(Provide, [next(1_500, 1_500)], [owner])
}

test provide_by_someone_else() fail {
  spend(Provide, [next(1_500, 1_500)], [#"02"])
}

test retire_by_the_owner() {
  spend(Retire, [], [owner])
}

test retire_by_someone_else() fail {
  spend(Retire, [], [#"02"])
}

// Whatever comes in, the best out amount (the floor of y * dx / (x + dx))
// trades, and one token more lowers the product and fails.
test prop_trades_keep_the_product(dx via fuzz.int_between(1, 1_000_000)) {
  let x = 1_000
  let y = 1_000
  let best = y * dx / ( x + dx )
  and {
    spend(Trade, [next(x + dx, y - best)], []),
    !spend(Trade, [next(x + dx, y - best - 1)], []),
  }
}
