# fiat-escrow

A human's fiat lives off chain, in integer cents, beside the buy orders that
spend it. An order is placed when the human decides to buy, but its ADA only
settles K deep later, and in between the human's bills come due, more buys
are placed, orders are released or given up, and the engine may restart from
a snapshot. `FiatEscrow.tla` decided, before the money code was written,
where an open order's hold lives so that fiat never goes below what is held,
every cent is accounted for, and a released order gives its hold back.

Run it with `make models` (all models) or
`models/scripts/run-model.sh specs/fiat-escrow`.

Models cardano-continuum at `433d1fd`. The model was written first, at
`881fdee`, and the money code implemented to it.

## The result in one paragraph

Placing an order **holds** its cents, and the hold is the open order's own
record (`wire.Order.Cents`), which the snapshot's pay section already holds:
available fiat is fiat less the holds of the open orders, and every expense
and new order reads only the available amount. Settling spends the hold;
releasing or giving up drops the record and the hold with it. Each simpler
rule breaks an invariant, as the [configs](#configs) show.

## What is modelled

- **One human** with a fiat balance, one payday and one bill.
- **Orders**: `Orders` buy orders of `Cents` each, placed, then settled or
  released.
- **Restart**: the engine restored from a snapshot; fiat and the order
  records come back.

## What is not modelled

- The ADA side of an order (specs/submit-retry and
  specs/planner-reservations), FX, partial fills, several humans, sales
  (a settled sale only adds fiat).
- No rule of the Agda formal ledger specification applies: fiat is off
  chain, and no ledger rule governs it.

## Code map

| Action | Code | What it stands for |
| --- | --- | --- |
| `Place` | internal/engine/internal/cohort/money.go `Money.Fund` (a scored buy), internal/engine/intents.go `sizeTrade` (a purchase from income) | an order holds its cents in `Order.Cents` |
| `Settle` | internal/engine/internal/cohort/money.go `Money.Settled`, called from internal/engine/internal/cohort/desk.go `Desk.Settled` | the held cents are spent |
| `Release` | internal/engine/internal/pay/retry.go (release and give up drop the pending record) | the hold goes with the record |
| `Pay`, `Earn` | internal/household/household.go `Month`, applied by internal/engine/internal/cohort/money.go `Money.month` | a payday and the bills, paid from available fiat |
| `Restart` | internal/engine/internal/cohort/money.go `moneyOf`/`purseOf`, internal/engine/internal/cohort/people.go `People.Save`/`Load` | fiat restored; holds come back with the pay section's orders |
| `Available` | internal/engine/internal/cohort/money.go `Money.Held`, `Money.Available` | fiat less the open buys' Cents |

## Drift

| Code | Model | Why | Config |
| --- | --- | --- | --- |
| a purchase's hold is the cost of what the venue can fill, at most the tranche | one fixed `Cents` | sizing does not change where the hold lives | - |
| a bill's unpaid rest forces a sale of ADA | the rest is short, nothing more | the sale is a new order, not a ledger rule | - |
| amounts are per currency | one currency | the rules are per human | - |

## Invariants and properties

| Invariant | Says | Broken by |
| --- | --- | --- |
| `TypeOK` | every variable has its shape | - |
| `AvailableNonNegative` | fiat never goes below what open orders hold | `FiatEscrowDebitAtSettleBug` |
| `Conservation` | fiat is the start plus income, less bills paid and orders settled | `FiatEscrowStoredHoldBug` |
| `HoldsMatchOpenOrders` | what the ledger holds is exactly the open orders' cents | `FiatEscrowReleaseLeakBug` |
| `Ends` (liveness, `FairSpec`) | an open order ends, traded or released | - |

## Configs

| Config | Mutation | Expected |
| --- | --- | --- |
| `FiatEscrow` | none | all invariants hold |
| `FiatEscrowLive` | none | `Ends` holds |
| `FiatEscrowDebitAtSettleBug` | orders hold nothing and debit at settlement | fails `AvailableNonNegative`: two orders are placed against fiat that covers one |
| `FiatEscrowStoredHoldBug` | the hold is a field of its own, not restored, and settlement spends what it holds | fails `Conservation`: after a restart the order settles without spending its fiat |
| `FiatEscrowReleaseLeakBug` | a release keeps the hold | fails `HoldsMatchOpenOrders` |

`verdicts.tsv` has the state counts. Covered elsewhere: the cents rounding
(`household.Hold` rounds up, `household.Proceeds` down) by
`household.TestCentsRounding`; `Money.Fund`'s shrinking by
`cohort.TestFundBoundsABuyByFiat`; whole-run conservation by
`engine.TestDeterminismHumans` and the fake summary's fiat check. Gap: a
human leaving with open orders (they settle into its fiat; not modelled).
