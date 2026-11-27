# engine-recovery: crash, restart, rollback past the cursor, replay

The engine treats the chain as ground truth: actors change only when a
block confirms something. Crashes make that claim harder to keep. A block can be
rolled back up to K deep, so either actor state comes only from K-deep
blocks or every change needs an undo record. Two more risks come from how the
store writes. If the engine persists its event log, its chain cursor and its
actor state in separate writes, a crash between them leaves the cursor ahead
of the state or behind it. On restart, chain-sync `FindIntersect` can answer
with a point *older* than the stored cursor, because the node rolled back
while the engine was down. And `continuum replay` only works if every
nondeterministic input is in the log.

`EngineRecovery.tla` models that at a small scale and lets TLC crash the
engine at every step. It began as a design model for
`internal/eventlog` (2.12), `internal/store` (2.13) and `internal/engine`
(2.22); the [code map](#code-map) pins the commit it now models.

## What the model contains

- **The node**: a chain of blocks. Each block names an actor and the number
  of rollbacks the node made before forging it, so blocks on different forks
  differ. The node extends the chain, or rolls back to a height no lower than
  its immutable height (`imm`, which is K below the longest chain it has
  had).
- **Chain-sync**: the node keeps a read pointer for the engine (`srv`). If
  the pointer is no longer on the chain, the next message is a rollback to
  the latest common point. Otherwise it is the next block.
- **The engine's memory** (`st`, lost on `Crash`): its points (cursor),
  confirmed height `conf`, actor counters `act`, undo depth `ud` and ticks.
  One pure `Step` handles every input. It is the Go `Loop.Handle`, and both the
  live loop and replay use it.
- **The store**: an append-only log `slog` and a checkpoint `ck = [seq, st]`
  (the state and the number of log entries it reflects). `CommitLog` makes
  the pending batch durable. `CommitCkpt` replaces the checkpoint.
  `Restart` loads the checkpoint, replays the log tail after `ck.seq`, runs
  `FindIntersect` and treats the answer as a rollback to `ip`, which it
  logs.
- **Two designs** (`Variant`):
  - `Stable` (design A) counts a block only when it is K deep, and keeps no
    undo.
  - `Undo` (design B) counts every block at once, and keeps undo records for
    the last K blocks.
- **Clock**: `Tick` stands for every wall-clock or timer read that drives a
  decision. It is an input and it is logged.

### Deviations from the plan's outline

- The plan's separate `Fetch` and `Apply` steps are one step here. A crash
  between them loses an inbox entry that was never acted on, which is the
  same as a crash before `Fetch`.
- The plan has one `Commit` step. The model has `CommitLog` and `CommitCkpt`,
  because 2.13 makes the log authoritative and the checkpoint a lagging read
  model rebuilt from the log tail. A single transaction that writes both is
  one interleaving of these two steps. `SplitCommit` splits the checkpoint
  itself into a points write and a separate actor-state write.
- `NoUndo` and the stuck restart are caught by `NeverHalted` (an explicit
  "cannot unwind" flag), not by a deadlock. The run ends in `Done`: the
  engine caught up (`CaughtUp`) or halted. TLC's deadlock check is on, so any
  other state with no step fails the config.
- The plan's bounds were two actors and a chain of up to 4 blocks. At those
  bounds, with 2 rollbacks and 2 crashes, TLC passed 11.6 million distinct
  states without finishing (the log is a history variable). The main configs
  use one actor. The `*TwoActors` configs keep two actors at a 3-block chain
  with one rollback, to check that counters for different actors stay
  separate. `MaxBatch = 1` holds the store writer to one unflushed entry.
  Losing a batch of n entries in a crash is the same as losing them one at a
  time, because a checkpoint waits for the batch either way.

## Not modelled

- Reservations, pending intents and the planner's use of the volatile
  overlay (model `planner-reservations`).
- Submissions in flight at a crash (2.13 risk: they resume as Pending).
- How many intersection points are stored. The model sends all of them to
  `FindIntersect`. Go needs points that reach at least the confirmed point
  (design A) or the bottom of the undo window (design B), or the
  intersection falls back to origin.
- Torn writes inside one transaction (SQLite's job), and the engine's own
  Parquet event tables (failpoint tests, 2.14).
- Deep rollbacks beyond K. The node never makes them, so `NeverHalted` holding
  means no forced re-sync within the security parameter.

## Invariants

| Invariant | Says | Broken by |
| --- | --- | --- |
| `TypeOK` | every variable has its shape | - (a shape check; no mutation is meant to break it) |
| `NeverHalted` | the engine never meets a rollback it cannot unwind | `EngineNoUndoBug` |
| `CursorAgrees` | the engine's points equal the node's read pointer for it | `EngineIntersectAtTipBug` |
| `MemConsistent` | in memory, actors = Derive(confirmed blocks) | `EngineNoUnwindBug` |
| `LogBeforeEffect` | the checkpoint never reflects an entry the durable log lacks | `EngineLogAfterEffectBug` |
| `CheckpointConsistent` | the checkpoint's points and actors were written together | `EngineSplitCommitBug` |
| `ReplayReproduces` | replaying the whole log from nothing = checkpoint + replayed tail | `EngineUnloggedClockBug` |
| `StoreMatchesChain` | once caught up and flushed, what a restart recovers is the chain and Derive of it | `EngineRecoverSkipsTailBug` |
| `StableOnly` (A) | every block the durable or in-memory actors reflect is still on the node's chain | `EngineConfirmAtDepthZeroBug` |
| `UndoCoversWindow` (B) | recovered undo covers every recovered block the node has dropped | `EngineNoUndoWindowBug` |
| `CatchesUp` (liveness) | with fair engine steps, it ends up running, at the tip, flushed and checkpointed | `EngineNoUndoLiveBug` |

## Configs and state counts

All configs use K = 2 and `MaxTicks = 1`. Unless noted, they also use one
actor, `MaxLen = 4`, `MaxRollbacks = 2`, `MaxCrashes = 2` and
`MaxBatch = 1`. TLC 2.19 (tla2tools 1.7.4, as `make models-install` pins), `-workers auto`. A counterexample's length is
given in states. Every config checks for deadlock: the run ends in `Done`
(caught up, or halted). The deadlock check is not decorative: without `Done`,
`EngineStable` deadlocks at the end of its bounded run.

| Config | Variant | Mutation or probe | Expected | TLC reported |
| --- | --- | --- | --- | --- |
| `EngineStable` | A | none | all hold | 437,364 distinct, 1,497,315 generated, depth 37 |
| `EngineUndo` | B | none | all hold | 437,364 distinct, 1,497,315 generated, depth 37 |
| `EngineStableTwoActors` | A | two actors, len 3, 1 rollback | all hold | 201,498 distinct, depth 25 |
| `EngineUndoTwoActors` | B | two actors, len 3, 1 rollback | all hold | 201,498 distinct, depth 25 |
| `EngineStableLive` | A | len 3, 1 rollback, fair | `CatchesUp` holds | 14,205 distinct, depth 25 |
| `EngineUndoLive` | B | len 3, 1 rollback, fair | `CatchesUp` holds | 14,205 distinct, depth 25 |
| `EngineSplitCommitBug` | A | points and actors in two transactions | `CheckpointConsistent` | violated, 3,056 distinct, trace 11 |
| `EngineLogAfterEffectBug` | A | checkpoint before its log batch | `LogBeforeEffect` | violated, trace 4 |
| `EngineNoUndoBug` | B | undo only in memory | `NeverHalted` | violated, trace 7 (rollback, crash, restart) |
| `EngineNoUndoLiveBug` | B | undo only in memory, len 3, fair | `CatchesUp` | violated, 28,178 distinct, lasso of 18 states |
| `EngineIntersectAtTipBug` | A | restart ignores the older intersection | `CursorAgrees` | violated, trace 7 |
| `EngineConfirmAtDepthZeroBug` | A | counts the tip, no undo | `StableOnly` | violated, trace 4 |
| `EngineUnloggedClockBug` | A | tick not logged | `ReplayReproduces` | violated, trace 3 |
| `EngineNoUnwindBug` | B | rollback truncates points, keeps counters | `MemConsistent` | violated |
| `EngineRecoverSkipsTailBug` | A | restart recovers the checkpoint, drops the log tail | `StoreMatchesChain` | violated |
| `EngineNoUndoWindowBug` | B | undo only in memory | `UndoCoversWindow` | violated |
| `EngineProbeOfflineRollbackBug` | A | probe | `ProbeNoOfflineRollback` | violated: reachable, trace 7 |
| `EngineProbeTailReplayBug` | A | probe | `ProbeNoTailReplay` | violated: reachable, trace 4 |
| `EngineUndoProbeDurableUnwindBug` | B | probe | `ProbeNoDurableUnwind` | violated: reachable, trace 8 |

The probes show that the passing configs do exercise their hard cases: a
rollback past the stored cursor while the engine is down, a restart that
replays a log tail, and (design B) a restart that must unwind durable
effects. A and B have the same state counts because the variant changes only
the deterministic content of `st`, not which steps can be taken.

## What it decides

Both designs pass. Design B passes only because its undo window is stored in
the checkpoint (`EngineNoUndoBug`). Design A needs no undo at all, since
`StableOnly` holds at every moment, including while down. That matches the
plan's recommendation: use A, keep durable actor state from K-deep blocks
only, and keep the tip as a volatile overlay.

Every design has to keep three rules:

1. **The log goes first.** An input's log entry is committed (fsynced) before
   any checkpoint that reflects it. The checkpoint records the log seq it
   covers, and recovery is checkpoint plus a replay of the tail. Every
   nondeterministic input is a log record before its effect: blocks,
   rollbacks (including the restart's intersection) and clock reads.
2. **The checkpoint is one transaction.** Cursor/points, actor state and
   (design B) the undo window are written together, never cursor first.
3. **Restart honours the intersection.** The `FindIntersect` answer is a
   rollback to `ip`: log it, unwind, and only then roll forward. Design A
   never has to unwind durable state, because `conf` is a high-water mark at
   least K below the tip.

## Go tests the model implies

The developers of nodes 7, 8 and 13 write these. Each names the config it
comes from.

### Node 7: `internal/eventlog`

- `TestReplayFromCheckpointEqualsFullReplay`: replaying the tail after a
  checkpoint's seq gives the same state as replaying the whole log
  (`ReplayReproduces`).
- `TestRollbackRecordReplays`: a log of forward, rollback, forward replays to
  the live loop's state, including the counters of the rolled-back blocks
  (Undo variant step).
- `TestIntersectionRollbackIsLogged`: a restart whose intersection is below
  the stored tip appends a `RolledBack` input record before any new block.
- `TestClockReadsAreLogged`: every timer or clock read that changes a decision
  appears as a `Tick` input. Replaying with it removed makes `Diff` non-empty
  (`EngineUnloggedClockBug`).
- `TestScanTailFromSeq`: `Scan(from = ckpt.seq+1)` yields exactly the entries
  after the checkpoint, with no gap or overlap (`LogTail`).

### Node 8: `internal/store`

- `TestSaveCheckpointIsAtomic`: a failpoint inside `SaveCheckpoint` leaves
  either the old or the new checkpoint, never new points with old actors
  (`EngineSplitCommitBug`).
- `TestCheckpointSeqNotAheadOfLog`: `SaveCheckpoint` refuses a seq above the
  last durable log seq (`LogBeforeEffect`, `EngineLogAfterEffectBug`).
- `TestCrashBetweenLogAndCheckpoint`: the log is ahead of the checkpoint.
  Recovery replays the tail and equals an uninterrupted run
  (`EngineProbeTailReplayBug` shows the case is reachable).
- `TestCrashAfterEveryWrite`: a failpoint kills after each commit. Every
  recovered state equals a full replay of the durable log
  (`ReplayReproduces`, `CheckpointConsistent`).
- `TestCheckpointKeepsIntersectionPoints`: the stored points reach down to
  the confirmed point, so `FindIntersect` never falls back to origin within
  K.
- `TestCheckpointStoresUndoWindow` (only if design B is chosen): the undo
  records for the last K blocks round-trip through a restart
  (`EngineNoUndoBug`).

### Node 13: `internal/engine`

The pure `Step` side (K-deep confirm, rollback refused below `conf`) is
`internal/engine/internal/window`. The I/O side is
`internal/engine/internal/recovery`: `Open` and `Replay` are `Restart`, and
`Saver.Save` orders `CommitLog` before `CommitCkpt`. `Runner` in
`internal/engine/runner.go` runs `FindIntersect` after the replay.

- `TestRestartAfterOfflineRollback`: an ouroboros-mock chain-sync server rolls
  back past the stored cursor while the engine is down. After restart the
  engine's cursor equals the server's, and its actors equal Derive of the new
  chain (`EngineProbeOfflineRollbackBug`, `StoreMatchesChain`).
- `TestIntersectionHandledAsRollback`: when `FindIntersect` answers below the
  tip, the engine unwinds to it before it applies the next block
  (`CursorAgrees`, `EngineIntersectAtTipBug`).
- `TestActorsChangeOnlyAtDepthK`: a block changes durable actor state only when
  its Kth successor arrives. A rollback of the tip leaves durable actors
  unchanged (`StableOnly`, `EngineConfirmAtDepthZeroBug`).
- `TestRollbackBelowConfirmedIsError`: a rollback below `conf` returns an
  explicit error and never silently resets (`NeverHalted`).
- `TestCheckpointWaitsForLogFlush`: the loop never asks the store to
  checkpoint a state whose inputs are still in the unflushed batch
  (`LogBeforeEffect`).
- `TestCrashInjectionConverges`: with kills after random store writes and a
  bounded number of crashes, the engine ends up at the mock's tip, flushed
  and checkpointed (`CatchesUp`).

## Code map

Models cardano-continuum at `243b05a`. The engine is design A. Each model
action and where the code does it:

| Model | Code | What |
|---|---|---|
| `Fetch` | `internal/engine/runner.go:289` (`Runner.drain`), `internal/eventlog/session.go:25` (`Session.Feed`) | the next chain-sync event becomes a `BlockApplied` or `RolledBack` record, appended before `Engine.Handle` |
| `Step` (one pure handler, live and replay) | `internal/engine/engine.go:296` (`Engine.Handle`) | every input, live or replayed |
| K-deep confirm (`Stable`) | `internal/engine/internal/window/window.go:183` (`Window.Confirm`) | posts at most K volatile blocks |
| rollback below `conf` (`NeverHalted`) | `internal/engine/internal/window/window.go:197` (`Window.Check`), `:53` (`ErrRollbackBelowConfirmed`) | refused with an error, never reset |
| `Tick` (input logged before its effect) | `internal/eventlog/session.go:48` (`Session.Tick`) | append, sync, then `Handle` |
| `CommitLog` | `internal/engine/internal/recovery/saver.go:52` (`Saver.Save`, its `Sync`) | the log is flushed first |
| `CommitCkpt` (`LogBeforeEffect`, `CheckpointConsistent`) | `internal/store/store.go:305` (`DB.SaveCheckpoint`), `:284` (`validate`), `:38` (`ErrAheadOfLog`), `:40` (`ErrPointsShort`) | one transaction, refused when the seq is past the durable log or the points miss the confirmed point |
| `Restart` | `internal/engine/internal/recovery/recovery.go:50` (`Open`), `:104` (`Replay`), then `internal/engine/runner.go:220` (`Runner.intersect`) | checkpoint, log tail, then `FindIntersect` |
| intersection as a rollback (`CursorAgrees`) | `internal/eventlog/session.go:60` (`Session.Intersect`) | an answer below the stored tip is logged as `RolledBack` and fed like any rollback |
| `Crash` | `internal/engine/internal/recovery/saver.go:33` (`Saver.Crash`) at `recovery.go:22-25` (`StageFed`, `StageSynced`, `StageCheckpointed`) | the failpoints a test kills at |
| `NodeExtend`, `NodeRollback` | the node (ouroboros-mock in tests) | not continuum code |

### Drift

Where the model deliberately differs from the code:

| Id | Code | Model | Why | Config |
|---|---|---|---|---|
| E1 | design A only (`Window` confirms K-deep blocks) | both designs, `Variant = "Stable"` or `"Undo"` | B was the rejected alternative; its configs show why it needs the undo window stored | `EngineUndo`, `EngineNoUndoBug` |
| E2 | `Runner.drain` reads an event, then `Session.Feed` appends and applies it | one `Fetch` step | a crash between the read and the append loses an event never acted on, the same as a crash before `Fetch` | all |
| E3 | the log is a file of encoded records, the checkpoint an SQLite row | `slog` a sequence of inputs, `ck` a record | only the order and the covered seq matter to recovery | all |
| E4 | `Session.Feed` appends and syncs each input before `Handle` | `Fetch` and `Tick` apply the input and leave it in `pend`; `CommitLog` flushes it later (`MaxBatch = 1`) | the model allows more than the code (an effect ahead of its flush), so a pass covers the code; `LogBeforeEffect` is what the order must keep | all |
| E5 | actor state is a set of actors with their own state | one counter per actor (`Derive`) | the recovery rules do not depend on what the state holds | all |
| E6 | `FindIntersect` gets the stored points, which must reach the confirmed point | every point is sent | the short-points case is refused by `validate` (`ErrPointsShort`), not modelled | - |

## Reading a formula literally

The "Literally" comments use the house readings from gouroboros-tla-models'
README, for example: `A == e` "A is defined as e", `x'` "x in the next
state", `UNCHANGED x` "x is unchanged", `[r EXCEPT !.f = v]` "r with f set to
v", `SubSeq(s, 1, n)` "the first n elements of s", `s \o t` "s followed by
t", `<>[]P` "eventually always P", `WF_vars(A)` "weak fairness of A".

## Running it

```sh
make models                                    # every model
cd models && scripts/run-model.sh specs/engine-recovery
```

The whole directory takes about a minute on an idle laptop (five minutes
when other TLC jobs share the CPU). The largest config is
`EngineStable` or `EngineUndo`, at about 437 thousand distinct states.
