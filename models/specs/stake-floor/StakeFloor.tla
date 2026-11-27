----------------------------- MODULE StakeFloor -----------------------------
(***************************************************************************)
(* The engine's stake-floor guard.                                        *)
(*                                                                         *)
(* The population manager and the operator agenda decide when a pool goes *)
(* dark, retires, or when a wallet moves its delegation. The chain feels   *)
(* those decisions late: the leader schedule of epoch e is drawn from the  *)
(* stake as it stood at the end of epoch e - 2 (the ledger's "set"         *)
(* snapshot), a retirement takes effect at the boundary of the epoch it    *)
(* names, and the retired pool's stake stays in the leader schedule for    *)
(* two more epochs. A devnet whose forging share of that schedule falls    *)
(* too low can have an epoch with no block, and an epoch with no block     *)
(* stops the ledger's epoch transitions for good (see Frozen).             *)
(*                                                                         *)
(* Leader election is abstracted to one rule: if, all through an epoch,    *)
(* the pools actually forging hold at least Floor percent of the stake in  *)
(* the schedule that epoch uses, the epoch has at least one block, and     *)
(* every transaction submitted in it is in one. Below the floor nothing is *)
(* promised (unless BlocksBelowFloor says otherwise).                      *)
(*                                                                         *)
(* The model answers two questions:                                        *)
(*   1. What must the guard check before it lets a pool go dark, retire,   *)
(*      or a wallet move its stake, so that the floor holds in every epoch *)
(*      without any further engine step? The rule the unmutated model      *)
(*      states: project the three schedules the decision can still reach  *)
(*      (in use now, next epoch, the one after) from confirmed state plus  *)
(*      every transaction in flight plus this one; count as forging only   *)
(*      pools that are registered, planned live, not failed and not        *)
(*      retiring; take the denominator from the projected schedule itself; *)
(*      and require the floor to survive the loss of any FaultMargin       *)
(*      forging pools.                                                     *)
(*   2. When pods die beyond that margin, does the engine's recovery       *)
(*      (bring dark pools live, repair, redelegate away from dead pools)   *)
(*      restore the floor, and within how many epochs?                     *)
(* Each value of Mutation replaces one part of the rule with a simpler one *)
(* a Go guard would plausibly use; a *Bug.cfg shows what it breaks. The    *)
(* README lists them.                                                      *)
(*                                                                         *)
(* Models cardano-continuum at 243b05a; README.md has the code map and    *)
(* the drift table.                                                        *)
(*   Allowed   internal/policy/guard.go:55                Guard.Check      *)
(*   GoDark, Retire, Delegate                                              *)
(*             internal/engine/internal/floor/floor.go:234 Floor.Check     *)
(*   GoLive    internal/engine/internal/floor/floor.go:261 Floor.SetTier   *)
(*   Fail      internal/engine/internal/floor/floor.go:280 Floor.PodStatus *)
(*   Repair    internal/engine/internal/floor/floor.go:292 Floor.Recover   *)
(*   Endangered, RecoveryDone                                              *)
(*             internal/policy/guard.go:204,232  Endangered, Recover       *)
(*   Block, Boundary                                                       *)
(*             internal/chain/ledger.go:68        epochs.next              *)
(*             internal/chain/view.go:320         View.PolicyState         *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see specs/hello/Hello.tla.                     *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    Floor,            \* percent of the schedule's stake that must be forging
    MaxEpoch,         \* the last epoch explored
    RetireMax,        \* a retirement may name an epoch 1..RetireMax ahead (eMax)
    MaxFaults,        \* how many times a pod dies
    FaultMargin,      \* how many forging pools the guard assumes can die at once
    Recovery,         \* TRUE: the engine runs its recovery before an endangered epoch ends
    Repairable,       \* TRUE: the engine can bring a dead pod back (new node, new opcert)
    DarkFirst,        \* TRUE: a pool is dark before its retirement certificate (pool-forging rule 18)
    BlocksBelowFloor, \* TRUE: any forging stake above zero still gives a block per epoch
    RecoverEpochs,    \* the bound RecoverBound checks
    MaxMoves,         \* how many delegation transactions are submitted in all
    MaxDarks,         \* how many times the engine takes a pool dark in all
    Mutation          \* "none", or one deliberate mistake (see the README)

\* Three pools and four wallets. Pool p1 starts with 3 units, p2 with 1 and
\* p3 with 2. At Floor = 70 that lets p2 go dark (5 of 6) but not p3
\* (4 of 6), so every decision is close to the line.
\* Literally: "Pools is defined as the set of "p1", "p2" and "p3"."
Pools == {"p1", "p2", "p3"}
\* Literally: "Wallets is defined as the set of "w1", "w2", "w3" and "w4"."
Wallets == {"w1", "w2", "w3", "w4"}
\* Literally: "Stake is defined as the function giving w1 3 and every other
\*   wallet 1."
Stake == [w \in Wallets |-> IF w = "w1" THEN 3 ELSE 1]
\* Literally: "Deleg0 is defined as the function sending w1 to p1, w2 to p2,
\*   and w3 and w4 to p3."
Deleg0 == [w \in Wallets |-> CASE w = "w1" -> "p1"
                               [] w = "w2" -> "p2"
                               [] OTHER    -> "p3"]

\* Literally: "None is defined as "none"." (a wallet delegated nowhere)
None == "none"
\* Literally: "NoTx is defined as "notx"." (nothing in flight for a wallet)
NoTx == "notx"
\* Literally: "Targets is defined as the union of Pools and the set holding
\*   None."
Targets == Pools \cup {None}

VARIABLES
    \* the ledger (the truth)
    epoch,    \* the current epoch, by slot time
    label,    \* the last epoch whose transition the ledger has applied (nesEL)
    deleg,    \* deleg[w]: w's confirmed delegation, as the ledger has it now
    mark,     \* the delegations as they stood at the last applied transition
    set,      \* the delegations at the transition before that
    reg,      \* reg[p]: "registered", "retiring" or "retired"
    retAt,    \* retAt[p]: the epoch a retiring pool's retirement takes effect
    produced, \* a block has been made in the current epoch
    \* the harness
    plan,     \* plan[p]: the engine's intent, "live" or "dark"
    failed,   \* failed[p]: the pool's pod is dead, whatever the plan says
    nfail,    \* how many pods have died
    cen,      \* cen[p]: the population census's state of the pool's actor
    pend,     \* pend[w]: w's delegation transaction in flight, or NoTx
    pret,     \* pret[p]: the epoch named by p's retirement in flight, or 0
    preg,     \* preg[p]: p's re-registration is in flight
    \* bookkeeping for the properties
    dry,      \* the floor was not met at some point of the current epoch
    dryRun,   \* how many epochs in a row since the latest pod death were dry
    cancels,  \* how many retirements a re-registration has cancelled
    n         \* n.moves, n.darks: the bounded engine steps taken so far

\* Literally: "vars is defined as the tuple of every variable."
vars == <<epoch, label, deleg, mark, set, reg, retAt, produced, plan, failed,
          nfail, cen, pend, pret, preg, dry, dryRun, cancels, n>>

-----------------------------------------------------------------------------
\* Stake arithmetic.

\* In English: "The total stake of the wallets in S."
\* Literally: "SumOver of S is defined as: if S is empty then 0, else,
\*   letting w be some member of S, Stake of w plus SumOver of S without w."
RECURSIVE SumOver(_)
SumOver(S) == IF S = {} THEN 0
              ELSE LET w == CHOOSE x \in S : TRUE IN Stake[w] + SumOver(S \ {w})

\* In English: "The wallets that count in distribution d: those delegated
\* to some pool."
\* Literally: "Active of d is defined as the set of w in Wallets such that
\*   d of w does not equal None."
Active(d) == {w \in Wallets : d[w] # None}

\* In English: "Distribution d meets the floor when the pools in F are the
\* ones forging and the wallets in T make up the denominator. A
\* distribution with no stake at all elects nobody, so it fails."
\* Literally: "Ok of d, F and T is defined as: letting tot be SumOver of T
\*   and fs be SumOver of the w in T with d of w in F: tot is greater than 0
\*   and 100 times fs is at least Floor times tot."
Ok(d, F, T) ==
    LET tot == SumOver(T)
        fs  == SumOver({w \in T : d[w] \in F})
    IN  tot > 0 /\ 100 * fs >= Floor * tot

-----------------------------------------------------------------------------
\* What the chain does.

\* In English: "The pools that really forge now: planned live, pod alive,
\* not retired, and not past the epoch their retirement names (the harness
\* stops a pool at its retirement epoch whatever the ledger has applied)."
\* Literally: "Forging is defined as the set of p in Pools such that plan
\*   of p is "live", failed of p is false, reg of p is not "retired", and it
\*   is not the case that reg of p is "retiring" and retAt of p is at most
\*   epoch."
Forging == {p \in Pools : /\ plan[p] = "live"
                          /\ ~failed[p]
                          /\ reg[p] # "retired"
                          /\ ~(reg[p] = "retiring" /\ retAt[p] <= epoch)}

\* In English: "The ledger skipped an epoch: an epoch passed with no block,
\* so the transition into it never ran, and a block now is more than one
\* epoch past the label. NEWEPOCH only runs for the label's successor, so
\* the ledger never transitions again: no snapshot rotates, no retirement
\* applies, no delegation reaches the schedule. (As dingo's
\* LeaderStakeSnapshotEpoch reads cardano-ledger; not checked against
\* cardano-ledger itself.)"
\* Literally: "Frozen is defined as: epoch is greater than label plus 1."
Frozen == epoch > label + 1

\* In English: "The delegations the leader schedule of the current epoch
\* was drawn from. After the transition into this epoch that is set (the
\* stake at the end of epoch - 2). Before the first block of the epoch the
\* transition has not run yet, but the schedule is already the one it will
\* install: today's mark. A frozen ledger keeps using its set."
\* Literally: "Lead is defined as: if epoch equals label plus 1 then mark,
\*   else set."
Lead == IF epoch = label + 1 THEN mark ELSE set

\* In English: "The forging pools hold at least Floor percent of the stake
\* in the schedule in use."
\* Literally: "FloorOk is defined as Ok of Lead, Forging and Active of
\*   Lead."
FloorOk == Ok(Lead, Forging, Active(Lead))

\* In English: "Some forging pool has stake in the schedule, so a block is
\* at least possible."
\* Literally: "CanBlock is defined as: there exists a w in Wallets with
\*   Lead of w in Forging."
CanBlock == \E w \in Wallets : Lead[w] \in Forging

-----------------------------------------------------------------------------
\* The guard: what the engine checks before it lets a decision through.

\* In English: "The pools the guard counts as forging once registrations
\* r hold: planned live (in plan pl), pod alive, and registered. A pool with
\* a retirement confirmed or in flight is not counted, even if it still
\* forges. Mutations: IgnoreRetiring counts every pool not yet retired;
\* PlanIsForging believes the plan and ignores dead pods."
\* Literally: "GuardForging of pl and r is defined as the set of p in Pools
\*   such that pl of p is "live", Mutation is "PlanIsForging" or failed of p
\*   is false, and: if Mutation is "IgnoreRetiring" then r of p is not
\*   "retired", else r of p is "registered"."
GuardForging(pl, r) ==
    {p \in Pools : /\ pl[p] = "live"
                   /\ (Mutation = "PlanIsForging" \/ ~failed[p])
                   /\ IF Mutation = "IgnoreRetiring" THEN r[p] # "retired"
                                                     ELSE r[p] = "registered"}

\* In English: "The denominator the guard uses for distribution d: every
\* wallet d delegates. Mutation TotalFromCensus leaves out the wallets on
\* pools the census calls retired, as a guard summing 'the stake of live
\* pools' from the population would."
\* Literally: "GuardTotal of d is defined as: if Mutation is
\*   "TotalFromCensus" then the w in Active of d with cen of d of w not
\*   "retired", else Active of d."
GuardTotal(d) ==
    IF Mutation = "TotalFromCensus"
    THEN {w \in Active(d) : cen[d[w]] # "retired"}
    ELSE Active(d)

\* Literally: "Margin is defined as: if Mutation is "NoMargin" then 0,
\*   else FaultMargin."
Margin == IF Mutation = "NoMargin" THEN 0 ELSE FaultMargin

\* In English: "d meets the floor with forging pools F even after any
\* Margin of them die."
\* Literally: "OkWithMargin of d and F is defined as: for every L in the
\*   subsets of F with at most Margin members, Ok of d, F without L, and
\*   GuardTotal of d."
OkWithMargin(d, F) ==
    \A L \in SUBSET F : Cardinality(L) <= Margin => Ok(d, F \ L, GuardTotal(d))

\* In English: "The delegations once the transactions in flight land: pd
\* is the in-flight map including the decision being checked. Mutation
\* IgnoreInflight applies only the decision itself (the entry that differs
\* from pend) to the confirmed delegations."
\* Literally: "AfterPending of pd is defined as the function giving each w:
\*   if pd of w is NoTx, or Mutation is "IgnoreInflight" and pd of w equals
\*   pend of w, then deleg of w, else pd of w."
AfterPending(pd) ==
    [w \in Wallets |->
        IF pd[w] = NoTx \/ (Mutation = "IgnoreInflight" /\ pd[w] = pend[w])
        THEN deleg[w] ELSE pd[w]]

\* In English: "The registrations once the retirements in flight (pr, the
\* decision included) land. A re-registration in flight is not counted: it
\* may not land, and until it does the pool is retiring."
\* Literally: "RegAfter of pr is defined as the function giving each p:
\*   if pr of p is greater than 0 and not (Mutation is "IgnoreInflight" and
\*   pr of p equals pret of p), then "retiring", else reg of p."
RegAfter(pr) ==
    [p \in Pools |->
        IF pr[p] > 0 /\ ~(Mutation = "IgnoreInflight" /\ pr[p] = pret[p])
        THEN "retiring" ELSE reg[p]]

\* In English: "The schedules a decision taken now can still reach, given
\* d2, the delegations after everything in flight lands:
\*   - in this epoch: Lead;
\*   - next epoch: mark if this epoch's transition has run; if it has not,
\*     the transition at the first block will make today's delegations the
\*     mark, so those (a transaction lands only after that transition);
\*   - the epoch after: d2.
\* Every later schedule is d2 minus the stake of pools retired meanwhile,
\* which the guard never counts as forging, so it only gains. A frozen
\* ledger keeps its set for ever.
\* Mutations: GateOnLive checks only d2 (the stake as it is now);
\* ThisEpochOnly only Lead; NextEpochOnly Lead and the next one."
\* Literally: "Horizon of d2 is defined as: letting next be (if epoch
\*   equals label then mark else deleg): if Frozen then the set holding
\*   set; in the case Mutation is "GateOnLive", the set holding d2; in the
\*   case "ThisEpochOnly", the set holding Lead; in the case
\*   "NextEpochOnly", the set of Lead and next; otherwise the set of Lead,
\*   next and d2."
Horizon(d2) ==
    LET next == IF epoch = label THEN mark ELSE deleg
    IN  IF Frozen THEN {set}
        ELSE CASE Mutation = "GateOnLive"    -> {d2}
               [] Mutation = "ThisEpochOnly" -> {Lead}
               [] Mutation = "NextEpochOnly" -> {Lead, next}
               [] OTHER                      -> {Lead, next, d2}

\* In English: "The guard: with plan pl, delegations in flight pd and
\* retirements in flight pr (the decision applied to each), every schedule
\* the decision can reach meets the floor with margin."
\* Literally: "Allowed of pl, pd and pr is defined as: for every d in
\*   Horizon of AfterPending of pd, OkWithMargin of d and GuardForging of
\*   pl and RegAfter of pr."
Allowed(pl, pd, pr) ==
    \A d \in Horizon(AfterPending(pd)) :
        OkWithMargin(d, GuardForging(pl, RegAfter(pr)))

\* In English: "Moving w to pool q only adds forging stake: q is one the
\* guard counts as forging and w now sits on a pool it does not. Such a
\* move needs no guard: the denominator is unchanged and the numerator only
\* grows, in every schedule, with any pools lost."
\* Literally: "Adds of w and q is defined as: letting F be GuardForging of
\*   plan and RegAfter of pret: q is in F, and deleg of w is in Pools
\*   without F."
Adds(w, q) ==
    LET F == GuardForging(plan, RegAfter(pret))
    IN  q \in F /\ deleg[w] \in Pools \ F

-----------------------------------------------------------------------------
\* Start.

\* In English: "Epoch 0, transition applied; every snapshot is the genesis
\* delegation; every pool registered, live and alive; nothing in flight."
\* Literally: "Init is defined as: epoch and label are 0; deleg, mark and
\*   set are Deleg0; every reg is "registered", every retAt 0; produced is
\*   false; every plan "live", every failed false, nfail 0, every cen
\*   "active"; every pend NoTx, every pret 0, every preg false; dry is
\*   whether FloorOk is false; dryRun and cancels are 0."
Init ==
    /\ epoch = 0
    /\ label = 0
    /\ deleg = Deleg0
    /\ mark = Deleg0
    /\ set = Deleg0
    /\ reg = [p \in Pools |-> "registered"]
    /\ retAt = [p \in Pools |-> 0]
    /\ produced = FALSE
    /\ plan = [p \in Pools |-> "live"]
    /\ failed = [p \in Pools |-> FALSE]
    /\ nfail = 0
    /\ cen = [p \in Pools |-> "active"]
    /\ pend = [w \in Wallets |-> NoTx]
    /\ pret = [p \in Pools |-> 0]
    /\ preg = [p \in Pools |-> FALSE]
    /\ dry = ~FloorOk
    /\ dryRun = 0
    /\ cancels = 0
    /\ n = [moves |-> 0, darks |-> 0]

-----------------------------------------------------------------------------
\* The engine. Each step leaves dry to UpdateDry (below).

\* In English: "The engine takes pool p dark (the operator agenda chose it,
\* or the population manager), if the guard allows it. At most MaxDarks
\* times in a run, to bound the state space."
\* Literally: "GoDark of p is defined as: plan of p is "live", n.darks is
\*   less than MaxDarks, Allowed of plan with p dark, pend and pret, plan
\*   in the next state is plan with p "dark", and n.darks goes up by 1;
\*   nothing else changes."
GoDark(p) ==
    /\ plan[p] = "live"
    /\ n.darks < MaxDarks
    /\ Allowed([plan EXCEPT ![p] = "dark"], pend, pret)
    /\ plan' = [plan EXCEPT ![p] = "dark"]
    /\ n' = [n EXCEPT !.darks = @ + 1]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, failed,
                   nfail, cen, pend, pret, preg, dryRun, cancels>>

\* In English: "The engine takes a registered pool with no retirement in
\* flight live. Never guarded: it only adds forging stake."
\* Literally: "GoLive of p is defined as: plan of p is "dark", reg of p is
\*   "registered", pret of p is 0, and plan in the next state is plan with
\*   p "live"; nothing else changes."
GoLive(p) ==
    /\ plan[p] = "dark"
    /\ reg[p] = "registered"
    /\ pret[p] = 0
    /\ plan' = [plan EXCEPT ![p] = "live"]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, failed,
                   nfail, cen, pend, pret, preg, dryRun, cancels, n>>

\* In English: "The engine brings a dead pod back: reschedules it, or
\* issues a new opcert after a KES expiry. Needs no transaction."
\* Literally: "Repair of p is defined as: Repairable, failed of p, and
\*   failed in the next state is failed with p false; nothing else
\*   changes."
Repair(p) ==
    /\ Repairable
    /\ failed[p]
    /\ failed' = [failed EXCEPT ![p] = FALSE]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, plan,
                   nfail, cen, pend, pret, preg, dryRun, cancels, n>>

\* In English: "The engine submits p's retirement certificate naming epoch
\* r. With DarkFirst the pool must already be dark (pool-forging rule 18);
\* without it the pool forges until r. Guarded: it ends the pool's chance
\* of coming back live."
\* Literally: "Retire of p and r is defined as: reg of p is "registered",
\*   pret of p is 0, preg of p is false, DarkFirst implies plan of p is
\*   "dark", r is in epoch plus 1 through epoch plus RetireMax, Allowed of
\*   plan, pend and pret with p set to r, and pret in the next state is
\*   pret with p set to r; nothing else changes."
Retire(p, r) ==
    /\ reg[p] = "registered"
    /\ pret[p] = 0
    /\ ~preg[p]
    /\ DarkFirst => plan[p] = "dark"
    /\ r \in (epoch + 1)..(epoch + RetireMax)
    /\ Allowed(plan, pend, [pret EXCEPT ![p] = r])
    /\ pret' = [pret EXCEPT ![p] = r]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, plan,
                   failed, nfail, cen, pend, preg, dryRun, cancels, n>>

\* In English: "The engine re-registers a retiring pool, which cancels the
\* retirement (the Shelley POOL rule as recalled; unverified). Never
\* guarded: it removes nothing. Bounded to one cancellation per run."
\* Literally: "Reregister of p is defined as: reg of p is "retiring",
\*   cancels is 0, no re-registration is in flight, and preg in the next
\*   state is preg with p true; nothing else changes."
Reregister(p) ==
    /\ reg[p] = "retiring"
    /\ cancels = 0
    /\ \A x \in Pools : ~preg[x]
    /\ preg' = [preg EXCEPT ![p] = TRUE]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, plan,
                   failed, nfail, cen, pend, pret, dryRun, cancels, n>>

\* In English: "Wallet w (its agenda, or the engine's recovery) submits a
\* delegation to q, or q = None to undelegate. The target must not be
\* retired. Guarded unless the move only adds forging stake; mutation
\* UnguardedDelegate never guards it. Moves that are not Adds happen at
\* most MaxMoves times in a run; the recovery's Adds moves are not
\* bounded, so the bound never stops a recovery. Mutation NoMove: the
\* recovery never submits those Adds moves."
\* Literally: "Delegate of w and q is defined as: pend of w is NoTx, q is in
\*   Targets and not deleg of w, q in Pools implies reg of q is not
\*   "retired"; Adds of w and q, or n.moves is less than MaxMoves and
\*   (Mutation is "UnguardedDelegate" or Allowed of plan, pend with w set
\*   to q, and pret); pend in the next state is pend with w set to q, and
\*   n.moves goes up by 1 unless Adds of w and q; nothing else changes."
Delegate(w, q) ==
    /\ pend[w] = NoTx
    /\ q \in Targets \ {deleg[w]}
    /\ q \in Pools => reg[q] # "retired"
    /\ \/ Adds(w, q) /\ Mutation # "NoMove"
       \/ /\ n.moves < MaxMoves
          /\ \/ Mutation = "UnguardedDelegate"
             \/ Allowed(plan, [pend EXCEPT ![w] = q], pret)
    /\ pend' = [pend EXCEPT ![w] = q]
    /\ n' = IF Adds(w, q) THEN n ELSE [n EXCEPT !.moves = @ + 1]
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, plan,
                   failed, nfail, cen, pret, preg, dryRun, cancels>>

-----------------------------------------------------------------------------
\* The environment.

\* In English: "A pod dies: its node is lost, its process crashes for
\* good, or its KES key expires (pool-forging). The pool is dark whatever
\* the plan says. RecoverBound measures from the latest death, so the
\* count of dry epochs starts again."
\* Literally: "Fail of p is defined as: nfail is less than MaxFaults,
\*   failed of p is false, failed in the next state is failed with p true,
\*   nfail goes up by 1 and dryRun becomes 0; nothing else changes."
Fail(p) ==
    /\ nfail < MaxFaults
    /\ ~failed[p]
    /\ failed' = [failed EXCEPT ![p] = TRUE]
    /\ nfail' = nfail + 1
    /\ dryRun' = 0
    /\ UNCHANGED <<epoch, label, deleg, mark, set, reg, retAt, produced, plan,
                   cen, pend, pret, preg, cancels, n>>

\* In English: "A forging pool makes a block. If it is the first block
\* since the epoch began and the ledger is not frozen, the epoch transition
\* runs first: the snapshots rotate (the new mark is the delegations as
\* they stand), and then pools whose retirement epoch has come are retired
\* and their delegators undelegated (SNAP before POOLREAP, so the retired
\* pool's stake is in the new mark). Then every transaction in flight lands
\* in the block, except one that is no longer valid: a delegation to a
\* retired pool, a retirement of a pool no longer registered, a
\* re-registration of a pool no longer retiring. The census follows each
\* confirmation; mutation RetireMarkedEarly marks the actor Retired as
\* soon as the certificate confirms."
\* Literally: see the LET: tick, the rotated snapshots, the reaped pools,
\*   the registrations and delegations after reaping, then after landing.
Block ==
    /\ CanBlock
    /\ LET tick   == epoch = label + 1
           reaped == IF tick THEN {p \in Pools : reg[p] = "retiring" /\ retAt[p] <= epoch}
                     ELSE {}
           reg1   == [p \in Pools |-> IF p \in reaped THEN "retired" ELSE reg[p]]
           d1     == [w \in Wallets |-> IF deleg[w] \in reaped THEN None ELSE deleg[w]]
           landR  == {p \in Pools : pret[p] > epoch /\ reg1[p] = "registered"}
           landG  == {p \in Pools : preg[p] /\ reg1[p] = "retiring"}
       IN  /\ label' = IF tick THEN epoch ELSE label
           /\ mark' = IF tick THEN deleg ELSE mark
           /\ set' = IF tick THEN mark ELSE set
           /\ deleg' = [w \in Wallets |->
                           IF pend[w] = NoTx THEN d1[w]
                           ELSE IF pend[w] = None THEN None
                           ELSE IF reg1[pend[w]] = "retired" THEN d1[w]
                           ELSE pend[w]]
           /\ reg' = [p \in Pools |-> CASE p \in landR -> "retiring"
                                        [] p \in landG -> "registered"
                                        [] OTHER       -> reg1[p]]
           /\ retAt' = [p \in Pools |-> CASE p \in landR -> pret[p]
                                          [] p \in landG -> 0
                                          [] OTHER       -> retAt[p]]
           /\ cen' = [p \in Pools |->
                         CASE p \in landR -> IF Mutation = "RetireMarkedEarly"
                                             THEN "retired" ELSE "retiring"
                           [] p \in landG -> "active"
                           [] p \in reaped -> "retired"
                           [] OTHER        -> cen[p]]
           /\ cancels' = cancels + Cardinality(landG)
    /\ produced' = TRUE
    /\ pend' = [w \in Wallets |-> NoTx]
    /\ pret' = [p \in Pools |-> 0]
    /\ preg' = [p \in Pools |-> FALSE]
    /\ UNCHANGED <<epoch, plan, failed, nfail, dryRun, n>>

\* In English: "The engine has done everything its recovery asks for: every
\* registered pool with no retirement in flight is planned live; with
\* Repairable, no pod is dead (an engine that believes the plan, mutation
\* PlanIsForging, does not know which pods are dead); and every wallet on a pool the guard does not
\* count as forging has a move to one that it does in flight or confirmed
\* (when there is such a pool). A wallet whose earlier transaction is still
\* in flight while no block can be made is excused: the engine cannot
\* replace it, and nothing would land anyway. Mutation NoRedelegate drops
\* the last part."
\* Literally: "RecoveryDone is defined as: letting F be GuardForging of
\*   plan and RegAfter of pret: every p registered with pret 0 has plan
\*   "live"; Repairable and Mutation not "PlanIsForging" imply no p is
\*   failed; and Mutation is
\*   "NoRedelegate", or F is empty, or every w with deleg of w in Pools
\*   without F has pend of w in F, or has something in flight while
\*   CanBlock is false."
RecoveryDone ==
    LET F == GuardForging(plan, RegAfter(pret))
    IN  /\ \A p \in Pools : reg[p] = "registered" /\ pret[p] = 0 => plan[p] = "live"
        /\ Repairable /\ Mutation # "PlanIsForging" => \A p \in Pools : ~failed[p]
        /\ \/ Mutation = "NoRedelegate"
           \/ F = {}
           \/ \A w \in Wallets :
                  deleg[w] \in Pools \ F =>
                      \/ pend[w] \in F
                      \/ pend[w] # NoTx /\ ~CanBlock

\* In English: "The floor is breached now or will be in a schedule already
\* on its way, counting only what really forges and is not retiring, with
\* the transactions in flight landed. This, not the current breach alone,
\* is what starts the engine's recovery: a pod that dies after the
\* epoch's last block may leave this epoch above the floor and the next
\* one below it."
\* Literally: "Endangered is defined as: letting F be the p in Forging
\*   with reg of p "registered", next be (if epoch equals label then mark
\*   else deleg), d2 be deleg with every transaction in flight applied, and
\*   ds be (if Frozen then the set holding set, else the set of Lead, next
\*   and d2): some d in ds fails Ok of d, F and Active of d."
Endangered ==
    LET F    == {p \in Pools : p \in Forging /\ reg[p] = "registered"}
        next == IF epoch = label THEN mark ELSE deleg
        d2   == [w \in Wallets |-> IF pend[w] = NoTx THEN deleg[w] ELSE pend[w]]
        ds   == IF Frozen THEN {set} ELSE {Lead, next, d2}
    IN  \E d \in ds : ~Ok(d, F, Active(d))

\* In English: "The epoch ends (slot time). Two timing assumptions gate
\* it:
\*   - if the epoch was guaranteed a block (the floor held all through it,
\*     or BlocksBelowFloor and some forging stake is in the schedule), it
\*     had one, and every transaction in flight is in a block;
\*   - with Recovery, if the floor is Endangered, the engine has finished
\*     its recovery (it reacts well within an epoch). Mutation
\*     RecoverOnBreach starts recovery only once the schedule in use is
\*     below the floor.
\* Whatever is still in flight expires (its TTL is inside the epoch)."
\* Literally: "Boundary is defined as: epoch is less than MaxEpoch; if dry
\*   is false, or BlocksBelowFloor and CanBlock, then produced and nothing
\*   is in flight; if Recovery and Endangered (FloorOk false, under
\*   RecoverOnBreach) then RecoveryDone; epoch goes up by
\*   1, produced becomes false, nothing is in flight, and dryRun becomes
\*   dryRun plus 1 if dry, else 0; the rest is unchanged."
Boundary ==
    /\ epoch < MaxEpoch
    /\ (~dry \/ (BlocksBelowFloor /\ CanBlock)) =>
           /\ produced
           /\ \A w \in Wallets : pend[w] = NoTx
           /\ \A p \in Pools : pret[p] = 0 /\ ~preg[p]
    /\ Recovery /\ (IF Mutation = "RecoverOnBreach" THEN ~FloorOk ELSE Endangered)
           => RecoveryDone
    /\ epoch' = epoch + 1
    /\ produced' = FALSE
    /\ pend' = [w \in Wallets |-> NoTx]
    /\ pret' = [p \in Pools |-> 0]
    /\ preg' = [p \in Pools |-> FALSE]
    /\ dryRun' = IF dry THEN dryRun + 1 ELSE 0
    /\ UNCHANGED <<label, deleg, mark, set, reg, retAt, plan, failed, nfail,
                   cen, cancels, n>>

\* In English: "dry records whether the floor was missed at any point of
\* the epoch; a boundary starts it afresh from the new epoch's first state."
\* Literally: "UpdateDry is defined as: dry in the next state equals: if
\*   epoch changes then FloorOk is false in the next state, else dry or
\*   FloorOk is false in the next state."
UpdateDry ==
    dry' = IF epoch' # epoch THEN ~FloorOk' ELSE (dry \/ ~FloorOk')

\* In English: "The run is over: it has reached its last epoch. Before
\* that the only thing that ends an epoch is Boundary, gated by the two
\* timing assumptions; a state before MaxEpoch where nothing can happen is
\* an assumption the model cannot meet, which TLC's deadlock check reports."
\* Literally: "Done is defined as: epoch equals MaxEpoch and nothing
\*   changes."
Done ==
    /\ epoch = MaxEpoch
    /\ UNCHANGED vars

\* Literally: "Next is defined as: one of the steps below happens, and
\*   UpdateDry; or Done."
Next ==
    \/ /\ \/ \E p \in Pools : GoDark(p) \/ GoLive(p) \/ Repair(p) \/ Reregister(p) \/ Fail(p)
          \/ \E p \in Pools, r \in 1..(MaxEpoch + RetireMax) : Retire(p, r)
          \/ \E w \in Wallets, q \in Targets : Delegate(w, q)
          \/ Block
          \/ Boundary
       /\ UpdateDry
    \/ Done

\* Literally: "Spec is defined as: Init, and always Next or nothing
\*   changes."
Spec == Init /\ [][Next]_vars

\* In English: "The same behaviours, where every step of the engine's
\* recovery that stays possible is eventually taken: bringing a pool
\* live, repairing a pod, and moving a wallet onto a pool the guard
\* counts as forging; and a block that can be made is made, which lands
\* what is in flight (the chain's side of Boundary's first timing
\* assumption). The environment, the guarded decisions and the epoch
\* clock get no fairness."
LiveSpec ==
    /\ Spec
    /\ WF_vars(Block /\ UpdateDry)
    /\ \A p \in Pools : WF_vars(GoLive(p) /\ UpdateDry)
    /\ \A p \in Pools : WF_vars(Repair(p) /\ UpdateDry)
    /\ \A w \in Wallets : WF_vars(\E q \in Pools : Adds(w, q) /\ Delegate(w, q) /\ UpdateDry)

-----------------------------------------------------------------------------
\* Invariants.

\* Literally: "TypeOK is defined as: every variable has its shape."
TypeOK ==
    /\ epoch \in 0..MaxEpoch
    /\ label \in 0..MaxEpoch
    /\ deleg \in [Wallets -> Targets]
    /\ mark \in [Wallets -> Targets]
    /\ set \in [Wallets -> Targets]
    /\ reg \in [Pools -> {"registered", "retiring", "retired"}]
    /\ retAt \in [Pools -> 0..(MaxEpoch + RetireMax)]
    /\ produced \in BOOLEAN
    /\ plan \in [Pools -> {"live", "dark"}]
    /\ failed \in [Pools -> BOOLEAN]
    /\ nfail \in 0..MaxFaults
    /\ cen \in [Pools -> {"active", "retiring", "retired"}]
    /\ pend \in [Wallets -> Targets \cup {NoTx}]
    /\ pret \in [Pools -> 0..(MaxEpoch + RetireMax)]
    /\ preg \in [Pools -> BOOLEAN]
    /\ dry \in BOOLEAN
    /\ dryRun \in 0..MaxEpoch
    /\ cancels \in 0..1
    /\ n \in [moves : 0..MaxMoves, darks : 0..MaxDarks]

\* In English: "While no more than FaultMargin pods are dead, the pools
\* really forging hold at least Floor percent of the stake in the schedule
\* in use. Checked in every state, so it covers every epoch and every
\* moment of it, after any sequence of decisions and with no further engine
\* step (the plan's PolicyLooksAhead is this invariant on the traces where
\* the engine stops deciding)."
\* Literally: "FloorHolds is defined as: if the number of p with failed of
\*   p is at most FaultMargin then FloorOk."
FloorHolds == Cardinality({p \in Pools : failed[p]}) <= FaultMargin => FloorOk

\* In English: "The census calls a pool's actor Retired only once the
\* ledger has retired the pool (an epoch transition applied it), not when
\* the certificate confirmed."
\* Literally: "RetiredOnlyAtBoundary is defined as: for every p, cen of p is
\*   "retired" implies reg of p is "retired"."
RetiredOnlyAtBoundary == \A p \in Pools : cen[p] = "retired" => reg[p] = "retired"

\* In English: "No epoch passes without a block: the ledger never
\* freezes. This is the model's stand-in for 'the chain does not stall'."
\* Literally: "NoFreeze is defined as: Frozen is false."
NoFreeze == ~Frozen

\* In English: "Unless the ledger froze, the floor is back within
\* RecoverEpochs epochs of the latest pod death: no more than that many
\* dry epochs in a row since it."
\* Literally: "RecoverBound is defined as: Frozen, or dryRun is at most
\*   RecoverEpochs."
RecoverBound == Frozen \/ dryRun <= RecoverEpochs

-----------------------------------------------------------------------------
\* Liveness (LiveSpec).

\* In English: "Once the floor is endangered, the engine eventually
\* finishes its recovery, or the danger passes. Boundary assumes the
\* recovery finishes within the epoch; this checks that the engine's own
\* steps can always get there, so the assumption never waits for ever.
\* With RecoverBound it gives the floor back. (A literal 'the floor is
\* eventually restored' says nothing in a run bounded by MaxEpoch: a death
\* in the last epoch has no epoch left to recover in.)"
RecoveryFinishes ==
    (Recovery /\ Endangered) ~> (RecoveryDone \/ ~Endangered)

-----------------------------------------------------------------------------
\* Reachability probes: each claims a situation never happens; the Probe
\* configs expect a counterexample, which shows the situation is reachable
\* and so the passing configs do exercise it.

\* In English: "Claim: no pool ever goes dark."
\* Literally: "ProbeNeverDark is defined as: every plan is "live"."
ProbeNeverDark == \A p \in Pools : plan[p] = "live"

\* In English: "Claim: no pool is ever dark while the schedule in use
\* differs from the confirmed delegations (the guard never has to see past
\* the lag)."
\* Literally: "ProbeNoLag is defined as: not (Lead differs from deleg and
\*   some plan is "dark")."
ProbeNoLag == ~(Lead # deleg /\ \E p \in Pools : plan[p] = "dark")

\* In English: "Claim: no pool is ever retired."
\* Literally: "ProbeNeverRetired is defined as: no reg is "retired"."
ProbeNeverRetired == \A p \in Pools : reg[p] # "retired"

\* In English: "Claim: a re-registration never cancels a retirement."
\* Literally: "ProbeNoCancel is defined as: cancels is 0."
ProbeNoCancel == cancels = 0

\* In English: "Claim: a breach never lasts two epochs in a row."
\* Literally: "ProbeShortBreach is defined as: dryRun is less than 2."
ProbeShortBreach == dryRun < 2
=============================================================================
