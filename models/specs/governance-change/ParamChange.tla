---------------------------- MODULE ParamChange ----------------------------
(***************************************************************************)
(* The network proposer's parameter-change chain, and the votes on it.     *)
(*                                                                         *)
(* Conway accepts a parameter change only when its predecessor            *)
(* (prevGovActionId) is the purpose root, the last enacted change, or a    *)
(* live proposal (Proposals.hs, proposalsAddAction). The engine never     *)
(* sees an enactment: the chain it follows carries proposals and votes     *)
(* only. So                                                                *)
(*   1. the proposer keeps ONE CHANGE OPEN AT A TIME: it proposes only    *)
(*      with no proposal of its own in flight (the book's pending         *)
(*      intents) and none in the view still open for votes (planParams);  *)
(*   2. the view REPEATS THE LEDGER'S RATIFICATION at each boundary, from *)
(*      the votes it tracked and the voting power AT THAT BOUNDARY        *)
(*      (chain ratify), and the proposer names the last change the view   *)
(*      enacted as the next one's predecessor;                            *)
(*   3. a change the view enacted LEAVES THE OPEN LIST at that boundary,  *)
(*      so no voter votes on it once the ledger has removed it            *)
(*      (GovActionsDoNotExist);                                           *)
(*   4. a vote on a proposal in its LAST EPOCH has a TTL that ends inside *)
(*      that epoch, since one landing after the boundary is refused       *)
(*      (VotingOnExpiredGovAction).                                       *)
(*                                                                         *)
(* The ledger ratifies at each epoch boundary every live change naming    *)
(* the root whose yes votes reach the threshold, enacts it there (as the   *)
(* fake chain does), and removes it at the boundary after: cardano-node   *)
(* ratifies from one boundary's snapshot and enacts and removes at the    *)
(* next, so a vote landing in between is accepted. It expires a change    *)
(* whose last voting epoch has passed. Voting power is a weight per       *)
(* voter; it may move (MaxMoves), which is how stake changes between the  *)
(* boundary the ledger tallies at and the moment the engine plans.       *)
(*                                                                         *)
(* Each *Bug.cfg replaces one rule with a simpler one, or denies a state  *)
(* that must be reachable (a probe), and names the invariant that breaks. *)
(*                                                                         *)
(* Models cardano-continuum at 243b05a; README.md has the code map and    *)
(* the drift table.                                                        *)
(*   Propose  internal/engine/internal/intent/spec.go:207   buildPropose   *)
(*            internal/engine/internal/intent/params.go:106 planParams     *)
(*            internal/engine/internal/pace/pop.go:353      Pop.Decide     *)
(*   Vote     internal/agenda/civic.go:71                   drepScores     *)
(*            internal/engine/internal/pace/pop.go:213      committeeVote  *)
(*            internal/engine/internal/intent/make.go:100   Maker.TTL      *)
(*   Land     internal/fakechain/rules.go:114               gouroboros     *)
(*            internal/fakechain/epoch.go:103               prune          *)
(*   Tick     internal/fakechain/ratify.go:51               ratify (node)  *)
(*            internal/chain/ratify.go:50                   ratify (view)  *)
(*   Move     payments and vote delegations, through the view's stake     *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see the "Reading a formula literally" table   *)
(* in models/specs/engine-recovery/README.md.                              *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets, TLC

CONSTANTS
    Voters,     \* DReps (and committee seats): who votes on a change
    Weights,    \* the voting power one voter may hold
    MaxEpoch,   \* the last epoch the run reaches (bound)
    Life,       \* govActionLifetime: a change made in e is open to e + Life
    MaxProps,   \* how many parameter changes the proposer submits (bound)
    MaxMoves,   \* how many times voting power moves (bound)
    Mutation    \* "none", or the rule replaced; see the *Bug.cfg files

ASSUME Life >= 1 /\ MaxProps >= 1 /\ MaxEpoch >= 1

\* Ledger states of a landed change.
\*   live:     open for votes until its last epoch, may be ratified
\*   enacted:  ratified and enacted; it is the root or an ancestor of it,
\*             and is removed at the boundary after the one that enacted it
\*   expired:  its last voting epoch passed without ratification
PStates == {"live", "enacted", "expired"}
\* The rejections the model can produce. Each is an honest transaction the
\* ledger refused, which the oracle files as a finding against the engine.
Errors == {"InvalidPrevGovActionId", "VotingOnExpiredGovAction",
           "GovActionsDoNotExist"}

VARIABLES
    epoch,   \* the current epoch, the same for the ledger and the view
    props,   \* the landed changes, in landing order; a change's id is its
             \* index, and 0 stands for "no predecessor"
    yes,     \* yes[i]: the voters whose yes vote on change i landed
    cast,    \* cast[i]: the voters who decided on change i (in flight or
             \* landed, yes or no): a voter decides once per change
    root,    \* the ledger's purpose root: the last change it enacted
    w,       \* w[v]: voter v's voting power now
    pend,    \* the transactions built and not yet landed or expired
    nsub,    \* how many proposals the proposer has built
    moves,   \* how many times voting power moved
    errs,    \* the rejections so far
    lastv,   \* a vote has landed in its change's last voting epoch
    lagv,    \* a vote has landed on a change enacted, not yet removed
    vdone    \* the changes the view's own ratification enacted

vars == <<epoch, props, yes, cast, root, w, pend, nsub, moves, errs, lastv, lagv, vdone>>

Ids == 1..MaxProps
Epochs == 0..(MaxEpoch + Life + 1)
PropTx == [k : {"prop"}, n : Ids, prev : 0..MaxProps, ttl : Epochs]
VoteTx == [k : {"vote"}, p : Ids, v : Voters, yes : BOOLEAN, ttl : Epochs]

TypeOK ==
    /\ epoch \in 0..MaxEpoch
    /\ Len(props) <= MaxProps
    /\ \A i \in DOMAIN props :
         props[i] \in [prev : 0..MaxProps, exp : Epochs, st : PStates, at : Epochs]
    /\ yes \in [Ids -> SUBSET Voters]
    /\ cast \in [Ids -> SUBSET Voters]
    /\ root \in 0..MaxProps
    /\ w \in [Voters -> Weights]
    /\ pend \subseteq (PropTx \cup VoteTx)
    /\ nsub \in 0..MaxProps
    /\ moves \in 0..MaxMoves
    /\ errs \subseteq Errors
    /\ lastv \in BOOLEAN
    /\ lagv \in BOOLEAN
    /\ vdone \subseteq Ids

-----------------------------------------------------------------------------
\* Helpers.

\* In English: "The voting power of the voters in S now." The CHOOSE only
\* picks the next voter to add to a sum, so its value does not depend on
\* which voter it picks; that keeps SYMMETRY VoterSym sound.
RECURSIVE Power(_)
Power(S) == IF S = {} THEN 0
            ELSE LET v == CHOOSE x \in S : TRUE IN w[v] + Power(S \ {v})
\* In English: "Yes votes S pass: at least two thirds of all voting power,
\* an unvoted voter counting against (Ratify.hs dRepAcceptedRatio). The
\* ledger and the view's ratification use the same rule."
Pass(S) == 3 * Power(S) >= 2 * Power(Voters)

\* In English: "Change i is open in the view: its last voting epoch has not
\* passed and the view has not enacted it (gov.open; paramLinks closes a
\* link on either). Mutation VoteOnEnacted keeps an enacted change open
\* to its last epoch."
Open(i) == /\ props[i].exp >= epoch
           /\ Mutation = "VoteOnEnacted" \/ i \notin vdone
\* In English: "The engine judges change i enacted: the view's
\* ratification enacted it. Mutation TallyNow judges from the votes
\* weighed with the power of now instead."
Judged(i) == IF Mutation = "TallyNow" THEN Pass(yes[i]) ELSE i \in vdone
\* In English: "The change the engine believes enacted last, judged on
\* the first n changes: the last one judged enacted (planParams folding
\* the links, in proposal order)."
RECURSIVE Believe(_)
Believe(n) == IF n = 0 THEN 0
              ELSE IF Judged(n) THEN n ELSE Believe(n - 1)
\* In English: "A proposal of the proposer's is in flight."
PropInFlight == \E t \in pend : t.k = "prop"

\* In English: "The ledger accepts predecessor p: the root, or a live
\* change (gouroboros UtxoValidateProposalAncestry)."
PrevOk(p) == p = root \/ (p # 0 /\ props[p].st = "live")

\* In English: "The ledger at the boundary into epoch e: in id order, every
\* live change naming the root (as it is after the earlier ones) whose
\* votes pass is enacted and becomes the root; then every live change whose
\* last voting epoch is before e expires. Returns <<props, root>>."
RECURSIVE Ratify(_, _, _, _)
Ratify(i, ps, r, e) ==
    IF i > Len(ps) THEN <<ps, r>>
    ELSE IF ps[i].st = "live" /\ ps[i].prev = r /\ ps[i].exp + 1 >= e
            /\ Pass(yes[i])
         THEN Ratify(i + 1, [ps EXCEPT ![i].st = "enacted", ![i].at = e], i, e)
         ELSE Ratify(i + 1, ps, r, e)
\* Mutation NoExpire never expires a change.
Expire(ps, e) ==
    [i \in DOMAIN ps |->
        IF Mutation # "NoExpire" /\ ps[i].st = "live" /\ ps[i].exp < e
        THEN [ps[i] EXCEPT !.st = "expired"] ELSE ps[i]]

-----------------------------------------------------------------------------
\* The proposer.

\* In English: "The proposer builds its next change: nothing of its own in
\* flight (Book.Has, checked in pace.Pop.Decide), no change open in the
\* view (planParams), and the predecessor the change it believes enacted
\* last. Mutation NoFlightGuard drops the first check, ProposeWhileOpen
\* the second; WaitEnacted also waits for every earlier change to be
\* enacted, so one that expires stops the chain of changes."
Propose ==
    /\ nsub < MaxProps
    /\ Mutation = "NoFlightGuard" \/ ~PropInFlight
    /\ Mutation = "ProposeWhileOpen" \/ \A i \in DOMAIN props : ~Open(i)
    /\ Mutation # "WaitEnacted" \/ \A i \in DOMAIN props : props[i].st = "enacted"
    /\ pend' = pend \cup {[k |-> "prop", n |-> nsub + 1,
                           prev |-> Believe(Len(props)), ttl |-> epoch + 1]}
    /\ nsub' = nsub + 1
    /\ UNCHANGED <<epoch, props, yes, cast, root, w, moves, errs, lastv, lagv, vdone>>

\* In English: "Voter v decides on change i, open in the view, that it has
\* not decided on yet. In i's last epoch the TTL ends inside the epoch
\* (intent spec lastEpoch, Maker.TTL); otherwise it may run into the next.
\* Mutation LateVoteTTL never shortens it."
Vote(v, i) ==
    /\ i \in DOMAIN props
    /\ Open(i)
    /\ v \notin cast[i]
    /\ \E c \in BOOLEAN :
         pend' = pend \cup {[k |-> "vote", p |-> i, v |-> v, yes |-> c,
                             ttl |-> IF Mutation # "LateVoteTTL" /\ props[i].exp <= epoch
                                     THEN epoch ELSE epoch + 1]}
    /\ cast' = [cast EXCEPT ![i] = @ \cup {v}]
    /\ UNCHANGED <<epoch, props, yes, root, w, nsub, moves, errs, lastv, lagv, vdone>>

-----------------------------------------------------------------------------
\* The node.

\* In English: "A proposal lands in a block: it is added if its predecessor
\* is acceptable, and is refused otherwise."
LandProp(t) ==
    /\ IF PrevOk(t.prev)
       THEN /\ props' = Append(props, [prev |-> t.prev, exp |-> epoch + Life,
                                       st |-> "live", at |-> 0])
            /\ UNCHANGED errs
       ELSE /\ errs' = errs \cup {"InvalidPrevGovActionId"}
            /\ UNCHANGED props
    /\ UNCHANGED <<yes, lastv, lagv>>

\* In English: "A vote lands in a block: refused when the change's last
\* voting epoch has passed, or when the change was enacted at an earlier
\* boundary than this epoch's and so is removed; counted otherwise."
LandVote(t) ==
    LET p == props[t.p] IN
    /\ IF p.exp < epoch
       THEN /\ errs' = errs \cup {"VotingOnExpiredGovAction"}
            /\ UNCHANGED <<yes, lastv, lagv>>
       ELSE IF p.st = "enacted" /\ p.at < epoch
       THEN /\ errs' = errs \cup {"GovActionsDoNotExist"}
            /\ UNCHANGED <<yes, lastv, lagv>>
       ELSE /\ yes' = IF t.yes THEN [yes EXCEPT ![t.p] = @ \cup {t.v}] ELSE yes
            /\ lastv' = (lastv \/ p.exp = epoch)
            /\ lagv' = (lagv \/ p.st = "enacted")
            /\ UNCHANGED errs
    /\ UNCHANGED props

Land(t) ==
    /\ t \in pend
    /\ epoch <= t.ttl
    /\ pend' = pend \ {t}
    /\ IF t.k = "prop" THEN LandProp(t) ELSE LandVote(t)
    /\ UNCHANGED <<epoch, cast, root, w, nsub, moves, vdone>>

\* In English: "The epoch turns: the ledger ratifies and enacts, then
\* expires; transactions whose TTL has passed can never land. The view
\* ratifies at the same boundary from the same votes and the same power
\* (its first block of the epoch, before the block's transactions), so
\* it enacts what the ledger enacts. That it sees all the stake is an
\* assumption: on a devnet the run's own actors hold all of it."
Tick ==
    /\ epoch < MaxEpoch
    /\ LET r == Ratify(1, props, root, epoch + 1) IN
       /\ props' = Expire(r[1], epoch + 1)
       /\ root' = r[2]
       /\ vdone' = {i \in DOMAIN r[1] : r[1][i].st = "enacted"}
    /\ epoch' = epoch + 1
    /\ pend' = {t \in pend : t.ttl >= epoch + 1}
    /\ UNCHANGED <<yes, cast, w, nsub, moves, errs, lastv, lagv>>

\* In English: "Voting power moves: a wallet pays or redelegates, and some
\* voter's weight changes."
Move ==
    /\ moves < MaxMoves
    /\ \E v \in Voters, x \in Weights :
         /\ x # w[v]
         /\ w' = [w EXCEPT ![v] = x]
    /\ moves' = moves + 1
    /\ UNCHANGED <<epoch, props, yes, cast, root, pend, nsub, errs, lastv, lagv, vdone>>

Init ==
    /\ epoch = 0
    /\ props = <<>>
    /\ yes = [i \in Ids |-> {}]
    /\ cast = [i \in Ids |-> {}]
    /\ root = 0
    /\ w = [v \in Voters |-> CHOOSE x \in Weights : \A y \in Weights : x <= y]
    /\ pend = {}
    /\ nsub = 0
    /\ moves = 0
    /\ errs = {}
    /\ lastv = FALSE
    /\ lagv = FALSE
    /\ vdone = {}

\* In English: "The run is over: it has reached its last epoch. Before
\* that, Tick is always possible, so this is the only state the model
\* means to end in; any other state with no step is a stuck proposer or
\* node, which TLC's deadlock check reports."
Done ==
    /\ epoch = MaxEpoch
    /\ UNCHANGED vars

Next ==
    \/ Propose
    \/ \E v \in Voters, i \in Ids : Vote(v, i)
    \/ \E t \in pend : Land(t)
    \/ Tick
    \/ Move
    \/ Done

Spec == Init /\ [][Next]_vars

\* In English: "The same behaviours, where the epoch keeps turning, a
\* proposal the proposer may build is built, and a transaction that can
\* land does. Votes get no fairness: a voter may never vote, which is how a
\* change expires."
LiveSpec ==
    /\ Spec
    /\ WF_vars(Tick)
    /\ WF_vars(Propose)
    /\ WF_vars(\E t \in pend : Land(t))

\* In English: "The voters are interchangeable: they are model values,
\* nothing names, orders or compares them, and the one CHOOSE over them
\* (Power) is order-independent. For the safety configs only: TLC's
\* liveness check is unsound under symmetry."
VoterSym == Permutations(Voters)

-----------------------------------------------------------------------------
\* Invariants.

\* In English: "At most one change is open in the view or in flight: never
\* two changes competing for one predecessor."
OneOpen ==
    Cardinality({i \in DOMAIN props : Open(i)})
      + Cardinality({t \in pend : t.k = "prop"}) <= 1
\* In English: "Once no change is open in the view, the change the engine
\* believes enacted last is the ledger's root."
RootAgrees == (\A i \in DOMAIN props : ~Open(i)) => Believe(Len(props)) = root
\* In English: "No proposal is refused for its predecessor."
PrevAccepted == "InvalidPrevGovActionId" \notin errs
\* In English: "No vote lands after its change's last voting epoch."
VotesInTime == "VotingOnExpiredGovAction" \notin errs
\* In English: "No vote names a change the ledger no longer has."
VotesFindAction == "GovActionsDoNotExist" \notin errs

-----------------------------------------------------------------------------
\* Liveness (LiveSpec). The run is bounded by MaxEpoch, so each property
\* speaks only of what can still happen before the run ends.

\* In English: "A landed change whose last voting epoch ends inside the run
\* is eventually enacted or expired: none stays open for good."
\* (IF, not /\: TLC's liveness checker rewrites the formula and does not
\* keep a conjunction's short circuit, so props[i] must be guarded by IF.)
Closes(i) == IF i \in DOMAIN props THEN props[i].exp < MaxEpoch ELSE FALSE
Decided(i) == IF i \in DOMAIN props THEN props[i].st # "live" ELSE FALSE
Settles == \A i \in Ids : Closes(i) ~> Decided(i)

\* In English: "The proposer is free to build its next change (changes
\* left, nothing of its own in flight, nothing open in the view)."
Free(k) ==
    /\ nsub = k /\ k < MaxProps
    /\ ~PropInFlight
    /\ \A i \in DOMAIN props : ~Open(i)

\* In English: "Once the proposer is free to build its next change, it
\* builds it."
ProposerMoves == \A k \in 0..(MaxProps - 1) : Free(k) ~> nsub > k

-----------------------------------------------------------------------------
\* Probes. Each says a state is unreachable; its *Bug.cfg must find it.

\* In English: "No change naming a predecessor (a revert) is ever enacted."
ProbeNoRevert == \A i \in DOMAIN props : ~(props[i].st = "enacted" /\ props[i].prev # 0)
\* In English: "No change is enacted while the view still has it open."
ProbeNoEarlyEnact == \A i \in DOMAIN props : ~(props[i].st = "enacted" /\ props[i].at <= props[i].exp)
\* In English: "No vote lands in its change's last voting epoch."
ProbeNoLastEpochVote == ~lastv
\* In English: "No vote lands on a change between its enactment and its
\* removal."
ProbeNoLagVote == ~lagv
=============================================================================
