# submit-retry

An actor pays once per intent. Its transaction may carry mistakes the
mistake injector put there. The submitter sends it; a node accepts it,
rejects it, or does not answer; the actor waits, resubmits the same bytes,
or reads the rejection, decides what it got wrong, fixes that and builds
again. A low-competence actor decides wrongly. After `Patience`
submissions it gives up. Meanwhile blocks include, bury and roll back its
attempts, and the planner hears about them late. `SubmitRetry.tla` decides
the retry machine's rules before the retry code exists.

Run it with `make models` (all models) or
`models/scripts/run-model.sh specs/submit-retry`.

## The result in one paragraph

The retry machine releases nothing. It only asks the planner, through
`Reserve`, for the next attempt, and calls `Refused` when a node's answer
proves an attempt never reached a mempool. A rejection is that proof only
when the attempt was **never sent before**, or the node named a rule that
does not depend on chain state (fee, missing signer). "Inputs already
spent", whether `BadInputs` or the mempool's `MempoolFailure`, is what a
node answers to a resubmit of bytes already in a block, so after a resubmit
it proves nothing. The decision reads the node's answer, never the actor's
diagnosis. Fixes accumulate, and the submission counter survives rollbacks
and restarts. The oracle expects a **set** of answers: every class the
mistakes cause, in any rule order, plus both "inputs spent" classes
whenever the attempt or a conflicting one may already be at a node. Under
those rules no intent pays twice, every intent ends in done or gave-up,
every attempt ends settled or dead, and the oracle expects every honest
answer. Each simpler rule tried breaks one of these; the table under
[Configs](#configs) lists them.

## How it refines planner-reservations

This model keeps `specs/planner-reservations`' rules and abstracts what that
model already checked:

| planner-reservations | here |
|---|---|
| outputs, inputs, `Excludes` | input **groups**: attempts of an intent in one group share an input, so at most one can be on the chain. `Reserve` puts a new attempt in the group of every unfinished one; a fresh group only when none is unfinished |
| `Reserve`, `Available` | `Build`: refuses after a settled attempt, refuses a group whose input the planner saw spent by somebody else |
| settled K deep, dead | a per-attempt view (`none`, `block`, `deep`); settled at `deep` in the view; dead on settled conflict, settled outside spend, settled expiry, or refusal |
| `DropNotice` is a hint | `Dropped` rebuilds through `Reserve` and releases nothing |
| `Refused` only when the node said no | `Refusal(t, v)` below; `Unreached` never refuses |
| restart resumes at the settled point; unfinished records are sent | `Restart` |
| `DeadIsDead`, `SettledIsFinal`, `IntentAtMostOnce` | the same three, over this model's truth |

Chaining (an attempt spending another's change) and contention between
intents are not modelled here; the planner model covers both, and the
retry machine never chooses inputs itself.

## What is modelled

- **Attempts**: up to `MaxSlots` records per intent. A record whose attempt
  is dead and provably cannot land is reused; nothing reads it again.
- **The node's answer** to a submission: accepted (into the local mempool);
  no answer (`Unreached`: it may or may not have arrived); or a rejection
  naming the first failing rule under any of the six orders of three rules
  (as gouroboros stops at the first), every failing rule (as cardano-node
  reports), or `MempoolFailure` alone when an input is already consumed.
- **Mistakes** as three classes: `Fee` (`FeeTooSmall`) and `Signer`
  (`MissingSigner`), whose rules do not depend on chain state, and `Stale`
  (`BadInputs`), an input spent long ago, which looks exactly like "my own
  attempt is already in a block". The injector picks any set of them per
  intent.
- **The actor**: diagnoses one of the classes named (`Competence = "high"`)
  or any class (`"low"`), fixes it, and builds again through `Reserve`.
  Fixing a class that was not the mistake fixes nothing.
- **The chain**: inclusion of any attempt that reached a node, at any later
  time; burial (K deep); rollback of one unburied block; mempool eviction;
  somebody else spending a group's input K deep; settled expiry of a sent
  attempt.
- **The planner's view**, per attempt, catching up at its own pace.
- **Restarts**: records, counters and fixes are on disk; the view resumes
  at the settled point; the machine's state is rebuilt from the records.
- **Liveness**, under weak fairness of the machine, the planner, burial and
  time. The node need never include anything.

## What is not modelled

- Backoff delays and jitter: `Backoff` is time, and the model has none. A
  backoff is the wait between `Classify` and `Build`.
- Values, fees, change, collateral, chaining, several intents contending
  for one output: `specs/planner-reservations`.
- Mistakes the node accepts (`WrongAddress`, phase-2 `ExUnitsLow`,
  `WrongDatum`) and `DoubleSubmit`: they never enter the retry loop as a
  rejection. `DoubleSubmit` is this model's resubmit.
- Several targets. The model has one aggregated answer (see the contract).
- `OutsideValidityInterval` from a node whose tip passed the TTL before the
  planner's settled tip did: the attempt is then dead by settled expiry
  later, which the model has.
- An `Unknown` class. The oracle must report it, not expect it.
- Learning and frustration: they change probabilities, which TLA+ does not
  have. Low competence here means "any diagnosis is possible".

## Invariants and properties

| Name | Says | Kind |
|---|---|---|
| `TypeOK` | every variable holds the kind of value it should | sanity |
| `IntentAtMostOnce` | the chain never holds two attempts of one intent, even in blocks that may still roll back | truth |
| `DeadIsDead` | an attempt the planner wrote off can truly never land, so releasing it is safe | truth |
| `SettledIsFinal` | a settled attempt is K deep | truth |
| `AttemptsBounded` | no actor submits more than `Patience` times, over rollbacks and restarts | machine |
| `OracleSound` | every answer a node can honestly give to the attempt about to be sent, in any rule order, is in the oracle's expected set | oracle |
| `SlotsSuffice` | the record bound never blocks a build the actor's patience allows | bound |
| `NoRegression` (action) | no attempt is built carrying a mistake the actor has ever fixed | machine |
| `FinalIsFinal` (action) | done and gave-up are never left | machine |
| `Terminates` (liveness) | every intent ends: done or gave up | machine |
| `AttemptsEnd` (liveness) | every attempt ends: settled or dead, including after its actor gave up | planner |

"Truth" invariants compare the records with the chain, which Go cannot
see; they are what the oracle would otherwise blame on the node.

## Configs

Passing configs check everything above. A `*Bug.cfg` checks the one
property it names and must fail.

| Config | Changes | Expected |
|---|---|---|
| `Retry` | nothing: base bounds | pass |
| `RetryHighCompetence` | the actor always names a reported class | pass |
| `RetryRestart` | one restart at any point, no outside spend | pass |
| `RetryTwoIntents` | two intents, one restart; safety only | pass |
| `RetryRebuildOnOwnSpendBug` | any rejection is a refusal, including "inputs spent" to a resubmit | `IntentAtMostOnce` |
| `RetryRefuseOnDiagnosisBug` | the refusal is decided from the actor's diagnosis | `IntentAtMostOnce` |
| `RetryRefuseOnUnreachedBug` | no answer is a refusal | `IntentAtMostOnce` |
| `RetryReleaseOnDropBug` | a drop notice writes the attempt off | `IntentAtMostOnce` |
| `RetryReleaseOnRollbackBug` | a rollback writes the attempt off and rebuilds | `IntentAtMostOnce` |
| `RetryBypassReserveBug` | a rebuild chooses inputs without `Reserve`'s conflict rule | `IntentAtMostOnce` |
| `RetryRestartForgetsSentBug` | after a restart, unfinished attempts count as never sent | `IntentAtMostOnce` |
| `RetryReleaseOnGiveUpBug` | giving up writes every unfinished attempt off | `DeadIsDead` |
| `RetryResetOnRollbackBug` | a rollback resets the submission counter | `AttemptsBounded` |
| `RetryFixUndoesBug` | a fix replaces earlier fixes | `NoRegression` |
| `RetryNoPatienceBug` | no attempt cap | `Terminates` |
| `RetrySingleExpectedVerdictBug` | the oracle expects one class, for the first mistake | `OracleSound` |
| `RetryOracleIgnoresResubmitBug` | the oracle treats a resubmit as a fresh transaction | `OracleSound` |
| `RetryProbe*Bug` (six) | nothing; each denies a state that must be reachable | its `ProbeNo*` |

The probes show the passing configs do not pass for want of states: an
actor gives up; an attempt settles after its actor gave up; a resubmit is
answered `MempoolFailure` while its bytes are in a block; a misdiagnosed
intent is still paid; with two mistakes the node names only the second;
an attempt rebuilt on a fresh input after an outside spend settles.

### What each mutation costs, read from its counterexample

- **Rebuild on own spend.** The first send gets no answer but arrives; a
  block includes it; the planner has not seen the block. The machine sends
  again and the node answers "inputs spent". Written off, the intent has no
  unfinished attempt, `Reserve` allows fresh inputs, and both land.
- **Refuse on diagnosis.** The same order; the low-competence actor reads
  "inputs spent" as a fee error, and a fee error would be proof.
- **Refuse on no answer, release on drop, release on rollback.** Each
  writes off an attempt that is in a block, or will be, and frees the
  rebuild to use fresh inputs.
- **Bypass Reserve.** A drop notice for an attempt in an unseen block, and
  a rebuild on the other input group.
- **Restart forgets sent.** Accepted, included, restart, resent, "inputs
  spent", refused: the rebuild-on-own-spend order through a restart.
- **Release on give-up.** Two sends with no answer, both arrived; the
  actor gives up and the planner writes off an attempt in a mempool.
- **Reset on rollback.** One send, included, rolled back, counter reset,
  three more sends. The plan named this a liveness bug; with a finite
  number of rollbacks a reset is still finite, so it shows as the safety
  bound `AttemptsBounded` instead.
- **Fix undoes.** Two mistakes: fixing the second brings the first back.
  With the cap the intent still ends (gave up), which is why this is a
  safety property, not liveness.
- **No patience.** The counterexample is not a misdiagnosis but the
  plainest loop: accepted, never included, expired, rebuilt. Without a cap
  any retry loop runs forever, because the node need never include
  anything; patience is the only bound on all of them.
- **Single expected verdict, oracle ignores resubmit.** A node checking
  `MissingSigner` first names the second mistake; a node answers
  `MempoolFailure` to bytes already in its mempool.

## Bounds and state counts

Base bounds: one intent, three record slots, `Patience = 3`, low
competence, all three mistake kinds, one rollback, one eviction, one
outside spend, no restart. Intents share nothing in this abstraction, so
one intent is the base and `RetryTwoIntents` shows the product with a
restart.

TLC 2.19 (tla2tools 1.7.4), 14 workers, with another model checker
competing for the machine:

| Config | States generated | Distinct states | Depth | Time |
|---|---|---|---|---|
| `Retry` | 1,109,832 | 383,236 | 25 | 66 s |
| `RetryHighCompetence` | 471,614 | 161,602 | 25 | 13 s |
| `RetryRestart` | 761,172 | 276,956 | 26 | 23 s |
| `RetryTwoIntents` | 83,536,484 | 18,845,236 | 29 | 110-200 s |

The liveness check is most of `Retry`'s time. `RetryTwoIntents` is safety
only and keeps one mistake kind: with two it passed at 128 million states
generated, over four minutes. The whole directory checks in under four
minutes; each `*Bug.cfg` stops at its counterexample within seconds.

## RETRY STATE MACHINE CONTRACT

For `internal/submit` (nodes 32 and 40) and `internal/mistake` (node 39).
Names are suggestions; the rules are not. Where this differs from
IMPLEMENTATION.md 2.9 and 2.10, this wins.

### States and events (node 32, retry core)

```go
type State int // Build, Submit, Pending, Confirmed, Rejected, Done, GaveUp
type Machine struct {
	State    State
	Current  plan.TxID       // the attempt being worked on
	Attempts int             // submissions so far; persisted, never reset
	Fixed    mistake.KindSet // fixes so far; persisted, only grows
}
// Step is pure. It never returns a release: the planner releases.
func Step(m Machine, ev Event, a population.Actor, r *rand.Rand) (Machine, []Action)
// Actions: Build (through plan.Ledger.Reserve), Send{same bytes}, Refused{tx},
// Diagnose, GiveUp, Learn, Frustrate. There is no Release action.
```

| State | Event | Next | Actions |
|---|---|---|---|
| Build | `Attempts < Patience` | Submit | Build via `Reserve`; carry the injected mistakes minus `Fixed` |
| Build, Submit | `Attempts >= Patience` | GaveUp | GiveUp, Frustrate; no release |
| Submit | send | (by outcome) | `Attempts++` on every send, first or again |
| Submit | Accepted | Pending | |
| Submit | Unreached | Submit | Send the same bytes again; never `Refused` |
| Submit | Rejected | Rejected | `Refused` only when `Refusal(sentBefore, failures)` |
| Rejected | diagnosed `d` | Build | `Fixed += FixFor(d)` |
| Pending | the view has an attempt of the intent in a block | Confirmed | none: confirmed is not settled |
| Pending | drop notice | Build | `DropNotice` only; no release |
| Confirmed | the attempt left the view (rolled back) | Submit | same bytes again; `Attempts` unchanged |
| Submit, Pending, Confirmed | the planner reports the current attempt dead | Build | |
| any but GaveUp | an attempt of the intent settled | Done | |
| GaveUp, Done | anything | unchanged | final |

Rules:

1. **The machine releases nothing.** Releases come only from the planner's
   `Advance` (settled or dead) and `Refused`.
2. **Refusal** is decided from the node's answer, never the diagnosis:
   `Refusal(sentBefore, failures) = !sentBefore || any(failures, Intrinsic)`,
   where `Intrinsic` holds classes whose rule reads only the transaction and
   the protocol parameters: `FeeTooSmall`, `MissingSigner` (modelled), and
   on the same argument `WrongNetwork*`, `OutputTooSmall`, `OutputTooBig`,
   `MaxTxSize` and the witness classes (not modelled; add one only if its
   rule reads nothing else). After an earlier send, `BadInputs`,
   `MempoolFailure`, `OutsideValidityInterval`, `ValueNotConserved` and
   `Unknown` are not proof.
   `submit.Outcome.Dead()` (any rejection) is sound only for a first send.
3. **sentBefore** is true once any send of these bytes returned Accepted or
   Unreached, and for every unfinished attempt after a restart.
4. **Several targets**: an attempt is rejected only when every target
   rejected it; one Accepted or Unreached answer makes it sent.
5. **Attempts** counts sends, is persisted with the intent and is never
   reset by a rollback, a drop or a restart.
6. **Give-up is final** and changes no reservation. The attempts stay
   unfinished until the planner settles them (the intent paid late) or
   writes them off (settled expiry, settled conflict).
7. **Restart**: rebuild each machine from the records: an intent with an
   unfinished attempt goes to Submit on the newest, sending the same bytes;
   one without goes to Build; Done and GaveUp stay.

### Mistakes and expected verdicts (node 39)

- `Expected(tx)` is a **set** of classes: one per injected mistake, plus
  {`BadInputs`, `MempoolFailure`} whenever the attempt, or an attempt it
  conflicts with, was sent before, or the harness's own adversary spent one
  of its inputs. An empty mistake set also expects acceptance.
- The oracle passes a rejection when it names at least one class and every
  class it names is in `Expected`. It never requires a particular class:
  rule order differs between implementations. Derive the classes from
  Utxo.tla's families, not a hand-written table.
- Each mutation reports the class it causes (`ClassOf`) and the fix that
  removes it (`FixFor`). A `Stale` mistake causes `BadInputs`, the same
  class as "already spent by my own attempt"; nothing downstream may tell
  them apart by class alone.

### The full machine (node 40)

- `Diagnose(r, failures, competence)` returns one class: one of the
  reported classes when right, any class when wrong. It chooses the fix and
  nothing else.
- A fix is a rebuild: new bytes through `Reserve`, which keeps them in
  conflict with every unfinished attempt, so misdiagnosis cannot pay twice.
- Fixes accumulate. A wrong diagnosis fixes nothing; it never undoes an
  earlier fix.
- The oracle judges each node answer against `Expected` at send time, with
  `sentBefore` as the submitter recorded it.

### What this changes in the plan

- 2.10 `TestGiveUpAtPatience` expects `GaveUp, Release, Frustrate`: there is
  no `Release` (`RetryReleaseOnGiveUpBug`).
- 2.10 `TestRollbackReverifies` "`Rebuild` if inputs are spent": rebuild
  only once the planner reports the attempt dead; until then resubmit the
  same bytes (`RetryReleaseOnRollbackBug`).
- 2.10 lists `Release` among `Step`'s actions: it is not one.
- 2.10 "Dropped is detected by TTL expiry": that is settled expiry, a
  planner death; a drop notice only triggers a rebuild.
- 2.9 `TestDoubleSubmit` "duplicate, no state change": the node answers
  "inputs spent" and the submitter must not refuse the attempt.
- The FORMAL-METHODS plan's `RetryMisdiagnosisLoopBug` is split:
  `RetryNoPatienceBug` (liveness) and `RetryFixUndoesBug` (safety).
  `RetryResetOnRollbackBug` fails `AttemptsBounded`, not liveness.

## Go tests this model asks for

### Retry core (32)

- `TestStepTable`: every (state, event) pair of the table above, illegal pairs returning an error.
- `TestNodeRefusalReleases`: a first-send rejection calls `Refused` exactly once and no other release.
- `TestDropYieldsRebuildNotRelease`: a drop notice yields `Build` through `Reserve`, never `Refused` (`RetryReleaseOnDropBug`).
- `TestRebuildGoesThroughReserve`: a rebuild with an unfinished attempt gets only conflicting inputs and fails closed when `Reserve` refuses (`RetryBypassReserveBug`).
- `TestRolledBackYieldsNoRelease`: a rollback sends the same bytes again and leaves the attempt unfinished (`RetryReleaseOnRollbackBug`).
- `TestRollbackKeepsAttempts`: the attempt counter after a rollback is unchanged (`RetryResetOnRollbackBug`).
- `TestGaveUpWaitsForSettledExpiry`: giving up stops sends and releases nothing; the reservation goes only when the planner reports the attempt dead (`RetryReleaseOnGiveUpBug`).
- `TestGaveUpThenSettledIsPaid`: an attempt settling after give-up marks the intent paid, and the machine stays GaveUp (`RetryProbeLateSettleBug`).
- `TestRestartTreatsUnfinishedAsSent`: after a restart an unfinished attempt is resent as the same bytes and a "inputs spent" answer does not refuse it (`RetryRestartForgetsSentBug`).
- `TestUnreachedIsNotRefusal`: a timeout resends the same bytes and never calls `Refused` (`RetryRefuseOnUnreachedBug`).
- `TestGiveUpAtPatience`: the send after `Patience` is not made; the state is GaveUp with no release.
- `PropMachineTerminates`: random event sequences reach Done or GaveUp within `Patience` sends (`RetryNoPatienceBug`).

### Mistake injector (39)

- `TestExpectedIsSetPerMistake`: a tx with two mistakes expects both classes (`RetrySingleExpectedVerdictBug`).
- `TestExpectedAnyRuleOrder`: for every order of the UTXO rules, the first failure is expected.
- `TestExpectedAfterSendIncludesSpent`: once the attempt or a conflicting one was sent, `BadInputs` and `MempoolFailure` are expected (`RetryOracleIgnoresResubmitBug`).
- `TestStaleAndOwnSpendSameClass`: a `Stale` mutant and a resubmit of an included tx both yield `BadInputs`; the catalog does not distinguish them.
- `TestFixForRemovesClass`: applying `FixFor(ClassOf(k))` to a mutant of kind `k` yields a tx the in-process rules accept.

### Full submit machine (40)

- `TestResubmitMempoolFailureIsNotRefusal`: fake node includes the tx, the resubmit gets `MempoolFailure`, no `Refused`, no fresh inputs (`RetryRebuildOnOwnSpendBug`).
- `TestResubmitBadInputsIsNotRefusal`: the same with `BadInputs`.
- `TestIntrinsicRejectionAfterSendRefuses`: a resend rejected with `FeeTooSmall` calls `Refused`.
- `TestRefusalIgnoresDiagnosis`: with competence 0, a "inputs spent" answer diagnosed as a fee error still does not refuse (`RetryRefuseOnDiagnosisBug`).
- `TestFanoutRejectedOnlyIfAllReject`: one target Accepted or Unreached and one Rejected makes the attempt sent, not refused.
- `TestFixesAccumulate`: two mistakes fixed in turn; the third build carries neither (`RetryFixUndoesBug`).
- `TestWrongFixFixesNothing`: a wrong diagnosis leaves the mistake and every earlier fix in place.
- `TestMisdiagnosisCannotPayTwice`: fake chain, competence 0, random rule order: the chain never holds two attempts of one intent.
- `PropSubmitMatchesModel`: random interleavings of sends, answers, inclusions, rollbacks, drops and restarts against a fake chain keep `IntentAtMostOnce`, `DeadIsDead`, `AttemptsBounded` and `OracleSound`.
