# planner-reservations

The planner decides which outputs a transaction may spend. It reserves every
output an unfinished transaction spends, and it chains: a transaction may
spend the change of one that is not confirmed yet. It hears about the chain
late and about the mempool only by absence. `PlannerReservations.tla` decided
the reservation store's rules before the planner code existed, and the
[code map](#code-map) pins the commit it now models. TLC tries every ordering of builds, submissions, blocks, rollbacks, drop notices
and restarts at a small scale.

Run it with `make models` (all models) or
`models/scripts/run-model.sh specs/planner-reservations`.

## The result in one paragraph

A reservation is released only when its transaction is **settled** (K blocks
deep in the planner's view) or **doomed** (provably unable to land). A drop
notice releases nothing. A second attempt at a payment must be **unable to
land together with** every unfinished earlier attempt. A record is on disk
before its transaction is handed to a node, and a restart resumes the view
no earlier than the settled point. Every simpler rule tried breaks an
invariant; the table under [Configs](#configs) lists them.

## What is modelled

- **Outputs**: two genesis outputs (`101`, `102`) and one change output per
  planner transaction, named after the transaction.
- **Transactions and intents**: three or four transactions over two
  payments ("intents"). Several transactions may attempt one intent.
- **The node**: a chain with one transaction per block; any transaction that
  ever reached a node may be included at any later time, in the local
  mempool or not, as long as its inputs are unspent and the block is within
  its time-to-live. Rolled-back transactions may be included again or never.
- **Rollbacks** of up to K blocks, never into the part of the chain that was
  once K deep.
- **Somebody else** spending a genesis output (contention at a shared
  script output).
- **The planner's view**: the chain, behind by a queue of roll-forward and
  roll-backward events.
- **Drop notices**: the planner sees that a transaction it sent is neither
  in the mempool nor in its view. That is what an eviction looks like, and
  also what a block in flight looks like.
- **Submission errors**: a refusal before the transaction reached any node.
- **Restarts**: records on disk survive; the view resumes from the settled
  point and chain-sync replays from there.
- **Time-to-live**, with block positions standing in for slots.

## What is not modelled

- Values, fees, minimum outputs, balancing, collateral, tokens. A
  transaction has inputs and one change output.
- Slots. A block's position is its slot, so the Go rule "the K-deep block's
  slot is past the TTL" appears as "the settled length has reached the TTL".
- Several transactions per block, addresses and owners (any intent may be
  offered any output), chain depth limits, the collateral pool.
- That an intent eventually settles: the node need never include anything.
  `Finishes` (in `PlannerLive`) proves the planner's half: a transaction
  that can never land is eventually written off, and one in the immovable
  chain is eventually settled. `NoLeak` is the state form of the first.
- Rollbacks deeper than K. The chain follower must treat one as an error.
- Torn writes. A restart loses whole records, never part of one.
- A rule against building on two attempts of which only one can land. The
  model has it (`Coherent`), but the invariants hold without it at the
  `Planner`, `PlannerRestart`, `PlannerEagerRebuild` and `PlannerK2` bounds;
  it only stops stillborn transactions.

## Invariants

| Invariant | Says | Kind |
|---|---|---|
| `TypeOK` | every variable holds the kind of value it should | sanity |
| `SettledViewIsChain` | the settled part of the view equals the immovable start of the node's chain | sanity; the rest stands on it |
| `ReservedWhileUnfinished` | every input of an unfinished transaction is reserved for its intent: no double reservation | store |
| `NoLeak` | every reservation is held for an unfinished transaction that is neither settled nor doomed | store |
| `InputsTracked` | an unfinished transaction builds only on unfinished transactions or settled ones in the view: no spend of a rolled-back output | store |
| `DeadIsDead` | a transaction the planner wrote off can truly never land: a drop is only a hint | truth |
| `SettledIsFinal` | a transaction the planner calls settled is in the immovable part of the chain | truth |
| `NoSelfConflict` | when two intents' transactions share an input, the earlier one could no longer land when the later was built: the planner never races itself | truth |
| `IntentAtMostOnce` | the chain never holds two attempts of one intent: no payment made twice | truth |
| `Finishes` | with the planner's event processing fair, an unfinished transaction the truth has decided (it can never land, or it is in the immovable chain) is eventually written off or settled | liveness |

"Store" invariants read only what the planner knows. "Truth" invariants
compare the planner's records with the node's chain, which Go cannot see;
they are what the oracle would otherwise blame on the node.

## Configs

Passing configs check all nine invariants, except `PlannerLive`, which
checks `Finishes` alone (safety and liveness are split). A `*Bug.cfg` checks
the one property it names and must fail.

TLC's deadlock check is on. A run ends in `Done`: the planner has processed
every event and every transaction it still counts on could still land, so
it waits for the node. Any other state with no step is a stuck planner.
The two liveness Bug configs turn the check off, with the reason in the
config: the stuck state they reach is also a deadlock, and the liveness
property is what they give teeth to.

| Config | Changes | Expected |
|---|---|---|
| `Planner` | nothing: base bounds | pass |
| `PlannerRestart` | one restart at any point | pass |
| `PlannerEagerRebuild` | a second attempt needs no drop notice | pass |
| `PlannerK2` | K = 2, chain of four, rollbacks of one or two blocks | pass |
| `PlannerFourTx` | four transactions of one input each | pass |
| `PlannerExpireAtTipFixed` | `PlannerExpireAtTipBug`'s time-to-live of one block, the rule unchanged | pass |
| `PlannerLive` | the base bounds, `LiveSpec` | `Finishes` holds |
| `PlannerFreshInputsBug` | a second attempt may use any free inputs | `IntentAtMostOnce` |
| `PlannerConflictWithLatestOnlyBug` | a new attempt conflicts with the newest attempt only | `IntentAtMostOnce` |
| `PlannerReleaseOnDropBug` | a drop notice writes the attempt off and releases its inputs | `NoSelfConflict` |
| `PlannerReleaseOnDropPaysTwiceBug` | the same | `IntentAtMostOnce` |
| `PlannerNoCascadeBug` | a doomed transaction's descendants are left alone | `NoLeak` |
| `PlannerSettleAtTipBug` | finished at first sight in the view, not K deep | `InputsTracked` |
| `PlannerSettleAtTipSelfConflictBug` | the same | `NoSelfConflict` |
| `PlannerExpireAtTipBug` | time-to-live judged at the view's tip, not the settled tip | `NoSelfConflict` |
| `PlannerDeadOnUnsettledConflictBug` | written off when an input is seen spent at any depth | `NoSelfConflict` |
| `PlannerReleaseOnSubmitErrorBug` | any submission error releases, even if the transaction reached a node | `NoSelfConflict` |
| `PlannerPersistLateBug` | handed to a node before the record is on disk, then a restart | `IntentAtMostOnce` |
| `PlannerRestartFromOriginBug` | the view is rebuilt from the first block after a restart | `NoSelfConflict` |
| `PlannerSettledPointFollowsTipBug` | the settled point moves back on a rollback, then a restart | `NoSelfConflict` |
| `PlannerSettleAtTipFinalBug` | finished at first sight in the view, not K deep | `SettledIsFinal` |
| `PlannerReleaseOnDropDeadBug` | a drop notice writes the attempt off | `DeadIsDead` |
| `PlannerPartialReserveBug` | a transaction reserves only the first of its inputs | `ReservedWhileUnfinished` |
| `PlannerSettleOneShallowBug` | the settled point is K - 1 behind the view's tip | `SettledViewIsChain` |
| `PlannerNoCascadeLiveBug` | as `PlannerNoCascadeBug`, `LiveSpec` | `Finishes` |
| `PlannerProbe*Bug` (nine) | nothing; each denies a state that must be reachable | its `ProbeNo*` |

The probes show the passing configs are not passing for want of states: a
chain of three unconfirmed transactions (three intents), a drop notice followed by
inclusion, a second attempt settling, the first attempt settling after a
second was sent, a parent rolled back under a child, a second attempt built
on a replaced parent, death by time-to-live, a released output reused, and
a restart with a transaction in flight.

### What each mutation costs, read from its counterexample

- **Fresh inputs.** A block holding the first attempt is in flight; the
  planner sees the transaction gone from the mempool, builds a second
  attempt on the other output, and both land.
- **Conflict with the newest only.** The third attempt spends the first's
  change. It conflicts with the second (through the first), not with the
  first, and lands on top of it.
- **Release on drop.** Same notice; the input goes to the other intent while
  the first transaction is already on the chain.
- **No cascade.** A transaction is refused before reaching a node. Its child
  keeps its reservation for an output that will never exist.
- **Settle at tip.** The block is rolled back; the child spends an output
  the view no longer has, and the parent's inputs go to another intent
  while the parent can be included again.
- **Expire at tip, dead on unsettled conflict.** One outside block makes the
  transaction look finished; a rollback of that block makes it includable
  again, after its input went elsewhere.
- **Persist late, restart from origin, settled point follows tip.** The
  restart leaves the planner with outputs that look free and are not.

## Bounds and state counts

Base bounds: 2 genesis outputs, 3 transactions, 2 intents, K = 1, a
time-to-live of 2 blocks, a chain of up to 3 blocks, at most 2 inputs, and
one each of rollback, outside spend, mempool eviction and failed submission.
K = 1 lets a block settle and a rollback happen inside three blocks;
`PlannerK2` repeats the check with rollbacks of two. Three transactions
give a parent, a child and one second attempt; `PlannerFourTx` gives both
intents a second attempt at the cost of one input per transaction.

TLC 2.19 (tla2tools 1.7.4), 14 workers:

| Config | States generated | Distinct states | Depth | Time |
|---|---|---|---|---|
| `Planner` | 1,284,313 | 536,626 | 21 | 8 s |
| `PlannerRestart` | 2,440,161 | 827,774 | 23 | 12 s |
| `PlannerEagerRebuild` | 454,876 | 180,849 | 17 | 4 s |
| `PlannerK2` | 2,039,623 | 796,678 | 22 | 11 s |
| `PlannerFourTx` | 1,959,625 | 706,894 | 24 | 13 s |
| `PlannerExpireAtTipFixed` | | 343,350 | | 3 s |
| `PlannerLive` (liveness, one worker) | | 536,626 | | 21 s |

`verdicts.tsv` holds the current distinct counts and times; the runner
compares every run with it. Turning TLC's deadlock check on changed no
distinct count (`Done` only adds stuttering).

What the deadlock check found: with a restart, the planner's settled point
can stay below the node's immovable point. A rollback shortens the chain
after a longer one made a block immovable; a restart then replays only the
current chain, so the planner settles that block, and writes off what it
dooms, only once the chain grows by K blocks again (`PlannerRestart` reached
this with the chain at its bound). That is waiting for the node, not a
stuck planner, and `Done` says so (`vfloor < floor`). It also means
`Finishes` holds only for a node that keeps growing its chain;
`PlannerLive` has no restart.

Times are from a quiet machine, where the whole directory checks in about a
minute. With other model checkers and test runs competing for the machine
the same directory took fourteen minutes, which is why the bounds stop here. Each `*Bug.cfg`
stops at its counterexample within seconds.

## The reservation store contract

For `internal/plan`. Names are suggestions; the rules are not.

### Shape

```go
// Reserve records one attempt of an intent and reserves its inputs for the
// intent. It returns only after the record and the reservations are durable.
func (l *Ledger) Reserve(id IntentID, a Attempt) error // Attempt{TxID, Inputs []Ref, Change []Utxo, TTL Slot}
// Available is what Reserve will accept for this intent.
func (l *Ledger) Available(id IntentID, opts Opts) []Utxo
// Advance applies one roll-forward or roll-backward and returns what it
// finished. It is the only place a reservation is released by the chain.
func (l *Ledger) Advance(ev chain.Event) (settled, dead []TxID)
// Refused writes off an attempt that provably never reached a node.
func (l *Ledger) Refused(tx TxID) (dead []TxID)
// DropNotice records that a sent transaction is absent from the mempool.
func (l *Ledger) DropNotice(tx TxID)
// ResumePoint is where chain-sync must intersect after a restart.
func (l *Ledger) ResumePoint() chain.Point
```

There is no `Release(intent, Dropped)`, no `Confirm` at first sight, and
`Rollback` changes no reservation.

### Rules

1. **Reservations belong to intents.** An output is free or held by one
   intent. Every attempt of that intent may spend it; no other intent may.
2. **`Available(i)`** is: outputs unspent in the view, plus the change of
   unfinished attempts the view does not hold yet, each free or held by `i`.
3. **`Reserve` refuses** an input outside `Available(i)`; an intent that has
   a settled attempt; and an attempt that could land together with an
   unfinished attempt of the same intent. Two transactions cannot land
   together when they, or any transactions they build on, share an input.
   Compare against every unfinished attempt, not the newest.
   It also refuses inputs that contradict each other: an attempt built on
   two transactions that cannot land together.
4. **An attempt is unfinished from `Reserve` until it is settled or dead.**
   Being confirmed, rolled back, dropped from the mempool or timed out
   changes nothing.
5. **Settled**: in a block with K blocks after it in the view. The settled
   point is a high-water mark; a rollback never lowers it.
6. **Dead**, any of: an input is spent by another transaction in a settled
   block; the settled tip's slot is past the TTL and the transaction is not
   in a settled block; it was refused and never reached a node; a transaction
   it builds on is dead. Death cascades to descendants in the
   same step.
7. **Release**: an output is released when no unfinished attempt of the
   holding intent spends it. Nothing else releases.
8. **A drop notice is a hint.** It may trigger a second attempt (rule 3
   makes that safe with or without the notice). It releases nothing.
9. **A submission error is a refusal only when the node said no.** A
   timeout or a broken connection leaves the attempt unfinished.

### Persistence

- `Reserve` commits the attempt record and its reservations in one
  transaction and returns before the submitter may send. After a restart
  every unfinished record is treated as sent.
- `Advance` commits the state changes, the releases and the settled point in
  one transaction. The settled point on disk is never behind a release on
  disk.
- After a restart chain-sync intersects at `ResumePoint()` or later. If the
  view is rebuilt from an earlier point, `Reserve` must refuse until the
  view has reached the settled point again. (The refusal variant is not
  model-checked; resuming at the settled point is.)
- Drop notices live in memory.

### What this changes in the plan

- `Ledger.Release(id, Dropped)` and `TestReleaseOnDrop` as written release
  on a drop: that is `PlannerReleaseOnDropBug`.
- `Ledger.Confirm` "reservation becomes permanent spend" must happen at K
  deep, not at confirmation: `PlannerSettleAtTipBug`.
- The submitter's "Dropped is detected by TTL expiry" must use the K-deep
  slot: `PlannerExpireAtTipBug`. Its `Rebuild` action must go through
  `Reserve`, so it cannot pick fresh inputs: `PlannerFreshInputsBug`.
- The chain follower must expose the K-deep point and resume from it; "the
  view is rebuilt by chain-sync from origin" is `PlannerRestartFromOriginBug`
  unless the planner waits.

## Code map

Models cardano-continuum at `243b05a`. `internal/plan` implements the
contract; its package comment (`internal/plan/plan.go`) maps each planner
action, and the engine's side is `internal/engine/internal/pay/pay.go`,
whose package comment maps both this model and `submit-retry`.

| Model | Code | What |
|---|---|---|
| `Offer` | `internal/plan/ledger.go:112` (`Ledger.Available`), `internal/plan/state.go:38` (`state.available`) | free outputs and own unfinished change |
| `Build` | `internal/plan/ledger.go:122` (`Ledger.Reserve`), `internal/plan/state.go:98` (`state.reserve`), `internal/plan/attempts.go:191` (`coherent`), `:209` (`attempts.excludes`), `:56` (`attempts.add`) | rule 3, then the inputs are held |
| `Persist` | `internal/plan/ledger.go:73` (`Ledger.commit`), inside `Reserve` before it returns | on disk before it is handed to a node |
| `Deliver` | `internal/engine/internal/pay/retry.go:121` (`Retry.Answer`) | the answer to a send |
| `Process` | `internal/plan/ledger.go:134` (`Ledger.Advance`): `internal/plan/state.go:182` (`state.forward`: `view.extend` at `internal/plan/view.go:90`, `state.settle` at `state.go:161`, `state.reap` at `state.go:147`) or `state.go:197` (`state.rollback`, `view.rollback` at `view.go:124`) | settle and write off what the new view decides |
| `SubmitFails` | `internal/plan/ledger.go:171` (`Ledger.Refused`), `internal/plan/state.go:203` (`state.refused`) | written off with its descendants |
| `ObserveDrop` | `internal/plan/ledger.go:193` (`Ledger.DropNotice`) | changes no state |
| `Restart` | `internal/plan/ledger.go:60` (`Open`), `internal/plan/encode.go:61` (`decode`); chain-sync resumes at `internal/plan/ledger.go:207` (`Ledger.ResumePoint`) through `internal/chain/node/follower.go:87` (`Follower.Run`) | the view resumes at the settled point |
| `Extend`, `IncludeTx`, `AdvSpend`, `Rollback`, `MempoolDrop` | the node; they reach the planner as chain events through `internal/engine/internal/pay/pay.go:150` (`Pay.Advance`) | not continuum code |
| the engine's side | `internal/engine/internal/pay/pay.go:139` (`Pay.Reserve`) | the only caller of `plan.Ledger.Reserve` |

Where each invariant is checked. The store invariants are asserted on every
state the ledger adopts, in debug builds (`-tags debug`), by `checkAdopt`
(`internal/plan/ledger.go:93`), which runs `state.consistent` and
`state.indexConsistent`:

- in `Ledger.commit`, so after every `Reserve`, `Refused` and every
  `Advance` that moves the settled point or writes an attempt off;
- in `Advance` on the volatile path, when only blocks above the settled
  point moved and nothing is stored.

That is once per model step, the granularity at which TLC checks an
invariant. Reads (`Available`) trust the owner and actor indexes and the
spent cache, which `indexConsistent` and `view.cacheConsistent` check at
the same points. `decode` runs `state.consistent` in every build, so
`Open` refuses a stored state that breaks the contract (`ErrBlob`).

| Invariant | Asserted by |
|---|---|
| `ReservedWhileUnfinished`, release half of `NoLeak` | `internal/plan/attempts.go:246` (`attempts.consistent`) |
| `InputsTracked`, doomed half of `NoLeak` | `internal/plan/state.go:214` (`state.consistent`) |
| settled point at most K below the tip (`SettledViewIsChain` as the ledger can see it) | `internal/plan/view.go:146` (`view.consistent`) |
| `NoSelfConflict` | enforced by `state.reserve`; checked by the property tests (`internal/plan/prop_test.go`, `internal/plan/rig_test.go`) |
| `IntentAtMostOnce`, `DeadIsDead` | the oracle's `checkPaid` (`internal/oracle/checks.go:56`), against the chain |
| `ReservedWhileUnfinished`, `NoLeak` on a run | the oracle's `checkReservations` (`internal/oracle/checks.go:99`) |
| `SettledIsFinal` | truth only; on a run the oracle's `checkSettled` (`internal/oracle/checks.go:166`) reports a settled point that moved back |
| `Finishes` | no runtime check; `NoLeak` is its state form |

### Drift

Where the model deliberately differs from the code:

| Id | Code | Model | Why | Config |
|---|---|---|---|---|
| P1 | outputs carry values; the planner balances fees, minimum outputs and change | outputs carry no value; one change output per transaction | reservation rules do not depend on value | all |
| P2 | a block holds many transactions, positioned by slot | one transaction per block; a block's position is its slot (TTL "the K-deep block's slot is past the TTL" becomes "the settled length has reached the TTL") | interleavings of single-transaction blocks cover multi-transaction ones | all |
| P3 | outputs are owned by actors at addresses; `Available` filters by owner | any intent may be offered any output | gives the planner the most chances to race itself | all |
| P4 | `Coherent` is enforced by `coherent` in `state.reserve` | kept in `Build`, though no invariant needs it at these bounds | it only stops stillborn transactions | `Planner`, `PlannerRestart`, `PlannerEagerRebuild`, `PlannerK2` |
| P5 | a rollback deeper than K is an error in the chain follower | never generated | the follower's job, not the store's | - |
| P6 | the store writes a whole blob per commit | a restart loses whole records, never part of one | torn writes are SQLite's job | `PlannerRestart`, `PlannerPersistLateBug` |
| P7 | `TestPropPlannerMatchesModel` (`internal/plan/prop_test.go:21`) drives random interleavings of the model's actions and checks the store invariants | no trace replay of TLC counterexamples | the property test mirrors the invariants, not TLC's traces | - |

## Go tests this model asks for

The node numbers are taken to be: 16 the chain follower (`internal/chain/node`),
17 the planner (`internal/plan`), 23 the submitter (`internal/submit`).

### Chain follower (16)

- `TestSettledPointNeverMovesBack`: after a rollback the reported K-deep point is unchanged.
- `TestResumeFromSettledPoint`: a restarted follower intersects at the stored settled point and replays from there with no gap.
- `TestRollbackDeeperThanKIsError`: a roll-backward below the settled point is an error, not a reset.
- `TestSettleReportsSlot`: each settled block carries its slot, so the planner can compare it with a TTL.

### Planner (17)

- `TestDropNoticeReleasesNothing`: after `DropNotice`, `Available` for another intent is unchanged.
- `TestDropNoticeAfterInclusionDoesNotPayTwice`: the `PlannerFreshInputsBug` order; the second attempt shares an input, so the fake chain accepts one.
- `TestReserveRefusesFreshInputsForSecondAttempt`: `Reserve` for an intent with an unfinished attempt and disjoint inputs returns the not-exclusive error.
- `TestSecondAttemptExcludesEveryEarlierAttempt`: the `PlannerConflictWithLatestOnlyBug` order; a third attempt built on the first's change is refused.
- `TestSecondAttemptOnReplacedParent`: a child rebuilt on the parent's second attempt is accepted though it shares no input with the child's first attempt.
- `TestReserveRefusesContradictoryInputs`: an attempt built on two attempts that exclude each other is refused.
- `TestConfirmedNotSettledKeepsReservation`: a transaction in the view fewer than K deep still holds its inputs.
- `TestRollbackKeepsReservationAndDescendants`: the `PlannerSettleAtTipBug` order; after the rollback parent and child are unfinished and nothing is offered to another intent.
- `TestSettleReleases`: at K deep the attempt is settled, its inputs are gone from `Available`, and its intent accepts no further attempt.
- `TestDeadOnlyOnSettledConflict`: an input spent by another transaction writes the attempt off at K deep and not before.
- `TestDeadOnlyOnSettledExpiry`: the `PlannerExpireAtTipBug` order; the tip passing the TTL changes nothing, the K-deep slot passing it does.
- `TestDeathCascadesToDescendants`: the `PlannerNoCascadeBug` order; `Refused(parent)` returns the child and releases the child's other inputs.
- `TestSettledSiblingKillsOtherAttempts`: when one attempt settles, the intent's other attempts and everything built on them die in the same `Advance`.
- `TestReserveIsDurableBeforeReturn`: with a store fake that fails the commit, `Reserve` returns the error and reserves nothing.
- `TestRestartKeepsUnfinishedReservations`: the `PlannerPersistLateBug` order; reopen the store mid-flight and no reserved input is offered.
- `TestRestartDoesNotOfferSettledInputs`: the `PlannerRestartFromOriginBug` and `PlannerSettledPointFollowsTipBug` orders; after reopening, inputs of settled transactions are not offered.
- `PropPlannerMatchesModel`: random interleavings of `Reserve`, `Advance`, `Refused`, `DropNotice` and reopen against a fake chain keep `ReservedWhileUnfinished`, `NoLeak`, `InputsTracked`, `NoSelfConflict` and `IntentAtMostOnce`.

### Submitter (23)

- `TestTransportErrorIsNotRefusal`: the `PlannerReleaseOnSubmitErrorBug` order; a timeout or closed connection yields no `Release` action.
- `TestNodeRefusalReleases`: a rejection from local-tx-submission calls `Refused` once.
- `TestDropYieldsRebuildNotRelease`: a drop notice yields `Rebuild`, never `Release`.
- `TestRebuildGoesThroughReserve`: `Rebuild` builds from `Available(intent)` and fails closed when `Reserve` refuses.
- `TestRolledBackYieldsNoRelease`: `RolledBack` leaves the attempt pending; it resubmits the same bytes.
- `TestGaveUpWaitsForSettledExpiry`: giving up stops retries; the reservation goes only when the planner reports the attempt dead.
- `TestRestartTreatsUnfinishedAsSent`: after a restart every unfinished attempt is tracked as pending, never rebuilt from scratch.
