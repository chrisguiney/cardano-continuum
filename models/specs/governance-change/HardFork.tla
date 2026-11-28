----------------------------- MODULE HardFork -----------------------------
(***************************************************************************)
(* The hard-fork purpose beside the parameter-change chain: what the       *)
(* engine's chain view must repeat of the ledger's ratification so that   *)
(* the protocol version it expects, and the predecessors its proposals    *)
(* name, are the ledger's.                                                 *)
(*                                                                         *)
(* The ledger is the formal ledger spec, formal-ledger-specifications at  *)
(* f8059737f1db (src/Ledger/Conway/Specification/...):                    *)
(*   - Epoch.lagda.md EPOCH: at each boundary the enact state ratified at *)
(*     the boundary before (fut) becomes the current one, the actions it  *)
(*     ratified or rejected are removed, and RATIFIES runs over the rest  *)
(*     into the next fut. So a hard fork ratified at boundary e moves the *)
(*     protocol version, and the hard-fork root, at boundary e + 1.       *)
(*   - Ratify.lagda.md RATIFY: in govActionPriority order (Gov.lagda.md:  *)
(*     hard forks before parameter changes), an action is accepted when   *)
(*     its votes pass, its predecessor is its purpose's root (verifyPrev) *)
(*     and nothing delaying was accepted earlier in the pass              *)
(*     (delayingAction: a hard fork delays; delayed). An action not      *)
(*     accepted and past its last voting epoch is rejected (expired).    *)
(*   - Enact.lagda.md Enact-HF and Enact-PParams move the version and    *)
(*     the purpose root (gid).                                            *)
(*   - Gov.lagda.md GOV-Propose: a hard fork is accepted when it names   *)
(*     the enacted root with a version that can follow the enacted one,  *)
(*     or a pending hard fork with a version that can follow its         *)
(*     (validHFAction, pvCanFollow in Core/Specification/                *)
(*     ProtocolVersion.lagda.md); a parameter change when it names the   *)
(*     root or a pending change (hasParent).                              *)
(* Versions are majors only: the engine only proposes major.0.            *)
(*                                                                         *)
(* The engine side (internal/chain ratify, the hard_fork scenario):       *)
(*   1. the view ratifies at each boundary from the same votes, HARD      *)
(*      FORKS FIRST and NOTHING AFTER A RATIFIED HARD FORK (the delay);   *)
(*   2. the view moves its protocol version at the BOUNDARY AFTER the one *)
(*      that ratified the fork, as the ledger enacts it;                  *)
(*   3. the scenario proposes a hard fork only with NONE OPEN, NONE IN    *)
(*      FLIGHT AND NONE RATIFIED BUT NOT YET ENACTED, naming the view's   *)
(*      hard-fork root and the version after the view's.                  *)
(* Votes are abstracted to one Approve per action: the same yes votes    *)
(* reach the ledger and the view (ParamChange.tla checks the vote side). *)
(*                                                                         *)
(* Each *Bug.cfg replaces one rule (Mutation) or denies a state that must *)
(* be reachable (a probe), and names the invariant that breaks.           *)
(*                                                                         *)
(* Models cardano-continuum at the commit README.md pins; README.md has   *)
(* the code map and the drift table.                                      *)
(*   ProposeFork  internal/engine/controls.go           scenarioRoute.HardFork *)
(*   ProposeParam internal/engine/internal/intent/spec.go buildPropose     *)
(*   Land         internal/fakechain/world.go           newLedgerView      *)
(*   Tick (view)  internal/chain/ratify.go              ratify             *)
(*   Tick (node)  internal/fakechain/ratify.go          ratify             *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    Start,      \* the genesis protocol major version
    MaxEpoch,   \* the last epoch the run reaches (bound)
    Life,       \* govActionLifetime: an action made in e is open to e + Life
    MaxForks,   \* how many hard forks the scenario proposes (bound)
    MaxParams,  \* how many parameter changes the proposer submits (bound)
    Mutation    \* "none", or the rule replaced; see the *Bug.cfg files

ASSUME Life >= 1 /\ MaxEpoch >= 1 /\ MaxForks >= 0 /\ MaxParams >= 0

Errors == {"HardForkRefused", "InvalidPrevGovActionId"}
NoFork == [to |-> 0, at |-> 0]

VARIABLES
    epoch,   \* the current epoch, the same for the ledger and the view
    props,   \* the landed actions in landing order: [k, prev, ver, exp];
             \* an action's id is its index, 0 stands for "none"
    ok,      \* the actions whose yes votes pass
    es,      \* the ledger's enact state: [pv, hf, pc]
    fut,     \* the ledger's ratification at the last boundary, enacted at
             \* the next: [pv, hf, pc, gone], gone being what it removes
    lgone,   \* the actions the ledger has removed
    vw,      \* the view: [pv, hf, pc, fork], fork a ratified hard fork's
             \* version and the epoch it enacts at, NoFork when none
    vdone,   \* the actions the view's ratification ratified
    pend,    \* the proposals built and not yet landed or expired
    nhf,     \* hard forks built
    npc,     \* parameter changes built
    errs,    \* the refusals so far
    dly      \* a parameter change was delayed by a hard fork (probe)

vars == <<epoch, props, ok, es, fut, lgone, vw, vdone, pend, nhf, npc, errs, dly>>

N == MaxForks + MaxParams
Ids == 1..N
Epochs == 0..(MaxEpoch + Life + 2)
Vers == Start..(Start + MaxForks + 1)
Prop == [k : {"hf", "pc"}, prev : 0..N, ver : Vers, exp : Epochs]
Tx == [k : {"hf", "pc"}, prev : 0..N, ver : Vers, ttl : Epochs]
Enact == [pv : Vers, hf : 0..N, pc : 0..N]

TypeOK ==
    /\ epoch \in 0..MaxEpoch
    /\ Len(props) <= N /\ \A i \in DOMAIN props : props[i] \in Prop
    /\ ok \subseteq Ids
    /\ es \in Enact
    /\ fut \in [pv : Vers, hf : 0..N, pc : 0..N, gone : SUBSET Ids]
    /\ lgone \subseteq Ids
    /\ vw \in [pv : Vers, hf : 0..N, pc : 0..N, fork : {NoFork} \cup [to : Vers, at : Epochs]]
    /\ vdone \subseteq Ids
    /\ pend \subseteq Tx
    /\ nhf \in 0..MaxForks /\ npc \in 0..MaxParams
    /\ errs \subseteq Errors
    /\ dly \in BOOLEAN

-----------------------------------------------------------------------------
\* Helpers.

Ofk(k) == {i \in DOMAIN props : props[i].k = k}
\* In English: "Action i is open in the view: the view has not ratified it
\* and its last voting epoch has not passed."
Open(i) == i \notin vdone /\ props[i].exp >= epoch
\* In English: "The ledger still holds action i."
Held(i) == i \in DOMAIN props /\ i \notin lgone
InFlight(k) == \E t \in pend : t.k = k

\* In English: "The ledger accepts hard fork t (GOV-Propose,
\* validHFAction): it names the enacted root with the next version, or a
\* pending hard fork whose version it can follow. Majors only: a pending
\* fork to the next major leaves only a minor step, which the engine
\* never proposes."
ForkOk(t) ==
    \/ t.prev = es.hf /\ t.ver = es.pv + 1
    \/ /\ t.prev # 0 /\ Held(t.prev) /\ props[t.prev].k = "hf"
       /\ props[t.prev].ver # es.pv + 1
       /\ t.ver = props[t.prev].ver + 1
\* In English: "The ledger accepts parameter change t (hasParent)."
ParamOk(t) == t.prev = es.pc \/ (t.prev # 0 /\ Held(t.prev) /\ props[t.prev].k = "pc")

\* In English: "The ledger's RATIFIES at the boundary into e over the
\* actions it holds, from enact state s: hard forks in id order, then
\* parameter changes; an accepted hard fork sets the delay, and a delayed
\* or failing action past its last epoch is rejected. Returns [pv, hf, pc,
\* gone, dl, skipped]: skipped says a parameter change that would have
\* passed was delayed."
RECURSIVE LRat(_, _, _, _)
LRat(ids, s, k, e) ==
    IF ids = {} THEN s
    ELSE LET i == CHOOSE x \in ids : \A y \in ids : x <= y
             p == props[i]
             acc == /\ i \in ok /\ ~s.dl
                    /\ IF k = "hf" THEN p.prev = s.hf ELSE p.prev = s.pc
             wouldPass == i \in ok /\ k = "pc" /\ p.prev = s.pc
             s2 == IF acc
                   THEN IF k = "hf"
                        THEN [s EXCEPT !.pv = p.ver, !.hf = i, !.gone = @ \cup {i}, !.dl = TRUE]
                        ELSE [s EXCEPT !.pc = i, !.gone = @ \cup {i}]
                   ELSE IF p.exp < e THEN [s EXCEPT !.gone = @ \cup {i},
                                                    !.skipped = @ \/ (wouldPass /\ s.dl)]
                   ELSE [s EXCEPT !.skipped = @ \/ (wouldPass /\ s.dl)]
         IN LRat(ids \ {i}, s2, k, e)

LedgerRatify(e, s0, held) ==
    LET a == LRat({i \in held : props[i].k = "hf"},
                  [pv |-> s0.pv, hf |-> s0.hf, pc |-> s0.pc, gone |-> {}, dl |-> FALSE, skipped |-> FALSE], "hf", e)
    IN LRat({i \in held : props[i].k = "pc"}, a, "pc", e)

\* In English: "The view's ratification at the boundary into e over the
\* actions it has open at it (not ratified, last voting epoch e - 1 or
\* later), from its state s: hard forks first, then parameter changes, and
\* nothing after a ratified hard fork. Mutation NoDelay drops the delay;
\* ParamFirst ratifies parameter changes first; EnactNow moves the version
\* at once instead of at the next boundary. Returns [vw, done]."
RECURSIVE VRat(_, _, _, _, _)
VRat(ids, s, d, k, e) ==
    IF ids = {} THEN [vw |-> s, done |-> d.done, dl |-> d.dl]
    ELSE LET i == CHOOSE x \in ids : \A y \in ids : x <= y
             p == props[i]
             acc == /\ i \in ok /\ (Mutation = "NoDelay" \/ ~d.dl)
                    /\ IF k = "hf" THEN p.prev = s.hf ELSE p.prev = s.pc
             s2 == IF ~acc THEN s
                   ELSE IF k = "pc" THEN [s EXCEPT !.pc = i]
                   ELSE IF Mutation = "EnactNow" THEN [s EXCEPT !.hf = i, !.pv = p.ver]
                   ELSE [s EXCEPT !.hf = i, !.fork = [to |-> p.ver, at |-> e + 1]]
             d2 == IF ~acc THEN d
                   ELSE [done |-> d.done \cup {i}, dl |-> d.dl \/ k = "hf"]
         IN VRat(ids \ {i}, s2, d2, k, e)

ViewRatify(e, s0) ==
    LET s1 == IF s0.fork # NoFork /\ s0.fork.at <= e
              THEN [s0 EXCEPT !.pv = s0.fork.to, !.fork = NoFork] ELSE s0
        open == {i \in DOMAIN props : i \notin vdone /\ props[i].exp + 1 >= e}
        first == IF Mutation = "ParamFirst" THEN "pc" ELSE "hf"
        second == IF Mutation = "ParamFirst" THEN "hf" ELSE "pc"
        a == VRat({i \in open : props[i].k = first}, s1, [done |-> {}, dl |-> FALSE], first, e)
        b == VRat({i \in open : props[i].k = second}, a.vw, [done |-> a.done, dl |-> a.dl], second, e)
    IN b

-----------------------------------------------------------------------------
\* The engine.

\* In English: "The hard_fork scenario proposes the next hard fork: none
\* open in the view, none in flight and none ratified but not yet enacted
\* (Mutation NoPendingGuard drops the last), naming the view's hard-fork
\* root and the version after the view's (Mutation StaleVersion: after
\* the genesis version, whatever was enacted since)."
ProposeFork ==
    /\ nhf < MaxForks
    /\ ~InFlight("hf")
    /\ \A i \in Ofk("hf") : ~Open(i)
    /\ Mutation = "NoPendingGuard" \/ vw.fork = NoFork
    /\ pend' = pend \cup {[k |-> "hf", prev |-> vw.hf,
                           ver |-> IF Mutation = "StaleVersion" THEN Start + 1 ELSE vw.pv + 1,
                           ttl |-> epoch + 1]}
    /\ nhf' = nhf + 1
    /\ UNCHANGED <<epoch, props, ok, es, fut, lgone, vw, vdone, npc, errs, dly>>

\* In English: "The proposer builds its next parameter change, one open at
\* a time (ParamChange.tla's rules 1 and 2), naming the view's root."
ProposeParam ==
    /\ npc < MaxParams
    /\ ~InFlight("pc")
    /\ \A i \in Ofk("pc") : ~Open(i)
    /\ pend' = pend \cup {[k |-> "pc", prev |-> vw.pc, ver |-> Start, ttl |-> epoch + 1]}
    /\ npc' = npc + 1
    /\ UNCHANGED <<epoch, props, ok, es, fut, lgone, vw, vdone, nhf, errs, dly>>

\* In English: "The votes on action i, open in the view, pass."
Approve(i) ==
    /\ i \in DOMAIN props /\ Open(i) /\ i \notin ok
    /\ ok' = ok \cup {i}
    /\ UNCHANGED <<epoch, props, es, fut, lgone, vw, vdone, pend, nhf, npc, errs, dly>>

-----------------------------------------------------------------------------
\* The node.

\* In English: "A proposal lands in a block: added when the ledger accepts
\* it, refused otherwise."
Land(t) ==
    /\ t \in pend /\ epoch <= t.ttl
    /\ pend' = pend \ {t}
    /\ LET good == IF t.k = "hf" THEN ForkOk(t) ELSE ParamOk(t) IN
       IF good
       THEN /\ props' = Append(props, [k |-> t.k, prev |-> t.prev, ver |-> t.ver, exp |-> epoch + Life])
            /\ UNCHANGED errs
       ELSE /\ errs' = errs \cup {IF t.k = "hf" THEN "HardForkRefused" ELSE "InvalidPrevGovActionId"}
            /\ UNCHANGED props
    /\ UNCHANGED <<epoch, ok, es, fut, lgone, vw, vdone, nhf, npc, dly>>

\* In English: "The epoch turns. The ledger enacts what it ratified at the
\* last boundary and removes what that ratification took out, then
\* ratifies the rest (EPOCH). The view ratifies at the same boundary from
\* the same votes (its first block of the epoch)."
Tick ==
    /\ epoch < MaxEpoch
    /\ LET e == epoch + 1
           es2 == [pv |-> fut.pv, hf |-> fut.hf, pc |-> fut.pc]
           gone == lgone \cup fut.gone
           r == LedgerRatify(e, es2, {i \in DOMAIN props : i \notin gone})
           v == ViewRatify(e, vw)
       IN /\ es' = es2
          /\ lgone' = gone
          /\ fut' = [pv |-> r.pv, hf |-> r.hf, pc |-> r.pc, gone |-> r.gone]
          /\ dly' = (dly \/ r.skipped)
          /\ vw' = v.vw
          /\ vdone' = vdone \cup v.done
    /\ epoch' = epoch + 1
    /\ pend' = {t \in pend : t.ttl >= epoch + 1}
    /\ UNCHANGED <<props, ok, nhf, npc, errs>>

Init ==
    /\ epoch = 0
    /\ props = <<>>
    /\ ok = {}
    /\ es = [pv |-> Start, hf |-> 0, pc |-> 0]
    /\ fut = [pv |-> Start, hf |-> 0, pc |-> 0, gone |-> {}]
    /\ lgone = {}
    /\ vw = [pv |-> Start, hf |-> 0, pc |-> 0, fork |-> NoFork]
    /\ vdone = {}
    /\ pend = {}
    /\ nhf = 0 /\ npc = 0
    /\ errs = {}
    /\ dly = FALSE

\* In English: "The run is over: it has reached its last epoch. Before
\* that Tick is always possible, so any other state with no step is a
\* stuck engine or node, which TLC's deadlock check reports."
Done ==
    /\ epoch = MaxEpoch
    /\ UNCHANGED vars

Next ==
    \/ ProposeFork
    \/ ProposeParam
    \/ \E i \in Ids : Approve(i)
    \/ \E t \in pend : Land(t)
    \/ Tick
    \/ Done

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
\* Invariants.

\* In English: "The protocol version the view expects is the ledger's, in
\* every epoch: what the oracle compares each node's answer with."
VersionAgrees == vw.pv = es.pv
\* In English: "With no fork pending in the view, its hard-fork root is
\* the ledger's enacted one."
ForkRootAgrees == vw.fork = NoFork => vw.hf = es.hf
\* In English: "The view's parameter-change root is the one the ledger's
\* last ratification left (the view moves it when it ratifies, the
\* ledger enacts it a boundary later)."
ParamRootAgrees == vw.pc = fut.pc
\* In English: "No hard fork or parameter change the engine proposes is
\* refused."
ForkAccepted == "HardForkRefused" \notin errs
ParamAccepted == "InvalidPrevGovActionId" \notin errs
\* In English: "At most one hard fork is open in the view, in flight or
\* ratified and waiting to be enacted."
OneFork ==
    Cardinality({i \in Ofk("hf") : Open(i)})
      + Cardinality({t \in pend : t.k = "hf"})
      + (IF vw.fork = NoFork THEN 0 ELSE 1) <= 1

-----------------------------------------------------------------------------
\* Probes. Each says a state is unreachable; its *Bug.cfg must find it.

\* In English: "No second hard fork is ever enacted."
ProbeNoSecondFork == es.pv < Start + 2
\* In English: "No parameter change is ever delayed by a hard fork."
ProbeNoDelayedParam == ~dly
=============================================================================
