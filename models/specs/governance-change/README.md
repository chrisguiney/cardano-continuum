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

`ParamChange.tla` checks the three rules the engine relies on for that:

1. **One change open at a time.** The proposer builds a change only with none
   of its own in flight and none in the view still open for votes.
2. **Enactment is judged from the votes**, once a change's last voting epoch
   has passed: the last change whose tracked votes pass is the root.
3. **A vote in a change's last epoch has a TTL that ends inside that
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
  proposal with an unacceptable predecessor, or a vote on an expired (or, on
  the real ledger, enacted) change, is refused and recorded in `errs`.
- **The ledger at each boundary**: in id order, every live change naming the
  root whose yes votes pass is enacted and becomes the root, as the fake
  chain does. Then a live change whose last voting epoch has passed expires.
- **Voting power moves** (`MaxMoves`): any payment or vote delegation changes
  a DRep's power. The ledger tallies at the boundary; the engine tallies when
  it next builds.
- **`Ledger`**: `"fake"` keeps enacted changes in the proposal map, so votes
  on them land; `"real"` drops them, as cardano-node does.

## Not modelled

- The real ledger's lag between ratification and enactment: it ratifies
  during epoch e + 1 from the votes at the boundary into e + 1, and enacts at
  the boundary into e + 2. The engine judges a change in the epoch after its
  last voting epoch, when a ratified change is not yet enacted but is still
  live, so naming it as predecessor is accepted either way. The model enacts
  at the boundary, as the fake does.
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
  vote to itself, then the fitted clocks). Each step is a function of the
  view (`pace.Pop.setup`), and a step is not built beside one of the same
  kind in flight (`Book.Has` in `pace.Pop.Decide`), the same guard as rule 1.
  A step replayed after a rollback is refused with an expected class
  (`DRepAlreadyRegistered` in the intent spec's `again`). There is no
  interleaving left for a model to explore that the planner model does not
  already cover, so the Go tests (`internal/agenda`, `internal/engine`
  `gov_test.go`) carry it.

## Invariants

| Invariant | Says | Broken by |
|---|---|---|
| `TypeOK` | every variable has its shape | - |
| `OneOpen` | at most one change is open in the view or in flight | (the guard itself; `ParamNoFlightGuardBug` and `ParamProposeWhileOpenBug` break it first) |
| `RootAgrees` | once no change is open in the view, the change the engine believes enacted last is the ledger's root | `ParamNoFlightGuardBug`, `ParamPowerMovesBug` |
| `PrevAccepted` | no proposal is refused for its predecessor | `ParamProposeWhileOpenBug` |
| `VotesInTime` | no vote lands after its change's last voting epoch | `ParamVoteTTLBug` |
| `VotesFindAction` | no vote names a change the ledger no longer has | `ParamRealVoteOnEnactedBug` |

## Configs and state counts

All configs: three voters, `MaxEpoch = 4`, `Life = 1`, `MaxProps = 3`, the
fake ledger and power that never moves, unless noted. TLC 2.19, `-workers
auto`.

| Config | Changes | Expected | TLC reported |
|---|---|---|---|
| `Param` | none | all hold | 321,726 distinct, 794,053 generated, depth 29 |
| `ParamLife2` | `Life = 2`, `MaxEpoch = 5` | all hold | 159,963 distinct, depth 23 |
| `ParamReal` | real ledger | all but `VotesFindAction` hold | 355,777 distinct, depth 29 |
| `ParamVoteTTLBug` | a last-epoch vote keeps the usual TTL | `VotesInTime` | violated, trace 7 |
| `ParamNoFlightGuardBug` | builds beside its own change in flight; `MaxProps = 2` | `RootAgrees` | violated, trace 15 |
| `ParamProposeWhileOpenBug` | builds while a change is open in the view | `PrevAccepted` | violated, trace 11 |
| `ParamPowerMovesBug` | weights 1 or 2, one move | `RootAgrees` | violated, trace 10 (open, see below) |
| `ParamRealVoteOnEnactedBug` | real ledger | `VotesFindAction` | violated, trace 10 (open, see below) |
| `ParamProbeRevertBug` | probe | `ProbeNoRevert` | violated: reachable, trace 17 |
| `ParamProbeEarlyEnactBug` | probe | `ProbeNoEarlyEnact` | violated: reachable, trace 8 |
| `ParamProbeLastEpochVoteBug` | probe | `ProbeNoLastEpochVote` | violated: reachable, trace 6 |

The probes show the passing configs reach their hard cases: a revert enacted
on top of an enacted change, a change enacted while the view still has it
open (the proposer must wait rather than infer), and a vote landing in its
change's last epoch.

## What it decides

Rules 1 to 3 are each necessary. Without the in-flight guard two changes name
the same predecessor, the ledger enacts one and the engine believes the other
(`ParamNoFlightGuardBug`). Without the open-change guard the same happens one
step later and the next change is refused (`ParamProposeWhileOpenBug`).
Without the shortened TTL a last-epoch vote is refused
(`ParamVoteTTLBug`, the `VotingOnExpiredGovAction` defect).

Two configs fail against the code as it is, and stay as known gaps:

- **`ParamPowerMovesBug`.** `paramPassed` tallies with `chain.CountVotes`,
  which weighs each DRep by its voting power when the proposer builds, not at
  the boundary the ledger tallied at. A payment or vote delegation between
  that boundary and the build can turn a change the ledger enacted into one
  the engine believes failed, or the reverse, and every later change names
  the wrong predecessor. The fix is to judge from the tally as of the
  change's last boundary, or to read the root from the node where one is
  available. A large yes margin hides it; a tally near the threshold
  exposes it.
- **`ParamRealVoteOnEnactedBug`.** The view keeps a change open until its last
  voting epoch, and the fake chain keeps enacted changes in its proposal map.
  On cardano-node an enacted change is gone, so an honest vote on a change
  enacted early is refused with `GovActionsDoNotExist`. Against the fake chain
  (`Param`) this cannot happen.

## Code map

| Model | Code |
|---|---|
| `Propose` | `buildPropose` in `internal/engine/internal/intent/spec.go`; the plan is `planParams` over `paramLinks` in `internal/engine/internal/intent/params.go`, which returns `errParamOpen` while a link is open |
| in-flight guard (`PropInFlight`) | `pace.Pop.Decide` in `internal/engine/internal/pace/pop.go`, asking `Env.Flight.InFlight`, which is `pay.Book.Has` (`internal/engine/internal/pay/book.go`) through the engine's hooks (`internal/engine/state.go`) |
| `Open` | `paramLinks`: closed once `Expires < View.Epoch()`; the voters' side is `chain.View.OpenProposals` (`internal/chain/view.go`, `gov.open` in `internal/chain/gov.go`) |
| `Believe` | `planParams` folding `paramPlan.enacted` over the closed links |
| `Pass` | `paramPassed` (`params.go`) over `chain.CountVotes` (`internal/chain/view.go`), with power from `View.VotingPower` |
| `Vote` | DReps: `drepScores` in `internal/agenda/civic.go`; committee seats: `committeeDue` and `committeeVote` in `pace/pop.go`. Deciding once is `gov.Voted` and `gov.CommitteeVoted` (`internal/chain/gov.go`) |
| last-epoch TTL | the `closing: lastEpoch` field of the vote kinds in `intent/spec.go`, which makes `Maker.Attempt` ask `Maker.TTL` (`intent/make.go`) for a TTL that ends with the epoch |
| `LandProp`, `PrevOk` | gouroboros `conway.UtxoValidateProposalAncestry` (v0.220.1), with the fake's root from `newLedgerView` in `internal/fakechain/world.go` |
| `LandVote` | gouroboros `VotingOnExpiredGovAction` and `UnknownGovActionIds` rules, classed in `internal/fakechain/rules.go` |
| `Tick` (`Ratify`, `Expire`) | `ratify` and `accepted` in `internal/fakechain/ratify.go`; the last voting epoch is `effectsOf` in `internal/fakechain/world.go` |
| `Move` | any settled payment or `IntentDelegateDRep`, through the view's stake |

The Go tests covering the same rules are in `internal/engine/internal/intent`
(`params_test.go`), `internal/engine/gov_test.go` and the fake chain's
`TestParameterChangeEnactsThenReverts` (`internal/fakechain/epoch_test.go`).

## Reading a formula literally

The "In English" comments use the house readings from
`specs/engine-recovery/README.md`.
