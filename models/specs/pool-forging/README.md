# pool-forging: who may forge for a pool, and with which opcert

A pool goes live, goes dark, switches implementation, has its image rolled and
retires. One scenario deliberately runs two forgers for it. An *accidental*
second forger equivocates, and the fork monitor then reports the harness's own
mistake as node behaviour. An opcert with the wrong counter or KES period
makes every block of the pool invalid, and the pool goes dark without an
error anywhere.

Kubernetes gives less exclusion than it seems to. A StatefulSet keeps one pod
per ordinal only until someone force-deletes a pod on a node that has stopped
answering. Two StatefulSets (one per driver, or one per image) exclude nothing
between them. The operator decides from an informer cache, and during a
leader handover two reconcilers act at once.

`PoolForging.tla` models the engine, the operator, the kubelet and the chain
at a small scale and lets TLC interleave them. This is a design model: it
constrains `internal/operator` (2.16), `internal/operator/image` (2.16a) and
the engine's opcert issuer (2.2, decisions D6 and D15) before they are
written.

## What the model contains

- **The engine** owns four things per pool. `want` is its plan (live on an
  implementation, dark, or retired). `cr` is the ContinuumPool spec it writes
  (`impl`, `phase`, `gen`). `secret` is the opcert in the keys Secret, which
  is `[ino, kes, gen]` or blank. `scen` is the equivocation scenario (`off`,
  `on`, `ending`). Every engine step is decided from durable state: the plan,
  the spec, the Secret, the chain and a live pod list. An engine restart
  between two steps therefore changes nothing. `IssueCrash` is the one
  exception worth a step: the engine dies after the Secret write and before
  its own record of the counter (`ectr`).
- **Going live** is `Hold` (spec dark), `BlankSecret`, `Issue` (needs a blank
  Secret and an empty live pod list) and `GoLive`. Rotation and a switch of
  implementation take the same path. `Retire` needs the same blank Secret and
  empty list.
- **The operator** is one or two reconcilers. Each acts on its own snapshot
  `obs` of the spec, refreshed by `Observe`. It creates the pool's keyed pod,
  deletes it when the snapshot says dark, wrong implementation or old image,
  and creates or deletes the scenario's duplicate pod. The API server decides
  whether a create succeeds (`NameFree`); the operator's own belief about
  other pods is never part of the exclusion.
- **Pods** are `pending` (object exists, no process), `running`, `terminating`
  (delete requested, process still running through its grace period),
  `stopped` (kubelet reported the containers gone; the finalizer still holds
  the object) and, in mutations only, `orphan` (the object is gone and the
  process is not). `Start` mounts the Secret as it is at that moment. `Kill`
  ends the grace period. `PodDies` restarts the container in the same pod.
  `NodeLost` leaves the process forging while nothing reaches the kubelet;
  `NodeHeal` and `NodeDead` resolve it. `Release` removes the finalizer.
- **Image rolls**: `ImageRoll` moves the pool's resolved digest; the operator
  deletes the old-image pod and creates the new one under the same name.
- **The chain**: `Forge` lets any running key-holding process produce a
  block, including a terminating one and an orphan. `chainCtr` is the highest
  counter forged and `onKes` the KES start of that opcert. `fault` records
  the first rule a block broke. `KesTick` advances the KES period.

### How "aligned to epoch boundaries" is modelled

`Request` is the engine planning a change at an epoch boundary. The model
does not count slots or epochs. Retirement taking effect is the `Retire` step:
the engine submits the certificate only then, so the epoch boundary it names
is always later. What the model checks is the order (dark, gone, then
effective), not the distance to the boundary.

### Timing assumption

`KesTick` waits for every pool whose opcert is due for rotation while its
plan or its spec says live. In words: once the engine counts a rotation as
due (one period before expiry), it finishes before the period ends. Without
that, no rule can keep a live pool's key valid, because time is not something
the harness controls. Two things follow for the code:

- `SlotsPerKESPeriod` times the slot length must be much longer than a pod's
  termination grace period plus its start-up time. Genesis (2.17) picks it.
- If a node is lost for longer than that, the pool goes dark. That is the
  fence working, and the operator must report it (see the contract, rule 8).

### Deviations from the plan's outline

- **The fence is a name, not a check.** The plan has `Fence(existing, next)`
  and one StatefulSet per driver. `ForgingCacheFenceBug` and
  `ForgingFencePerDriverBug` show that any check the operator makes before a
  create loses a race during a leader handover. The model's design gives the
  keyed pod one name per `PoolID`, whatever the driver or digest, and lets the
  API server refuse the second create.
- **`obs` holds the spec only.** The plan's `obs[p]` also cached pods. The
  design never decides a create from cached pods, so the snapshot does not
  carry them (`CacheFence` adds one bit back).
- **No `KNodes`.** Each pod has its own `lost` flag, which is each pod on its
  own node. A partition is `NodeLost` without `NodeDead`.
- **`ForceDelete` is a mutation.** With the finalizer, a force delete is an
  ordinary delete; only `NoFinalizer` gives it an effect.
- **Blocks are not stored.** `fault` and `chainCtr` replace the plan's
  `blocks` history, which keeps the passing configs small.
- **`ForgingCounterFromSecretBug` became two configs.** The Secret is blanked
  before every issue, so it cannot supply a counter. The same mistake shows up
  as a counter taken from the engine's own record (`BookCounter`) or from a
  lagging chain view (`StaleCounter`).
- **The engine reads the pod list.** The plan had the operator alone confirm
  "gone". An operator status can be stale; a live list by the engine after it
  blanked the Secret cannot be overtaken (`ForgingEngineCachedListBug`,
  `ForgingIssueWithoutBlankBug`).
- **The duplicate has its own Secret.** The plan gates the duplicate on a
  network annotation. An annotation read is a check before a create, and
  loses the same race (`ForgingDuplicateSharedSecretBug`).
- **Amaru is left out.** `Impls` is `{"dingo", "cardano-node"}`; the spike
  (`docs/spikes/amaru-devnet.md`) found Amaru cannot join a blank devnet.
  Nothing in the model depends on which two implementations they are.

## Not modelled

- The `logical` and `validating` tiers (no keys, nothing to fence), budgets,
  topology, the mithril2pq sidecar and status reporting.
- Chain rollbacks. A block that carried the pool's highest counter can be
  rolled back. The contract (rule 12) says which counter to read; the model's
  `chainCtr` only grows.
- Someone removing the finalizer by hand, or editing the Secret. A cluster
  admin can defeat any fence.
- A container that restarts on a lost node. The kubelet would remount its
  cached Secret; the model allows `PodDies` only on a reachable node.
- A patch that reaches a newer pod with the same name. Rule 6 covers it with
  a UID precondition.
- Readiness. A process counts as a forger from the moment it starts, which
  is the cautious reading.
- Slots, epochs and how long a grace period is (see the timing assumption).

## Invariants

| Invariant | Says | Broken by |
| --- | --- | --- |
| `TypeOK` | every variable has its shape | - |
| `AtMostOneForger` | outside a scenario window a pool has at most one running key-holding process, across implementations, images and lost nodes | `ForgingFencePerDriverBug`, `ForgingCacheFenceBug`, `ForgingCreateBeforeDeleteBug`, `ForgingRollSurgeBug`, `ForgingNoFinalizerBug`, `ForgingLostIsGoneBug` |
| `DuplicateScoped` | a scenario adds exactly one forger, and no duplicate process runs once the scenario is over | `ForgingDuplicateOutlivesScenarioBug`, `ForgingDuplicateSharedSecretBug` |
| `ForgedBlocksValid` | no forged block had a counter below the chain's or more than one above it (`SpecCounterOk`) | `ForgingDoubleIssueBug` |
| `CounterNeverReused` | no two different opcerts of a pool were forged with the same counter, across a rotation, a restart or a switch | `ForgingReissueSameCounterBug`, `ForgingStaleCounterBug`, `ForgingIssueWithoutBlankBug` |
| `KesValidForSlot` | no block was forged with a KES key that starts after the block's period | `ForgingKesFromWallClockBug` |
| `SwitchCarriesCounter` | the Secret's opcert and every running process's opcert has a counter the ledger accepts now | `ForgingBookCounterSecretBug` |
| `NoDarkByHarness` | a pool planned and written live never has a running pod whose KES key is not valid for the current period | `ForgingRotateAtExpiryBug` |
| `NoForgeAfterRetire` | once retirement has taken effect nothing holds the pool's keys and runs | `ForgingRetireBeforeGoneBug`, `ForgingEngineCachedListBug` |
| `LiveForges` (liveness) | with fair steps, a pool planned live ends up forging for good | `ForgingGenerationRaceBug`, `ForgingKesFromGenesisBug` |
| `DarkStops` (liveness) | with fair steps, a pool planned dark or retired ends up with no key-holding process, and a pool planned retired is retired | `ForgingDarkKeepsPodBug` |

"Counter strictly increasing" is checked on what reaches the chain. Two
opcerts may share a counter if the first was never forged with and no process
can still hold it; the ledger cannot tell, and Praos leaves no other number
(the counter may not exceed the chain's by more than one).

## Configs and state counts

Unless a row says otherwise: one pool, `Impls = {"dingo", "cardano-node"}`,
one reconciler, `MaxEvo = 2`, `CPraos = TRUE`, and the bounds `req 3`,
`kes 2`, `dig 1`, `crash 1`, `death 1`, `loss 1`, `scen 0` (`MaxReq`,
`MaxPeriod`, `MaxDig`, `MaxCrashes`, `MaxDeaths`, `MaxLosses`, `MaxScen`).
With every default at once the single-reconciler state space passes 1.2
million distinct states in four minutes without finishing, so each passing
config turns some bounds down; between them they cover every fault. TLC 2.19 (tla2tools 1.7.4, as
`make models-install` pins), `-workers auto`. A counterexample's length is
given in states. A violated config stops at its first counterexample, so its
count is not the whole state space. Every config checks for deadlock: a run ends in `Done`,
where every pool has reached its plan (`Settled`: retired, dark or forging
as planned, no scenario on, every refreshing reconciler current); any other
state with no step left fails the config.

| Config | Mutation or probe | Bounds changed | Expected | TLC reported |
| --- | --- | --- | --- | --- |
| `Forging` | none | dig 0, death 0, loss 0 | all hold | 109,416 distinct, 487,613 generated, depth 60 |
| `ForgingFaults` | none | kes 1, req 2, crash 0 | all hold | 59,046 distinct, 293,231 generated, depth 39 |
| `ForgingOverlap` | none | two reconcilers, kes 1, dig 0, crash 0, death 0 | all hold | 340,791 distinct, 2,403,979 generated, depth 50 |
| `ForgingScenario` | none | kes 1, dig 0, req 2, scen 1, crash 0, death 0, loss 0 | all hold | 34,872 distinct, 154,458 generated, depth 41 |
| `ForgingScenarioFaults` | none | kes 0, req 1, scen 1, crash 0 | all hold | 16,820 distinct, 84,986 generated, depth 21 |
| `ForgingScenarioOverlap` | none | two reconcilers, kes 0, dig 0, req 2, scen 1, crash 0, death 0, loss 0 | all hold | 73,811 distinct, 496,120 generated, depth 32 |
| `ForgingSwitchInPlace` | `SwitchInPlace` | dig 0, death 0, loss 0 | all hold | 107,818 distinct, 466,795 generated, depth 48 |
| `ForgingTwoPools` | none | two pools, kes 1, dig 0, req 2, crash 0, death 0 | all hold | 149,864 distinct, 885,101 generated, depth 44 |
| `ForgingLive` | none | kes 1, req 2, crash 0, fair | `LiveForges` and `DarkStops` hold | 59,046 distinct, 293,231 generated, depth 39 |
| `ForgingFencePerDriverBug` | `FencePerDriver` | two reconcilers, kes 0, dig 0, crash 0, death 0, loss 0 | `AtMostOneForger` | violated, 6,377 distinct, trace 15 |
| `ForgingCacheFenceBug` | `CacheFence` | two reconcilers, kes 0, dig 0, crash 0, death 0, loss 0 | `AtMostOneForger` | violated, 12,807 distinct, trace 15 |
| `ForgingCreateBeforeDeleteBug` | `FencePerDriver`, `SwitchInPlace` | kes 0, dig 0, crash 0, death 0, loss 0 | `AtMostOneForger` | violated, 1,062 distinct, trace 12 |
| `ForgingRollSurgeBug` | `RollSurge` | kes 0, req 1, crash 0, death 0, loss 0 | `AtMostOneForger` | violated, 62 distinct, trace 10 |
| `ForgingNoFinalizerBug` | `NoFinalizer` | kes 0, dig 0, req 1, crash 0, death 0 | `AtMostOneForger` | violated, 51 distinct, trace 11 |
| `ForgingLostIsGoneBug` | `LostIsGone` | kes 0, req 2, crash 0, death 0 | `AtMostOneForger` | violated, 2,527 distinct, trace 13 |
| `ForgingGenerationRaceFixed` | none | as `ForgingGenerationRaceBug` | `LiveForges` holds | 81 distinct |
| `ForgingGenerationRaceBug` | `StaleLeader` | two reconcilers, kes 0, dig 0, req 1, crash 0, death 0, loss 0, fair | `LiveForges` | violated, 33 distinct, lasso of 12 states |
| `ForgingDoubleIssueBug` | `BookCounter` | dig 0, crash 0, death 0, loss 0 | `ForgedBlocksValid` | violated, 9,216 distinct, trace 14 |
| `ForgingBookCounterSecretBug` | `BookCounter` | dig 0, crash 0, death 0, loss 0 | `SwitchCarriesCounter` | violated: the jump is in the Secret before a block is forged |
| `ForgingDarkKeepsPodBug` | `DarkKeepsPod` | kes 0, dig 0, req 2, crash 0, death 0, loss 0, fair, no deadlock check (the stuck dark pool is the finding) | `DarkStops` | violated |
| `ForgingReissueSameCounterBug` | `BookCounter` | dig 0, death 0, loss 0 | `CounterNeverReused` | violated, 70,771 distinct, trace 21 |
| `ForgingStaleCounterBug` | `StaleCounter` | dig 0, crash 0, death 0, loss 0 | `CounterNeverReused` | violated, 25,944 distinct, trace 22 |
| `ForgingIssueWithoutBlankBug` | `IssueWithoutBlank` | two reconcilers, kes 1, dig 0, req 1, crash 0, death 0, loss 0 | `CounterNeverReused` | violated, 1,103 distinct, trace 19 |
| `ForgingKesFromGenesisBug` | `KesFromGenesis` | kes 1, dig 0, req 1, crash 0, death 0, loss 0, fair | `LiveForges` | violated, 127 distinct, lasso of 5 states |
| `ForgingKesFromWallClockBug` | `KesFromWallClock` | dig 0, crash 0, death 0, loss 0 | `KesValidForSlot` | violated, 941 distinct, trace 8 |
| `ForgingRotateAtExpiryBug` | `RotateAtExpiry` | dig 0, crash 0, death 0, loss 0 | `NoDarkByHarness` | violated, 1,734 distinct, trace 9 |
| `ForgingEngineCachedListBug` | `EngineCachedList` | kes 0, dig 0, crash 0, death 0, loss 0 | `NoForgeAfterRetire` | violated, 1,020 distinct, trace 11 |
| `ForgingRetireBeforeGoneBug` | `RetireBeforeGone` | kes 0, dig 0, crash 0, death 0, loss 0 | `NoForgeAfterRetire` | violated, 549 distinct, trace 10 |
| `ForgingDuplicateOutlivesScenarioBug` | `DuplicateOutlives` | kes 0, dig 0, req 1, scen 1, crash 0, death 0, loss 0 | `DuplicateScoped` | violated, 238 distinct, trace 10 |
| `ForgingDuplicateSharedSecretBug` | `DuplicateSharedSecret` | two reconcilers, kes 0, dig 0, req 1, scen 1, crash 0, death 0, loss 0 | `DuplicateScoped` | violated, 711 distinct, trace 10 |
| `ForgingProbeIntendedEquivocationBug` | probe | kes 0, dig 0, req 1, scen 1, crash 0, death 0, loss 0 | `ProbeNoIntendedEquivocation` | violated: reachable, 247 distinct, trace 10 |
| `ForgingProbeSecondCertBug` | probe | dig 0, crash 0, death 0, loss 0 | `ProbeNoSecondCert` | violated: reachable, 18,641 distinct, trace 21 |
| `ForgingProbeSwitchBug` | probe | kes 0, dig 0, crash 0, death 0, loss 0 | `ProbeNoSwitch` | violated: reachable, 271 distinct, trace 7 |
| `ForgingProbeZombieBug` | probe | kes 0, dig 0, crash 0, death 0 | `ProbeNoZombie` | violated: reachable, 1,501 distinct, trace 12 |
| `ForgingProbeRetireBug` | probe | kes 0, dig 0, crash 0, death 0, loss 0 | `ProbeNoRetire` | violated: reachable, 211 distinct, trace 3 |
| `ForgingProbeLatePodBug` | probe | two reconcilers, kes 0, dig 0, crash 0, death 0, loss 0 | `ProbeNoLatePod` | violated: reachable, 1,420 distinct, trace 10 |

The probes show that the passing configs do exercise their hard cases: two
forgers inside a scenario, a second opcert on chain, a completed switch, a
lost node holding the fence, a retirement, and a pod created after
retirement.

## What it decides

1. **Per-driver StatefulSets cannot be fenced by the operator.** With one
   reconciler the engine's dark spell hides the gap, which is why it survives
   testing. A leader handover exposes it. One name per pool closes it without
   any coordination.
2. **The operator needs no live read for safety.** `ForgingOverlap` runs two
   reconcilers on arbitrarily stale snapshots, one of which never has to
   agree with the other, and every invariant holds. The API server's answer
   to the create, the finalizer and the Secret do the work. A reconciler that
   never refreshes costs liveness only (`ForgingGenerationRaceBug`).
3. **The engine does need one.** Its pod list before an issue, a retirement
   and a scenario close must come from the API server, after the Secret is
   blank.
4. **A switch does not need a new opcert.** `ForgingSwitchInPlace` changes
   the implementation in a live spec with one write and keeps the opcert, and
   every invariant holds. The plan's "stop, confirm, issue, start" is safe too
   and is what the other configs check. Node 21 has to serialise the switch
   either way.
5. **The counter comes from the chain and from nowhere else.** The engine's
   own record is wrong after one crash.

## Operator fencing contract

Rules for the developers of the operator, its image roll and the engine's
opcert issuer. Each names the config that fails without it.

### Acquiring the fence

1. A pool's keyed workload has **one StatefulSet name and one pod name per
   `PoolID`**. The name does not contain the implementation, the image digest,
   the generation or anything else that changes. Replicas is 1.
   (`ForgingFencePerDriverBug`, `ForgingRollSurgeBug`)
2. **The create is the acquire.** `AlreadyExists` means the fence is held;
   requeue. No list, cache lookup or status field decides whether to create,
   and none may be added "to be safe": a check before a create is the bug.
   (`ForgingCacheFenceBug`)
3. The keyed pod template carries the operator's **finalizer**, so the
   finalizer exists from the pod's first moment. (`ForgingNoFinalizerBug`)
4. The scenario's duplicate has a different, scenario-scoped name and mounts
   **its own Secret**, never the pool's. (`ForgingDuplicateSharedSecretBug`)

### Releasing the fence

5. The finalizer comes off in exactly two cases: the pod's status, as its
   kubelet reported it, shows every container terminated; or the pod's Node
   object no longer exists. `NotReady`, `Unknown` and any timeout are not
   among them. (`ForgingLostIsGoneBug`)
6. The patch that removes the finalizer carries the pod's **UID** as a
   precondition, so it cannot reach a newer pod of the same name.
7. A force delete changes nothing: the finalizer keeps the object. The
   operator never force-deletes a keyed pod and never removes another
   controller's view of it.
8. While a finalizer is held because a node is unreachable, the pool reports
   a condition naming the pod and the node. Someone has to resolve the node;
   the operator must not.

### Order on a roll and on a switch

9. **Image roll**: update the same StatefulSet in place. Delete the pod, wait
   for the pod *object* to be gone (finalizer included), and only then is the
   replacement created, by the StatefulSet controller, under the same name.
   No surge, no second StatefulSet, no `maxSurge`. The Secret is not touched.
   (`ForgingRollSurgeBug`)
10. **Implementation switch**: delete the old driver's StatefulSet, wait for
    its pod object to be gone, create the new driver's StatefulSet under the
    same name. If the create comes early it fails with `AlreadyExists`, which
    is the intended outcome. (`ForgingCreateBeforeDeleteBug`)

### Live reads

11. **Operator**: none for safety. Decisions may come from the informer
    cache; a stale decision is refused by the API server or undone on the
    next reconcile. Reconcile from the newest spec seen and compare
    `observedGeneration`, or the pool flaps. (`ForgingGenerationRaceBug`)
12. **Engine**: two reads are live, never cached.
    - The pod list for the pool (by `PoolID` label, every implementation,
      the duplicate included), read from the API server *after* the Secret is
      blank. (`ForgingEngineCachedListBug`, `ForgingIssueWithoutBlankBug`)
    - The pool's opcert counter: the highest counter in any block of the pool
      the follower has seen, and only once that value is the same in the tip
      view and the K-deep view. While the pod list is empty nothing new is
      forged, so waiting for the two to agree terminates.
      (`ForgingStaleCounterBug`)

### Opcert issuance

13. An opcert is written only while the spec is dark, the Secret is blank and
    the live pod list is empty. In order: spec dark, blank the Secret, list,
    issue, spec live. (`ForgingIssueWithoutBlankBug`)
14. The counter is the chain's highest plus one, or 0 for a pool that has
    never forged. It is not read from the engine's store, the event log or
    the old Secret. (`ForgingDoubleIssueBug`, `ForgingReissueSameCounterBug`)
15. The KES period is the chain's: tip slot divided by `SlotsPerKESPeriod`.
    Not a wall clock and not genesis. (`ForgingKesFromGenesisBug`,
    `ForgingKesFromWallClockBug`)
16. **Idempotence across a restart.** The Secret carries the spec generation
    of the dark spell it was issued for (an annotation). After a restart the
    engine looks at the spec and the Secret only: stamped with the current
    generation and not due for rotation means go live; anything else means
    blank it and start again from rule 13. Issuing twice in one dark spell is
    harmless, because no process could start in between.
    `keys.Store.OpCert(id, kesIndex, counter, kesPeriod)` is a pure function
    of the seed, so use `kesIndex = counter`: the same counter and period then
    give the same bytes.
17. Rotation starts one KES period before expiry, never at expiry, and takes
    the same path as rule 13. (`ForgingRotateAtExpiryBug`)

### Retirement and scenarios

18. **Retire**: spec dark, blank the Secret, list pods live, and only when
    the list is empty submit the retirement certificate. A pod created
    afterwards cannot start. (`ForgingRetireBeforeGoneBug`)
19. **Scenario end**: delete the duplicate's Secret and ContinuumPool, list
    pods live, and log the scenario as over only when no duplicate pod object
    remains. (`ForgingDuplicateOutlivesScenarioBug`)
20. No lifecycle change, rotation or retirement starts while a scenario is on
    or ending.

## Code map

Models cardano-continuum at `72c4fb0`. The operator side is implemented;
the engine's opcert issuer is not. The engine writes a pool's plan (dark or
live) through `steer.PoolPlan` and the pools feed
(`internal/engine/internal/feeds/pools.go:34`, `Pools.Poll`, applied by
`internal/cli/live/livepools.go:63`, `livePools.Sync`), and the only opcert
it makes is counter 0 at genesis (`internal/genesis/genesis.go:126`,
`keys.Store.OpCert` at `internal/keys/keys.go:195`). Rules 12 to 20 and the
engine-issuer tests below are still a contract for code that does not exist.

| Model | Code | What |
|---|---|---|
| the name is the fence (rule 1) | `internal/operator/render/names.go:79` (`Name`), `:92` (`PodName`); replicas 1 in `internal/operator/render/render.go:62` (`Render`) | one name per `PoolID`, whatever the driver or digest |
| `CreateMain` (rule 2: the create is the acquire) | `internal/operator/pool/apply.go:91` (`applier.create`) | `AlreadyExists` is requeued |
| finalizer from the first moment (rule 3) | `internal/operator/render/render.go:62` (`Render`), `internal/operator/render/names.go:34` (`Finalizer`) | on the keyed pod template |
| `CreateDup` (rule 4) | `internal/operator/render/names.go:45` (`RoleDuplicate`), `internal/operator/pool/plan.go:89` (`planner.keys`) | own name; refused when it names the pool's Secret or its Secret is gone |
| `Release`, `LostRelease` (rules 5 to 7) | `internal/operator/fence/release.go:33` (`Releasable`), `internal/operator/fence/fence.go:81` (`Release.Unfence`), `internal/operator/fence/nodes.go:50` (`ReadNode`) | a UID- and resourceVersion-preconditioned patch; the Node is read live |
| `ImageRoll` (rule 9) | `internal/operator/pool/apply.go:125` (`applier.update`), `internal/operator/image/select.go:55` (`NeedsRoll`), `:64` (`Roll`) | an in-place StatefulSet update |
| `DeleteMain` on a switch (rule 10) | `internal/operator/pool/apply.go:209` (`applier.drop`) | foreground deletion: the create under the same name fails until the pod object is gone |
| `Observe` and the stale snapshot (rule 11) | `internal/operator/pool/pool.go:104` (`Pools.Reconcile`), `:139` (`Pools.newest`) | a spec older than the newest generation seen is skipped |
| `Gate`, `Start` (no Secret, no start) | `internal/operator/render/render.go:214` (the `keys-gate` init container) | |
| `Forge` | the node, or `internal/forger` for a rogue pool | uses only certificates the engine issued |
| `Request` | `internal/engine/internal/feeds/pools.go:34` (`Pools.Poll`) | the engine's plan for a pool |
| `Hold`, `BlankSecret`, `CheckPods`, `Issue`, `IssueCrash`, `GoLive`, `Retire`, `ScenarioEnd`, `ScenarioClose`, `EngineCacheCatchUp`, `SeenCatchUp` | not implemented | a contract for the engine's issuer |
| `KubeStep`, `PodDies`, `NodeLost`, `NodeResolved`, `ForceDelete`, `KesTick` | Kubernetes, the kubelet and the chain | not continuum code |

### Drift

Where the model deliberately differs from the code:

| Id | Code | Model | Why | Config |
|---|---|---|---|---|
| F1 | the engine's issuer (rules 12 to 20) does not exist | it is modelled as designed | the model is its contract | `Forging`, the engine Bug configs |
| F2 | a reconciler's informer cache delivers watch events | `Observe` copies the current spec; a stale snapshot is the older value kept | only the order of what a reconciler sees matters | `ForgingOverlap`, `ForgingGenerationRaceBug` |
| F3 | Kubernetes pods, StatefulSets and Secrets | one record per pod, a Secret per pool | the fence depends on names and object lifetimes only | all |
| F4 | KES periods derive from slots | `period` advances by `KesTick`, which waits for a due rotation | the timing assumption in `KesTick`'s comment | all |
| F5 | the deposed leader is any reconciler | `Deposed` names `r2` | breaks symmetry on purpose; only one config deposes | `ForgingGenerationRaceBug`, `ForgingGenerationRaceFixed` |

## Go tests the model implies

Each names the config it comes from. The fake kubelet is a test helper that
sets pod status and honours deletion; envtest runs no kubelet.

### Node 21: continuum-operator (`internal/operator`)

- `TestWorkloadNameIgnoresImplAndDigest`: every driver renders the same
  StatefulSet and pod name for one `PoolID`, at any digest (rule 1).
- `TestKeyedPodTemplateHasFinalizer`: every driver's forging template carries
  the finalizer; relay templates do not (rule 3).
- `TestCreateIsTheFence`: with a keyed pod present and an empty cache, the
  reconciler's create fails with `AlreadyExists` and it requeues; no second
  pod appears (`ForgingCacheFenceBug`).
- `TestSwitchImplSerial`: after `Impl` changes, the new StatefulSet exists
  only after the old pod object is gone (`ForgingCreateBeforeDeleteBug`).
- `TestSwitchWaitsForStuckTermination`: the old pod stays terminating (fake
  kubelet withholds the status); no new keyed pod is created
  (`ForgingProbeZombieBug`).
- `TestFinalizerHeldOnNotReadyNode`: Node `NotReady`, pod deleted with grace
  0: the object stays and the fence condition names the node
  (`ForgingLostIsGoneBug`, `ForgingNoFinalizerBug`).
- `TestFinalizerReleasedOnTerminatedStatus` and
  `TestFinalizerReleasedOnNodeDeleted`: the two release cases (rule 5).
- `TestFinalizerPatchHasUIDPrecondition`: a release decided for an old UID
  does not touch a new pod of the same name (rule 6).
- `TestTwoReconcilersOneForger`: two managers without leader election, one
  fed a delayed cache, through go-live, switch and go-dark: never two keyed
  pods (`ForgingOverlap`, `ForgingFencePerDriverBug`).
- `TestStaleSpecDoesNotFlap`: an older spec delivered after a newer one is
  dropped by generation (`ForgingGenerationRaceBug`).
- `TestDuplicateUsesOwnSecret`: the duplicate's pod mounts the duplicate's
  `KeysSecret` and has a name distinct from the pool's (rule 4).
- `TestDuplicatePendingWithoutSecret`: a duplicate created after its Secret
  was deleted never becomes ready and is deleted on the next reconcile
  (`ForgingDuplicateSharedSecretBug`).
- `TestLatePodAfterRetireCannotStart`: a keyed pod created for a retired pool
  stays pending (blank Secret) and is deleted (`ForgingProbeLatePodBug`).

### Image rolls (`internal/operator/image`, 2.16a)

- `TestRollIsInPlace`: a new digest changes the image of the same
  StatefulSet; name and selector are unchanged (`ForgingRollSurgeBug`).
- `TestRollWaitsForPodGone`: during a roll the new-digest pod is created only
  after the old pod object is gone, with termination held open by the fake
  kubelet (`ForgingRollSurgeBug`).
- `TestRollOnLostNodeHolds`: a roll whose old pod is on a `NotReady` node
  does not proceed and reports the fence condition (`ForgingLostIsGoneBug`).
- `TestRollKeepsSecret`: the Secret's resourceVersion is the same before and
  after a roll, and the new pod mounts the same opcert (rule 9).
- `TestRollDuringScenario`: rolling either pod of a scenario pair never gives
  a third keyed pod (`DuplicateScoped`).

### Engine opcert issuer and pool lifecycle

- `TestIssueCounterFromChain` (table, fake chain view): never forged gives
  0; highest forged n gives n + 1; a second issue with no block between gives
  the same number, not n + 2 (`ForgingDoubleIssueBug`).
- `TestIssueWaitsForSettledCounter`: tip view and K-deep view disagree: no
  issue until they agree (`ForgingStaleCounterBug`).
- `TestIssueOrder`: against a fake API server the calls are spec dark, Secret
  blank, pod list, Secret write, spec live, in that order
  (`ForgingIssueWithoutBlankBug`).
- `TestIssueUsesLivePodList`: the list goes to the API reader, not the cache,
  and a pod created after the blank blocks the issue
  (`ForgingEngineCachedListBug`).
- `TestIssueResumesAfterCrash`: a failpoint after each of the five writes;
  the restarted engine ends with one opcert on chain per counter
  (`ForgingReissueSameCounterBug`, `Forging`).
- `TestSecretStampedWithGeneration`: the Secret's annotation equals the dark
  spell's generation; a stale stamp is blanked, a current one is kept
  (rule 16).
- `TestOpCertDeterministic`: `OpCert` with `kesIndex = counter` returns the
  same bytes for the same counter and period (rule 16).
- `TestKesPeriodFromTipSlot`: the period is tip slot over
  `SlotsPerKESPeriod`, also when the wall clock disagrees
  (`ForgingKesFromGenesisBug`, `ForgingKesFromWallClockBug`).
- `TestRotationStartsOnePeriodEarly`: rotation is due at
  `kesPeriod + MaxKESEvolutions - 1` (`ForgingRotateAtExpiryBug`).
- `TestRetireAfterPodsGone`: the retirement certificate is submitted only
  after a blank Secret and an empty live list (`ForgingRetireBeforeGoneBug`).
- `TestScenarioCloseAfterDuplicateGone`: the scenario-end event is logged
  only after the duplicate's pod object is gone
  (`ForgingDuplicateOutlivesScenarioBug`).
- `TestNoLifecycleDuringScenario`: a request made during a scenario is
  applied after it closes (rule 20).
- e2e `TestSwitchKeepsCounterValid`: dingo to cardano-node on kind; the first
  block after the switch is accepted and its counter is the chain's or one
  above (`SwitchCarriesCounter`).

## Reading a formula literally

The "Literally" comments use the house readings listed in
`models/specs/engine-recovery/README.md`.

## Running it

```sh
make models                                    # every model
cd models && scripts/run-model.sh specs/pool-forging
```

The whole directory takes about three minutes on an idle laptop. The largest
config is `ForgingOverlap`, at about 341 thousand distinct states.
