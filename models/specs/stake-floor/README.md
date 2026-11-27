# stake-floor: the engine's go-dark, retire and delegation guard

A devnet stalls when the pools that actually forge hold too little of the
stake the leader schedule is drawn from. The engine decides when pools go
dark, retire and when wallets move their delegation, and the chain feels
those decisions late. The schedule of epoch e is the stake as it stood at the
end of epoch e - 2. A retirement takes effect at the boundary of the epoch it
names, and the retired pool's stake stays in the schedule for two more
epochs. A guard that looks at "the stake now" can approve every decision and
still leave an epoch two boundaries later below the floor, with no engine step
in between.

`StakeFloor.tla` models the ledger's snapshots, the pool lifecycle, the
engine's guard, pod deaths and the engine's recovery, at a small scale. It is
a design model: it constrains the population manager (2.4), the agendas
(2.5, the operator agenda in particular) and the engine's policy before the
guard is written. It does not pick the floor's value: that is probabilistic
(Praos leader election), and FORMAL-METHODS.md section 4 leaves it to a Monte
Carlo of the schedule. The model checks that a guard keeps whatever floor is
chosen.

## What the model contains

- **Pools and wallets.** Three pools, four wallets; `w1` holds 3 units and
  the others 1 each. `p1` starts with 3, `p2` with 1, `p3` with 2. At
  `Floor = 70` the engine may take `p2` dark (5 of 6) but not `p3` (4 of 6),
  so most decisions are near the line.
- **The ledger's snapshots.** `deleg` is the confirmed delegation now; `mark`
  and `set` are the delegations at the last two epoch transitions. The
  transition runs at the first block of an epoch: it rotates the snapshots
  (`set := mark`, `mark := deleg`), then retires the pools whose epoch has
  come and undelegates their wallets (SNAP before POOLREAP, so the retired
  pool's stake is in the new `mark`). The schedule in use (`Lead`) is `set`
  after the transition and `mark` before the epoch's first block, which is
  what the transition will install.
- **Freezing.** `label` is the last epoch whose transition ran. If an epoch
  passes with no block at all, the next block is more than one epoch past
  the label and the transition never runs again (`Frozen`): no snapshot
  rotates, no retirement applies, no delegation reaches the schedule.
- **Blocks.** `Block` needs some forging pool with stake in `Lead`. It lands
  every transaction in flight. The leader election abstraction is in
  `Boundary`: an epoch in which the floor held throughout must have had a
  block and must have landed every transaction submitted in it (their TTL
  ends inside the epoch). Below the floor nothing is promised, unless
  `BlocksBelowFloor` says that any forging stake above zero still gives a
  block per epoch.
- **The engine's steps.** `GoDark`, `Retire` (dark first when `DarkFirst`,
  per pool-forging rule 18; otherwise the pool forges until its retirement
  epoch and the harness stops it then), `Delegate` (including undelegation),
  `GoLive`, `Reregister` (cancels a retirement) and `Repair` (a dead pod
  brought back on a new node or with a new opcert; needs no transaction).
  The first three pass through `Allowed`; the last three, and a delegation
  that only moves stake from a non-forging pool to a forging one (`Adds`),
  never need the guard.
- **The guard** (`Allowed`). It projects the three schedules the decision can
  still reach: `Lead`; the next one (`mark` once this epoch's transition ran,
  today's `deleg` before it); and the one after, `deleg` with every
  transaction in flight and the decision applied. It counts as forging only
  pools that are registered, planned live and whose pod is alive, and not a
  pool with a retirement confirmed or in flight. Its denominator is every
  delegated wallet in the projected schedule. Each schedule must meet the
  floor after the loss of any `FaultMargin` forging pools. Later schedules
  only lose stake of pools the guard already counts as dark, so three are
  enough.
- **Pod deaths** (`Fail`). An expired KES key, a lost node or a crash loop:
  the pool is dark whatever the plan says.
- **Recovery.** With `Recovery`, the engine reacts within the epoch
  (`Boundary` waits) when the floor is `Endangered`: breached now, or breached in one of the three
  schedules with only the pools that really forge. `RecoveryDone` is: every
  registered dark pool with no retirement in flight is live again, every
  dead pod is repaired (if `Repairable`), and every wallet on a pool that
  does not forge has a move to a forging pool in flight. The guard configs
  run with `Recovery = FALSE`: the guard alone must keep the floor. With
  recovery on, `FloorNextEpochOnlyBug` and `FloorIgnoreInflightBug` pass at
  these bounds,
  because recovery reverts the agenda's decisions before the epoch ends;
  the guard is what keeps recovery from having to fire.
- **The census.** `cen` is the population's view of each pool's actor. It
  follows confirmations: Retiring when the certificate lands, Retired when a
  transition retires the pool, back to active when a re-registration lands.

### Deviations from the plan's outline

- **The schedule is `set`, not `go`.** The plan says the leader schedule of
  epoch e uses "the go snapshot". cardano-ledger elects from the stake at the
  end of e - 2, which after the transition into e is `set`; `go` feeds the
  reward calculation only. Source: dingo `consensus/praos/snapshot.go`
  (`StakeSnapshotEpoch`) and `ledger/queries.go`
  (`queryShelleyStakeSnapshots`) at dingo 7de9e83eb. `go` is not modelled.
- **An empty epoch freezes the ledger.** Not in the plan. Taken from dingo's
  `LeaderStakeSnapshotEpoch`, which says NEWEPOCH runs only for the label's
  successor; not checked against cardano-ledger itself. If that reading is
  wrong, a stalled epoch delays the effects rather than stopping them, and
  `NoFreeze` becomes "no epoch without a block", which is still what the
  floor must buy.
- **`FloorGateOnMarkBug` is `FloorGateOnLiveBug`.** The plan's "the live
  distribution" is the ledger's current delegation, which is not `mark`.
- **`FloorIgnoresKesBug` is `FloorPlanIsForgingBug`, and breaks recovery,
  not the floor.** With a margin, a dead pod the guard still counts is
  exactly one of the pools the margin already subtracts, so `FloorHolds`
  survives it (the passing `FloorMargin` exploration covers it). What fails
  is recovery: an engine that believes the plan neither repairs the pod nor
  moves stake off it.
- **`PolicyLooksAhead` is not a separate invariant.** `FloorHolds` is checked
  in every state, including the traces where the engine stops deciding after
  a step, which is what `PolicyLooksAhead` asks.
- **Liveness is a safety invariant.** `NoFreeze` (every epoch has a block)
  and `RecoverBound` stand in for `[]<>(ForgingStake > 0)`, under the timing
  assumptions in `Boundary`.
- **No Apalache config.** Integers stay at 0..6; the guard's arithmetic is
  linear, and the property tests in the Go list cover realistic stake sizes.
- **Bounds on the engine.** `MaxMoves` and `MaxDarks` cap the guarded moves
  and go-dark steps in a run, and one re-registration may cancel one
  retirement; without them the passing configs do not finish in four
  minutes.

## Not modelled

- The floor's value, slot-level leader election, chain quality and
  rollbacks. A confirmed delegation or certificate is final here; in the
  code, "confirmed" means K deep (engine-recovery design A).
- The 2-epoch lag of `go` for rewards, deposits and refunds, pool pledge and
  saturation, and the denominator's exact era rules (total active stake is
  assumed; unverified per era).
- Pools registered during the run. A new pool starts with no stake in any
  schedule, so it cannot lower the floor; it only adds a target.
- The order of transactions inside a block, and a delegation that lands in
  a later epoch than it was submitted in (the TTL rule forbids it).
- How the engine learns that a pod is dead (operator status, the fork
  monitor's missed slots). The model's engine knows at once.
- Re-registration of a pool that has already retired (a new registration,
  not a cancellation).

## Invariants

| Invariant | Says | Broken by |
| --- | --- | --- |
| `TypeOK` | every variable has its shape | - |
| `FloorHolds` | while no more than `FaultMargin` pods are dead, the pools really forging hold at least `Floor` percent of the stake in the schedule in use, in every state of every epoch | `FloorGateOnLiveBug`, `FloorThisEpochOnlyBug`, `FloorNextEpochOnlyBug`, `FloorIgnoreRetiringBug`, `FloorTotalFromCensusBug`, `FloorUnguardedDelegateBug`, `FloorIgnoreInflightBug`, `FloorNoMarginBug` |
| `RetiredOnlyAtBoundary` | the census calls a pool Retired only once a transition has retired it | `FloorRetireMarkedEarlyBug` |
| `NoFreeze` | no epoch passes without a block | `FloorRecoverFreezeBug`, `FloorRecoverWorstCaseBug` |
| `RecoverBound` | unless the ledger froze, the floor is back within `RecoverEpochs` epochs of the latest pod death | `FloorRecoverInTwoBug`, `FloorRecoverNoRedelegateBug`, `FloorRecoverLateBug`, `FloorPlanIsForgingBug` |

## Configs and state counts

Unless a row says otherwise: `Floor = 70`, `MaxEpoch = 3`, `RetireMax = 2`,
no pod deaths, `FaultMargin = 0`, `Recovery = FALSE`, `DarkFirst = TRUE`,
`BlocksBelowFloor = FALSE`, `MaxMoves = 2`, `MaxDarks = 2`. "Recovery
setting" is `Recovery = TRUE`, `MaxFaults = 2`, `FaultMargin = 0`,
`Repairable = FALSE`, `BlocksBelowFloor = TRUE`, `RecoverEpochs = 3`,
`MaxEpoch = 4`, `MaxMoves = 0`, `MaxDarks = 1`.
TLC 2.19 (tla2tools 1.7.4), `-workers auto`, 14 cores. A counterexample's
length is in states. A violated config stops at its first counterexample, so
its count is not the whole state space. Each trace was read and fails for
the reason in its row.

| Config | Mutation or probe | Bounds changed | Expected | TLC reported |
| --- | --- | --- | --- | --- |
| `Floor` | none | - | all hold | 1,607,788 distinct, 7,338,366 generated, depth 21 |
| `FloorLiveRetire` | none | `DarkFirst = FALSE`, `MaxMoves = 1` | all hold | 253,404 distinct, 1,272,091 generated, depth 22 |
| `FloorMargin` | none | `Floor = 30`, `FaultMargin = 1`, `MaxFaults = 1` | all hold | 3,520,332 distinct, 12,358,724 generated, depth 25 |
| `FloorRecover` | none | recovery setting | `RecoverBound`, `RetiredOnlyAtBoundary` hold | 124,008 distinct, 343,110 generated, depth 30 |
| `FloorRecoverRepair` | none | recovery setting, `RecoverEpochs = 1`, `Repairable = TRUE` | `RecoverBound`, `NoFreeze` hold | 1,407,693 distinct, 6,187,718 generated, depth 28 |
| `FloorGateOnLiveBug` | `GateOnLive` | - | `FloorHolds` | violated, 281 distinct, trace 3 |
| `FloorThisEpochOnlyBug` | `ThisEpochOnly` | - | `FloorHolds` | violated, 16,408 distinct, trace 9 |
| `FloorNextEpochOnlyBug` | `NextEpochOnly` | - | `FloorHolds` | violated, 18,531 distinct, trace 8 |
| `FloorIgnoreRetiringBug` | `IgnoreRetiring` | `DarkFirst = FALSE` | `FloorHolds` | violated, 1,912 distinct, trace 5 |
| `FloorTotalFromCensusBug` | `TotalFromCensus` | - | `FloorHolds` | violated, 27,383 distinct, trace 9 |
| `FloorRetireMarkedEarlyBug` | `RetireMarkedEarly` | - | `RetiredOnlyAtBoundary` | violated, 289 distinct, trace 4 |
| `FloorUnguardedDelegateBug` | `UnguardedDelegate` | - | `FloorHolds` | violated, 21,414 distinct, trace 9 |
| `FloorIgnoreInflightBug` | `IgnoreInflight` | - | `FloorHolds` | violated, 26,457 distinct, trace 7 |
| `FloorNoMarginBug` | `NoMargin` | `Floor = 30`, `FaultMargin = 1`, `MaxFaults = 1` | `FloorHolds` | violated, 424 distinct, trace 3 |
| `FloorPlanIsForgingBug` | `PlanIsForging` | recovery setting, `RecoverEpochs = 1`, `Repairable = TRUE` | `RecoverBound` | violated, 871 distinct, trace 6 |
| `FloorRecoverInTwoBug` | none | recovery setting, `RecoverEpochs = 2` | `RecoverBound` | violated, 36,380 distinct, trace 14 |
| `FloorRecoverLateBug` | `RecoverOnBreach` | recovery setting, `RecoverEpochs = 1`, `Repairable = TRUE` | `RecoverBound` | violated, 537,318 distinct, trace 17 |
| `FloorRecoverFreezeBug` | none | recovery setting | `NoFreeze` | violated, 3,419 distinct, trace 11 |
| `FloorRecoverNoRedelegateBug` | `NoRedelegate` | recovery setting | `RecoverBound` | violated, 2,846 distinct, trace 12 |
| `FloorRecoverWorstCaseBug` | none | recovery setting, `MaxFaults = 1`, `Repairable = TRUE`, `BlocksBelowFloor = FALSE` | `NoFreeze` | violated, 1,759 distinct, trace 6 |
| `FloorProbeDarkBug` | probe | - | `ProbeNeverDark` | violated: reachable, 2 distinct, trace 2 |
| `FloorProbeLagBug` | probe | - | `ProbeNoLag` | violated: reachable, 241 distinct, trace 5 |
| `FloorProbeRetiredBug` | probe | - | `ProbeNeverRetired` | violated: reachable, 2,643 distinct, trace 8 |
| `FloorProbeReregisterCancelsBug` | probe | - | `ProbeNoCancel` | violated: reachable, 2,346 distinct, trace 6 |
| `FloorProbeLongBreachBug` | probe | recovery setting | `ProbeShortBreach` | violated: reachable, 516 distinct, trace 7 |

## What it decides

1. **Gate on the schedules, not on the stake.** The current delegation is
   what the chain will use two epochs from now; it says nothing about this
   epoch or the next. All three must be checked (`FloorGateOnLiveBug`,
   `FloorThisEpochOnlyBug`, `FloorNextEpochOnlyBug`).
2. **Before the epoch's first block the schedules are one step behind.**
   The schedule in use is still `mark` and the next one is today's
   delegation, so the guard keys its schedules on whether the transition has
   run, not on the epoch number (rule 4). No config isolates a guard that
   gets this wrong; `TestScheduleSelection` pins it.
3. **Delegations are lifecycle decisions.** A wallet moving 3 units onto a
   dark pool breaks the floor as surely as a pool going dark
   (`FloorUnguardedDelegateBug`), and two moves in flight together break it
   where either alone would not (`FloorIgnoreInflightBug`).
4. **A retiring pool is dark to the guard from the moment its certificate is
   submitted, and its stake stays in the denominator for two epochs after it
   retires.** Either order of "go dark" and "retire" is then safe
   (`Floor`, `FloorLiveRetire`); counting a retiring pool as forging
   (`FloorIgnoreRetiringBug`) or taking the denominator from the census
   (`FloorTotalFromCensusBug`) is not.
5. **Recovery cannot replace margin.** Below the floor nothing promises a
   block, and one empty epoch freezes the ledger, so a death beyond the
   margin can stall the devnet even when the engine repairs it at once
   (`FloorRecoverWorstCaseBug`). The margin is the protection; recovery
   restores it.
6. **How long recovery takes.** Repair (a new pod, a new opcert) needs no
   transaction and brings the floor back in the epoch of the death
   (`FloorRecoverRepair`, `RecoverEpochs = 1`). Without repair the stake has
   to move: back within 3 epochs, 2 when a block can still land in the epoch
   of the death (`FloorRecover`, `FloorRecoverInTwoBug`). If the dead pools
   hold all the stake in the schedule, no block can carry the move and the
   ledger freezes (`FloorRecoverFreezeBug`).
7. **Recovery must start on a projected breach.** A pod that dies after the
   epoch's last block can leave this epoch above the floor and the next one
   below it; waiting for the breach loses the epoch in which the move could
   still land (`FloorRecoverLateBug`). An engine that believes its plan
   instead of the pods never starts (`FloorPlanIsForgingBug`).

## STAKE-FLOOR GUARD CONTRACT

Rules for the developers of the population manager, the agendas and the
engine's policy. Each names the config that fails without it.

### What is guarded

1. Every intent that can lower forging stake goes through one guard,
   whoever chose it: `GoDark`, `RetirePool`, and `Delegate` or `Undelegate`
   of a wallet currently on a forging pool, or onto a pool that is not
   forging. (`FloorUnguardedDelegateBug`)
2. Never guarded, so recovery is never blocked: `GoLive`, repairing a pod,
   re-registration, and a delegation from a non-forging pool to a forging
   one.
3. The guard is a pure function of the ledger view (the three schedules
   below, registrations and retirement epochs), the engine's transactions
   in flight, the plan and the observed pod health. It reads nothing else,
   so the Go policy can be compared with the model's `Allowed` over the
   model's domain.

### What it checks

4. Three schedules, keyed on whether the current epoch's transition has
   run (the follower has seen a block of this epoch), not on the epoch
   number:
   - after the transition: `set`, `mark`, and the projection;
   - before it: `mark`, today's confirmed delegation, and the projection;
   - the projection is the confirmed delegation with every engine
     transaction in flight and the candidate applied.
   (`FloorGateOnLiveBug`, `FloorThisEpochOnlyBug`, `FloorNextEpochOnlyBug`)
5. Every transaction in flight counts as landed: delegations, retirements.
   A re-registration in flight does not count until it confirms.
   (`FloorIgnoreInflightBug`)
6. Forging means registered, planned live, pod observed running (operator
   status, not the plan), and no retirement confirmed or in flight.
   (`FloorIgnoreRetiringBug`, `FloorPlanIsForgingBug`)
7. The denominator is all delegated stake in that schedule, taken from the
   snapshot itself. Never subtract pools the census or the ledger calls
   retired: their stake stays in the schedule for two epochs after.
   (`FloorTotalFromCensusBug`)
8. Each schedule meets the floor after removing any `FaultMargin` forging
   pools (the largest ones are the worst case). `FaultMargin` is a profile
   parameter, at least 1. (`FloorNoMarginBug`, `FloorRecoverWorstCaseBug`)

### Lifecycle order

9. Retire dark first (pool-forging rule 18): dark, pod gone, then the
   certificate. A pool with a retirement in flight is not brought live.
10. A delegation or certificate is submitted with a TTL that ends inside
    the current epoch, so the epoch its effect reaches is known when the
    guard runs.
11. The census moves a pool to Retiring when its certificate confirms, to
    Retired only when a transition has applied it (the ledger shows it
    retired), and back from Retiring to Active when a re-registration
    confirms. (`FloorRetireMarkedEarlyBug`, `FloorProbeReregisterCancelsBug`)

### Recovery

12. Recovery starts when any of the three schedules, with the pools that
    really forge, is below the floor, not only the one in use.
    (`FloorRecoverLateBug`)
13. In the same epoch, the engine brings every registered dark pool with no
    retirement in flight live, repairs every dead pod it can, and submits a
    move to a forging pool for every wallet on a pool that does not forge.
    (`FloorRecoverNoRedelegateBug`)
14. Detection and reaction take much less than an epoch. Expect the floor
    back in the epoch of the death after a repair, and within 3 epochs when
    stake has to move. An epoch with no block is a fatal incident for the
    run: report it, do not try to recover from it.

## Code map

The guard is pure, in `internal/policy`; the engine's side of it is
`internal/engine/internal/floor`, whose package comment maps each contract
rule. In short:

| Model | Code |
|---|---|
| `Allowed` | `policy.Guard.Check` (`internal/policy/guard.go`); `TestPolicyAgreesWithModel` (`internal/policy/model_test.go`) compares it with a transliteration over the model's domain |
| `GoDark`, `Retire`, `Delegate` (guarded) | `floor.Floor.Check`, the one gate for every intent that can lower forging stake (`internal/engine/internal/floor/floor.go`) |
| `GoLive` | `floor.Floor.SetTier` (never guarded) |
| `Fail` | `floor.Floor.PodStatus` |
| `Repair` | `floor.Floor.Recover`, through its repair step |
| the guard's input (rule 3) | `floor.Floor.State`, from `chain.View.PolicyState` (`internal/chain/view.go`) and the in-flight list `Floor.inflight` |
| `Block`, `Boundary` (the transition label, `set`, `mark`) | `epochs.next` in `internal/chain/ledger.go` and `View.PolicyState` (`internal/chain/view.go`, helpers in `policy.go`), which key the schedules on whether this epoch's transition ran |
| recovery (rules 12 to 14) | `policy.Guard.Endangered` and `policy.Guard.Recover`, run by `Floor.Recover` after every block and pod event |
| census (rule 11) | `internal/population` (`state.go`) and the engine's cohort, `internal/engine/internal/cohort` |
| rule 10 (TTL inside the epoch) | the guarded kinds of the intent spec (`internal/engine/internal/intent/spec.go`) and `Maker.TTL` (`make.go`) |

Rule 10 has a second user since this model: a governance vote on a
proposal in its last epoch takes the same epoch-bounded TTL
(`specs/governance-change`).

## Go tests the model implies

Each names the config it comes from.

### Population, node 9 follow-up (`internal/population`)

- `TestPoolRetiringNeedsConfirmation`: Active to Retiring is refused from a
  timer or intent cause and accepted from the certificate's confirmation
  (rule 11; today the edge is not chain-gated).
- `TestPoolRetiredOnlyAtEpochApply`: Retiring to Retired is refused on the
  certificate's confirmation and accepted only from an epoch-applied cause
  (`FloorRetireMarkedEarlyBug`).
- `TestPoolReregisterCancelsRetiring`: a confirmed re-registration moves
  Retiring back to Active; the edge exists and is chain-gated
  (`FloorProbeReregisterCancelsBug`).
- `TestCensusNotAStakeSource`: the census exposes no per-pool stake total for
  the policy to use (`FloorTotalFromCensusBug`).

### Agendas, node 31 (`internal/agenda`)

- `TestOperatorGoDarkVetoedScoresZero`: with a `View` whose floor check
  refuses, `GoDark` and `RetirePool` score 0 with a `Why` naming the floor.
- `TestOperatorRetireOnlyWhenDark`: `RetirePool` is offered only for a pool
  already dark with its pod gone (rule 9).
- `TestYieldMoveOntoDarkPoolVetoed`: a redelegation off a forging pool onto
  a dark one scores 0 when the floor check refuses
  (`FloorUnguardedDelegateBug`).
- `TestRecoveryMoveNeverVetoed`: a move from a dead or dark pool to a
  forging pool scores normally even when the floor is breached (rule 2).

### Engine policy (`internal/policy`)

- `TestScheduleSelection`: table over (epoch, transition run or not): the
  three schedules are set/mark/projection after and mark/deleg/projection
  before (rule 4).
- `TestGuardUsesSnapshotNotLive`: stake moved this epoch onto pool A; taking
  B dark is refused although the live delegation allows it
  (`FloorGateOnLiveBug`).
- `TestGuardLooksTwoEpochsAhead`: a decision that breaks only the next or
  only the projected schedule is refused (`FloorThisEpochOnlyBug`,
  `FloorNextEpochOnlyBug`).
- `TestGuardCountsInFlight`: two delegations in flight that together break
  the floor: the second is refused (`FloorIgnoreInflightBug`).
- `TestGuardRetiringIsDark`: a pool with a retirement confirmed or in
  flight is never counted as forging (`FloorIgnoreRetiringBug`).
- `TestGuardDenominatorFromSnapshot`: two epochs after a retirement, the
  retired pool's stake is still in the denominator
  (`FloorTotalFromCensusBug`).
- `TestGuardFaultMargin`: a decision that meets the floor only while the
  largest forging pool lives is refused (`FloorNoMarginBug`).
- `TestGuardUsesObservedPods`: a pool planned live with a dead pod counts as
  dark (`FloorPlanIsForgingBug`).
- `TestPolicyAgreesWithModel`: over the model's domain (three pools, four
  wallets, stake 0..6), the Go guard and a transliteration of `Allowed`
  agree on every intent (rule 3).
- `PropGuardMonotone`: for random states, an allowed decision stays allowed
  if any pool goes live or any wallet moves to a forging pool.

### Engine recovery (`internal/engine`, with `internal/engine/internal/floor`)

- `TestRecoveryStartsOnProjectedBreach`: a pod dies after the epoch's last
  block, the current schedule is fine and the next is not: recovery
  intents are emitted in this epoch (`FloorRecoverLateBug`).
- `TestRecoveryIntentsInOneEpoch`: after deaths beyond the margin the engine
  emits GoLive for every dark pool, a repair for every dead pod and a move
  for every wallet on a dead pool before the boundary
  (`FloorRecoverNoRedelegateBug`).
- `TestRecoveryBound` (fake chain, seeded): with repair the floor is back in
  the epoch of the death; without, within 3 epochs (`FloorRecoverRepair`,
  `FloorRecover`).
- `TestEmptyEpochIsFatal`: an epoch boundary with no block in the epoch
  stops the run with a stall incident (`NoFreeze`).
