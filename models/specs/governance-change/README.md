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
| `Settles` (liveness) | a landed change whose last voting epoch ends inside the run is eventually enacted or expired | `ParamNoExpireLiveBug` |
| `ProposerMoves` (liveness) | once the proposer is free to build its next change (changes left, nothing in flight, nothing open), it builds it | `ParamWaitEnactedLiveBug` |

TLC's deadlock check is on: the run ends in `Done`, at `epoch = MaxEpoch`.
Before that `Tick` is always possible, so the check finds no deadlock; it
would report a stuck proposer or node if a change made one.

The liveness properties hold under `LiveSpec`: weak fairness on `Tick`,
`Propose` and landing a transaction, none on votes (a voter may never vote,
which is how a change expires). The run is bounded by `MaxEpoch`, so they
establish only what can still happen inside it: a change open past the last
epoch is not required to settle, and the proposer is required to build its
next change only while it has changes left. They do not establish that the
voters ever pass anything.

## Configs and state counts

All configs: three voters, `MaxEpoch = 4`, `Life = 1`, `MaxProps = 3` and
power that never moves, unless noted. TLC 2.19, `-workers auto`.

| Config | Changes | Expected | TLC reported |
|---|---|---|---|
| `Param` | none | all hold | 1,146,033 distinct, depth 29 |
| `ParamLife2` | `Life = 2`, `MaxEpoch = 5` | all hold | 2,945,786 distinct, depth 30 |
| `ParamPowerMoves` | weights 1 or 2, one move | all hold | 4,819,182 distinct, depth 30 |
| `ParamNoFlightGuardFixed` | `ParamNoFlightGuardBug`'s bounds, the guard in place | all hold | 89,993 distinct |
| `ParamLive` | fair (`LiveSpec`), `MaxEpoch = 3`, `MaxProps = 2` | `Settles`, `ProposerMoves` hold | 43,095 distinct |
| `ParamNoExpireLiveBug` | the ledger never expires a change (`NoExpire`); `ParamLive`'s bounds | `Settles` | violated |
| `ParamWaitEnactedLiveBug` | the proposer waits for every earlier change to be enacted (`WaitEnacted`); `ParamLive`'s bounds | `ProposerMoves` | violated |
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

## Defects: model first

The two governance defects this model found (the proposer judging enactment
from the power at build time, and votes on a change the node had removed)
were reproduced here first, as `ParamPowerMovesBug` and
`ParamRealVoteOnEnactedBug`, before the code changed. Two things went less
well, and the next defect follows the order in `models/README.md` instead:

- A passing config (`ParamReal`, since removed) left out `VotesFindAction`,
  which it was known to violate. A passing config never omits a violated
  invariant; such a config is a Bug config.
- The model followed the fix instead of proposing it: no Fixed config
  modelled the proposed rule before the Go change, the Go tests landed in
  the fix commits with nothing showing them fail first, and the model was
  rewritten to the fixed rules afterwards (the old rules became the
  `TallyNow` and `VoteOnEnacted` mutations).

For a future defect: a `<Name>Bug.cfg` that fails on the property the
defect breaks; a `<Name>Fixed.cfg` at the same bounds modelling the
proposed rule, passing; a Go test that fails for the reason TLC printed;
then the fix. A review finding on the fix adds a Bug config.

## Code map

Models cardano-continuum at `72c4fb0`.

| Model | Code | What |
|---|---|---|
| `Propose` | `internal/engine/internal/intent/spec.go:207` (`buildPropose`); `internal/engine/internal/intent/params.go:106` (`planParams`), `:132` (`paramLinks`), `:127` (`errParamOpen`) | the plan returns `errParamOpen` while a link is open |
| in-flight guard (`PropInFlight`) | `internal/engine/internal/pace/pop.go:353` (`Pop.Decide`) asking `Env.Flight.InFlight`, which is `internal/engine/internal/pay/book.go:86` (`Book.Has`) through the engine's hooks (`internal/engine/state.go`) | |
| `Open` | `internal/engine/internal/intent/params.go:132` (`paramLinks`: closed once enacted or `Expires < View.Epoch()`); voters' side `internal/chain/view.go:235` (`View.OpenProposals`), `internal/chain/gov.go:377` (`gov.open`) | enacted changes leave the open list |
| `Believe`, `Judged` | `internal/engine/internal/intent/params.go:106` (`planParams` folding `paramPlan.enacted`) | |
| `vdone` (the view's ratification in `Tick`) | `internal/chain/ratify.go:50` (`ratify`), run by `internal/chain/view.go:326` (`View.Apply`) when an epoch closes, before the block's transactions; `internal/chain/gov.go:306` (`gov.enact`) | |
| `Pass` | `internal/chain/ratify.go:70` (`passes`) over `internal/chain/view.go:277` (`CountVotes`), power from `internal/chain/view.go:243` (`View.VotingPower`) at the boundary | threshold by `govGroup` |
| `Vote` | DReps `internal/agenda/civic.go:71` (`drepScores`); committee `internal/engine/internal/pace/pop.go:177` (`committeeDue`), `:213` (`committeeVote`); once per change `internal/chain/gov.go:389` (`gov.Voted`), `:277` (`gov.CommitteeVoted`) | |
| last-epoch TTL | `internal/engine/internal/intent/spec.go:117-118` (`closing: lastEpoch`), `internal/engine/internal/intent/make.go:48` (`Maker.Attempt`), `:100` (`Maker.TTL`) | a TTL that ends with the epoch |
| `LandProp`, `PrevOk` | gouroboros `conway.UtxoValidateProposalAncestry` (v0.220.1); the fake's root from `internal/fakechain/world.go:78` (`newLedgerView`) | not continuum code |
| `LandVote` | gouroboros rules classed in `internal/fakechain/rules.go:114-115`; removal `internal/fakechain/epoch.go:103` (`prune`), run before the block's transactions are chosen (`internal/fakechain/producer.go:36`, `producer.produce`) | |
| `Tick` (`Ratify`, `Expire`) | `internal/fakechain/ratify.go:51` (`ratify`), `:79` (`accepted`); last voting epoch `internal/fakechain/world.go:109` (`effectsOf`) | |
| `Move` | any settled payment or `IntentDelegateDRep` (`internal/engine/internal/intent/spec.go:105`), through the view's stake | |
| `Done` | the end of the bounded run | not code |

### Drift

Where the model deliberately differs from the code (from "Not modelled" and
"What the model contains"):

| Id | Code | Model | Why | Config |
|---|---|---|---|---|
| G1 | the real ledger enacts at the boundary after ratifying | the root moves at the ratifying boundary, as the fake chain does | a change naming a ratified, not yet enacted change is accepted either way | all |
| G2 | DReps and committee seats vote with separate thresholds per parameter group | one class of voters, two-thirds threshold | both sides apply the same rule | all |
| G3 | the view tallies the stake it sees | the view sees all the voting stake | true on a devnet whose DRep stake is all the run's own | all |
| G4 | votes and proposals are transactions the planner and retry machine submit | one transaction per intent, landing once up to its TTL | `planner-reservations` and `submit-retry` show at most one attempt lands | all |
| G5 | stake moves through payments and vote delegations | `Move` changes one voter's weight, at most `MaxMoves` times | only the weight at a boundary matters | `ParamPowerMoves`, `ParamPowerMovesBug` |
| G6 | orphans and siblings are removed on enactment | not modelled | with one change open at a time no change has a sibling | - |

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
