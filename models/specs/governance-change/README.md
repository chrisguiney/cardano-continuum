# governance-change: the proposer's parameter-change chain

The network proposer walks a list of parameter changes: change one, and once
the votes enact it, revert it, naming the change as its predecessor
(`prevGovActionId`). Conway accepts a parameter change only when its
predecessor is the purpose root (the last enacted change) or a live proposal
(`Proposals.hs`, `proposalsAddAction`; gouroboros
`UtxoValidateProposalAncestry`). The engine never sees an enactment: the chain
it follows carries proposals and votes, not the ledger's ratification. So it
has to infer the root, and a wrong inference makes every later change name a
predecessor the ledger refuses.

`ParamChange.tla` checks the four rules the engine relies on for that:

1. **One change open at a time.** The proposer builds a change only with none
   of its own in flight and none in the view still open for votes.
2. **The view repeats the ledger's ratification** at each epoch boundary,
   from the votes it tracked and the voting power at that boundary, and the
   proposer names the last change the view enacted as the next one's
   predecessor. Judging from the power at the time the proposer builds
   instead let stake moving after the boundary turn an enacted change into
   a failed one, or the reverse, and the next change named a predecessor
   the ledger refused.
3. **An enacted change leaves the open list** at the boundary that enacted
   it, so no honest voter votes on it once cardano-node has removed it
   (`GovActionsDoNotExist`).
4. **A vote in a change's last epoch has a TTL that ends inside that
   epoch.** One landing after the boundary is refused with
   `VotingOnExpiredGovAction`; before this rule the engine's honest votes hit
   that rejection.

Run it with `make models` (all models) or
`models/scripts/run-model.sh specs/governance-change` from `models/`.

## What the model contains

- **Epochs**: the ledger and the view share one epoch counter. A change
  proposed in epoch e is open for votes until `e + Life`.
- **The proposer**: builds up to `MaxProps` changes, each naming the change it
  believes enacted last (`Believe`), or none.
- **Voters**: three, each with a weight (voting power). Each decides once per
  open change, yes or no. DReps and committee seats are folded into one
  class with a two-thirds threshold.
- **The node**: lands a built transaction in any epoch up to its TTL. A
  proposal with an unacceptable predecessor, a vote on an expired change, or
  a vote on a change removed after its enactment is refused and recorded in
  `errs`.
- **The ledger at each boundary**: in id order, every live change naming the
  root whose yes votes pass is enacted and becomes the root, as the fake
  chain does. Then a live change whose last voting epoch has passed expires.
  An enacted change is removed at the boundary after: cardano-node ratifies
  from one boundary's snapshot and enacts and removes at the next, so a vote
  landing in between is accepted. The fake chain enacts at the first and
  removes at the second (`prune`).
- **The view at each boundary** (`vdone`): the same ratification from the
  same votes and power. That the view sees all the voting stake is an
  assumption, true on a devnet whose DRep stake is all the run's own.
- **Voting power moves** (`MaxMoves`): any payment or vote delegation changes
  a DRep's power. The ledger and the view tally at the boundary.

## Not modelled

- When the parameters take effect. The real ledger enacts at the boundary
  after ratifying; the model, like the fake, moves the root at the first.
  A change naming a ratified but not yet enacted change is accepted either
  way, since that change is still live.
- Removal of orphans and siblings on enactment (real ledger). With one change
  open at a time no change has a sibling.
- Thresholds per parameter group, pool votes on security-group changes,
  active and inactive DReps, deposits. The thresholds are one rule applied
  identically by both sides; how the engine prices deposits is a builder
  concern with its own Go tests.
- Other action types. Voters never favour no-confidence, new-constitution
  or committee-update actions, so the only purpose root that moves is the
  parameter-change root.
- Rollbacks and resubmission. A proposal is one intent; the planner
  (`planner-reservations`) and retry machine (`submit-retry`) guarantee that
  at most one attempt of it lands.
- The DRep setup steps (register DRep, register the stake key, delegate the
  vote to itself, then the fitted clocks). Each step is chosen from the
  view (`internal/engine/internal/pace`), and a step is not built beside
  one of the same kind in flight (`Book.Has` in `pace.Pop.Decide`), the
  same guard as rule 1.
  A step replayed after a rollback is refused with an expected class
  (`DRepAlreadyRegistered` in the intent spec's `again`). There is no
  interleaving left for a model to explore that the planner model does not
  already cover, so the Go tests (`internal/agenda`, `internal/engine`
  `gov_test.go`) carry it.

## Invariants

| Invariant | Says | Broken by |
|---|---|---|
| `TypeOK` | every variable has its shape | - |
| `OneOpen` | at most one change is open in the view or in flight | `ParamNoFlightGuardBug` |
| `RootAgrees` | once no change is open in the view, the change the engine believes enacted last is the ledger's root | `ParamPowerMovesBug` |
| `PrevAccepted` | no proposal is refused for its predecessor | `ParamProposeWhileOpenBug` |
| `VotesInTime` | no vote lands after its change's last voting epoch | `ParamVoteTTLBug` |
| `VotesFindAction` | no vote names a change the ledger no longer has | `ParamRealVoteOnEnactedBug` |

## Configs and state counts

All configs: three voters, `MaxEpoch = 4`, `Life = 1`, `MaxProps = 3` and
power that never moves, unless noted. TLC 2.19, `-workers auto`.

| Config | Changes | Expected | TLC reported |
|---|---|---|---|
| `Param` | none | all hold | 1,146,033 distinct, depth 29 |
| `ParamLife2` | `Life = 2`, `MaxEpoch = 5` | all hold | 2,945,786 distinct, depth 30 |
| `ParamPowerMoves` | weights 1 or 2, one move | all hold | 4,819,182 distinct, depth 30 |
| `ParamVoteTTLBug` | a last-epoch vote keeps the usual TTL | `VotesInTime` | violated, trace 7 |
| `ParamNoFlightGuardBug` | builds beside its own change in flight; `MaxProps = 2` | `OneOpen` | violated, trace 3 |
| `ParamProposeWhileOpenBug` | builds while a change is open in the view | `PrevAccepted` | violated, trace 10 |
| `ParamPowerMovesBug` | judges from the power of now (`TallyNow`); weights 1 or 2, one move | `RootAgrees` | violated, trace 9 |
| `ParamRealVoteOnEnactedBug` | keeps an enacted change open (`VoteOnEnacted`); `Life = 2`, `MaxEpoch = 5` | `VotesFindAction` | violated, trace 11 |
| `ParamProbeRevertBug` | probe | `ProbeNoRevert` | violated: reachable, trace 16 |
| `ParamProbeEarlyEnactBug` | probe | `ProbeNoEarlyEnact` | violated: reachable, trace 8 |
| `ParamProbeLastEpochVoteBug` | probe | `ProbeNoLastEpochVote` | violated: reachable, trace 7 |
| `ParamProbeLagVoteBug` | probe | `ProbeNoLagVote` | violated: reachable, trace 12 |

The probes show the passing configs reach their hard cases: a revert enacted
on top of an enacted change, a change enacted before its last voting epoch,
a vote landing in its change's last epoch, and a vote landing on an enacted
change before the ledger removes it.

## What it decides

Rules 1 to 4 are each necessary. Without the in-flight guard two changes
name the same predecessor (`ParamNoFlightGuardBug`); the view still follows
the ledger's root, but cardano-node drops the loser as a sibling when the
other is enacted, deposit and all. Without the open-change guard the next
change names one the ledger never enacted and is refused
(`ParamProposeWhileOpenBug`). Judging enactment from the power at build
time, as the proposer once did, lets a payment or vote delegation after the
boundary flip the judgement, and every later change names the wrong
predecessor (`ParamPowerMovesBug`); a tally near the threshold exposes it.
Keeping an enacted change open sends honest votes the node refuses with
`GovActionsDoNotExist` once it removed the change; with a lifetime of one
epoch the removal and the end of voting coincide, so it takes two
(`ParamRealVoteOnEnactedBug`). Without the shortened TTL a last-epoch vote
is refused (`ParamVoteTTLBug`, the `VotingOnExpiredGovAction` defect).

## Code map

| Model | Code |
|---|---|
| `Propose` | `buildPropose` in `internal/engine/internal/intent/spec.go`; the plan is `planParams` over `paramLinks` in `internal/engine/internal/intent/params.go`, which returns `errParamOpen` while a link is open |
| in-flight guard (`PropInFlight`) | `pace.Pop.Decide` in `internal/engine/internal/pace/pop.go`, asking `Env.Flight.InFlight`, which is `pay.Book.Has` (`internal/engine/internal/pay/book.go`) through the engine's hooks (`internal/engine/state.go`) |
| `Open` | `paramLinks`: closed once `ProposalState.Enacted` or `Expires < View.Epoch()`; the voters' side is `chain.View.OpenProposals` (`internal/chain/view.go`, `gov.open` in `internal/chain/gov.go`, which leaves out enacted changes) |
| `Believe`, `Judged` | `planParams` folding `paramPlan.enacted` over the links the view enacted |
| `vdone` | `ratify` in `internal/chain/ratify.go`, run by `View.Apply` when `epochs.next` closes an epoch, before the block's transactions; it marks the change enacted with `gov.enact` (`gov.go`). The predecessor and group reach it through `wire.ProposalDigest` and `window.viewProposal` |
| `Pass` | `passes` (`ratify.go`) over `chain.CountVotes` (`view.go`), with power from `View.VotingPower` at the boundary; the threshold by `govGroup` |
| `Vote` | DReps: `drepScores` in `internal/agenda/civic.go`; committee seats: `committeeDue` and `committeeVote` in `pace/pop.go`. Deciding once is `gov.Voted` and `gov.CommitteeVoted` (`internal/chain/gov.go`) |
| last-epoch TTL | the `closing: lastEpoch` field of the vote kinds in `intent/spec.go`, which makes `Maker.Attempt` ask `Maker.TTL` (`intent/make.go`) for a TTL that ends with the epoch |
| `LandProp`, `PrevOk` | gouroboros `conway.UtxoValidateProposalAncestry` (v0.220.1), with the fake's root from `newLedgerView` in `internal/fakechain/world.go` |
| `LandVote` | gouroboros `VotingOnExpiredGovAction` and `UnknownGovActionIds` rules, classed in `internal/fakechain/rules.go`; removal is `prune` in `internal/fakechain/epoch.go`, run before the block's transactions are chosen (`producer.produce`) |
| `Tick` (`Ratify`, `Expire`) | `ratify` and `accepted` in `internal/fakechain/ratify.go`; the last voting epoch is `effectsOf` in `internal/fakechain/world.go` |
| `Move` | any settled payment or `IntentDelegateDRep`, through the view's stake |

The Go tests covering the same rules are in `internal/engine/internal/intent`
(`params_test.go`, `TestPlanParamsIgnoresStakeMovedAfterEnactment` for
`ParamPowerMovesBug`), `internal/chain/gov_test.go`
(`TestEnactedParamChangeLeavesOpenList` for `ParamRealVoteOnEnactedBug`),
`internal/engine/gov_test.go`, and the fake chain's
`TestParameterChangeEnactsThenReverts` and
`TestEnactedChangeRemovedAtNextBoundary` (`internal/fakechain/epoch_test.go`).

## Reading a formula literally

The "In English" comments use the house readings from
`specs/engine-recovery/README.md`.
