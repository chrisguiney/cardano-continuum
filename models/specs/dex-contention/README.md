# dex-contention

Script users trade at one constant-product market (the `market` Aiken
template, `contracts/templates/validators/market.ak`): every trade spends the
one market output and pays the next one back. Trades built against the same
output race; one lands, and the others are refused because their market input
is gone. `DexContention.tla` decided, before the engine traded, what a loser
must do so that it never pays twice, never forfeits collateral, never leaks
its reservation, and is never reported by the oracle for losing a fair race.

Run it with `make models` (all models) or
`models/scripts/run-model.sh specs/dex-contention`.

Models cardano-continuum at `PIN`.

## The result in one paragraph

A loser hears `BadInputsUTxO` (a phase-1 failure: no script runs, no
collateral is taken) and builds again, up to its patience. Every attempt reads
the market's output **and its datum** from the view at build time, so the next
attempt's new reserves are computed from the datum it spends; and every attempt
spends the **intent's one reservation**, so at most one attempt of an intent
can land, even when a rollback brings a lost race back. Giving up releases the
reservation. The oracle expects "inputs spent" from a market spend's **first**
send, not only from resends: other actors race for the market. Each simpler
rule breaks an invariant, as the [configs](#configs) show.

## What is modelled

- **The market**: one output, named by its version (the number of trades on
  chain). A version stands for a datum; reserves and amounts are not modelled.
- **Swappers**: `Actors`, each with one trade intent and `Patience` attempts.
- **The chain**: trades in order, one per block; any attempt ever sent may be
  included while its market version is current and its coin unspent.
- **The pool validator**: a trade whose datum is not the version it spends
  fails phase 2 (collateral forfeited).
- **The engine's view**, which lags the chain until `Sync`.
- **Rollbacks** of the last block, up to `MaxRollbacks`; `Bury` stands for the
  chain growing K deep past the trades.
- **The oracle's expected set** for a refusal.

## What is not modelled

- Amounts, fees, slippage and the reserves; several markets; the owner's
  provisions (a provision is a trade here); front-running adversaries.
- Settlement depth: a trade is final once `Bury` has run.
- The planner's write-off rule for attempts that may still land
  (specs/planner-reservations covers it): here giving up waits until no
  rollback remains.
- Node mempools: an attempt is sent to "a node"; the retry machine's
  answers and resends are specs/submit-retry's.

## Code map

| Action | Code | What it stands for |
| --- | --- | --- |
| `Build` | internal/engine/internal/intent/spec.go:419 `buildTrade`, :405 `liveMarket`; internal/engine/internal/intent/make.go:100 `Maker.Attempt` | reads the market output and datum from the view each attempt; spends the intent's reserved coins |
| `Include` | internal/fakechain (UTXO rule, then the script), contracts/templates/validators/market.ak:22 | a valid trade lands; the validator checks the next datum against the one spent |
| `Reject` | internal/engine/internal/pay/retry.go:123 `Retry.Answer`; internal/engine/internal/judge/judge.go:178 (`Contended`) | a refusal with BadInputs; the oracle's expected set |
| `Sync` | internal/chain/view.go:357 `View.Apply`, :426 `View.Rollback` | the view catches up |
| `Settle` | internal/engine/engine.go:661 (`Reclaim(p.Held)`) | the market output spent leaves the foreign count |
| `GiveUp` | internal/engine/internal/pay/retry.go:371 `Retry.rebuild` (patience in submit.Machine) | the actor stops; the reservation goes |
| choice of call | internal/engine/internal/intent/choose.go:270 `Chooser.prepareMarket` | who trades, buys or sells, opens or provides |

## Drift

| Code | Model | Why | Config |
| --- | --- | --- | --- |
| a refusal names BadInputsUTxO, ExtraRedeemers and MissingScriptWitnesses (the spent input's redeemer and witness point at nothing) | one class, BadInputs | the extra classes follow from the same fact; the kind's `again` classes carry them | - |
| `Held` is the market output of the attempt built last; if an earlier attempt landed after a rollback the reclaimed amount is the later one's | the market is a version | an earlier attempt lands only after a rollback past a rebuild, which `Bury` excludes here | - |
| the retry machine may also resend the same bytes | an attempt is sent once | resends are specs/submit-retry's | - |

## Invariants and properties

| Invariant | Says | Broken by |
| --- | --- | --- |
| `TypeOK` | every variable has its shape | - |
| `PaidOnce` | an intent's trade lands at most once | `DexFreshCoinsBug` |
| `CollateralSafe` | no trade on chain failed the market validator | `DexStaleDatumBug` |
| `NoLeak` | an ended intent holds no reservation | `DexKeepReservationBug` |
| `OracleSound` | every refusal of a market spend was in the expected set | `DexNarrowExpectBug` |
| `Bound` | the chain holds at most actors times patience trades | - |
| `Ends` (liveness, `FairSpec`) | every intent ends, by a trade or by giving up | - |

## Configs

| Config | Mutation | Expected |
| --- | --- | --- |
| `Dex` | none | all invariants hold |
| `DexLive` | none | `Ends` holds |
| `DexStaleDatumBug` | a rebuild keeps its first attempt's datum | fails `CollateralSafe` |
| `DexFreshCoinsBug` | each attempt spends fresh coins | fails `PaidOnce`: the loser rebuilds on the winner's market, the winner is rolled back, and both attempts land |
| `DexKeepReservationBug` | giving up keeps the reservation | fails `NoLeak` |
| `DexNarrowExpectBug` | the oracle expects BadInputs only from a rebuild | fails `OracleSound` |

`verdicts.tsv` has the state counts. Every function of the market path is
covered as follows: `buildTrade` and `liveMarket` by `Build` (rules 1 and 2),
`Fill` by the Aiken property tests and `build.TestFillKeepsTheProduct`,
`Chooser.prepareMarket` is a gap (which actor trades is not modelled), and the
validator's Provide and Retire branches by Aiken unit tests only.
