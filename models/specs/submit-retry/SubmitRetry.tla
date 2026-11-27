---------------------------- MODULE SubmitRetry ----------------------------
(***************************************************************************)
(* The submitter's retry machine.                                          *)
(*                                                                         *)
(* Models cardano-continuum at 243b05a; README.md has the code map and     *)
(* the drift table.                                                        *)
(*   Build      internal/engine/internal/pay/retry.go:343  Retry.rebuild   *)
(*              internal/engine/internal/pay/pay.go:139    Pay.Reserve     *)
(*   Submit     internal/engine/internal/pay/retry.go:300  SendTx action   *)
(*              internal/engine/internal/send/send.go:58   Sender.Send     *)
(*   Refusal    internal/submit/submit.go:138              Outcome.Refutes *)
(*   Classify   internal/engine/internal/pay/retry.go:121  Retry.Answer    *)
(*              internal/mistake/sample.go:64              Diagnose        *)
(*   Confirm    internal/engine/internal/pay/retry.go:144  Retry.InBlock   *)
(*   RolledBack internal/engine/internal/pay/retry.go:163  Retry.LeftView  *)
(*   Lost       internal/engine/internal/pay/retry.go:198  Retry.Release   *)
(*   Dropped    internal/engine/internal/pay/retry.go:184  Retry.Dropped   *)
(*   Done       internal/engine/internal/pay/retry.go:128  Retry.Settle    *)
(*   GiveUp     internal/engine/internal/pay/retry.go:314  GiveUp action   *)
(*   machine    internal/submit/step.go:244                submit.Step     *)
(*   Sync       internal/engine/internal/pay/pay.go:150    Pay.Advance     *)
(*   Restart    internal/engine/internal/send/send.go:42   send.New Resend *)
(*   Expected   internal/mistake/mistake.go:265            mistake.Expect  *)
(*                                                                         *)
(* An actor pays once per intent. Its transaction may carry mistakes the   *)
(* mistake injector put there. The submitter sends it, a node answers      *)
(* (accepted, rejected with some failures, or no answer at all), and the   *)
(* actor reacts: it waits, resubmits the same bytes, or reads the          *)
(* rejection, decides what it got wrong, fixes that and builds again. A    *)
(* low-competence actor may decide wrongly. After Patience submissions it  *)
(* gives up. Meanwhile blocks include, bury and roll back its attempts and *)
(* the planner hears about them late.                                      *)
(*                                                                         *)
(* The model answers: which rules let this machine never pay an intent     *)
(* twice, end every intent, keep within the actor's patience, and let the  *)
(* oracle expect every answer a node can honestly give? The rules the      *)
(* unmutated model states are:                                             *)
(*   1. the reservation rules of specs/planner-reservations: an attempt    *)
(*      holds its inputs until it is settled or dead; a new attempt goes   *)
(*      through Reserve and conflicts with every unfinished one; a drop,   *)
(*      a rollback, a transport error and giving up release nothing;       *)
(*   2. a rejection writes the attempt off (Refused) only when the         *)
(*      attempt was never sent before, or the node named a rule that does  *)
(*      not depend on chain state. An "inputs already spent" answer to a   *)
(*      resubmit is what a node says when the attempt is already in a      *)
(*      block, so it proves nothing;                                       *)
(*   3. that decision reads the node's answer, never the actor's           *)
(*      diagnosis; the diagnosis only chooses the fix;                     *)
(*   4. fixes accumulate, and the submission counter survives rollbacks    *)
(*      and restarts;                                                      *)
(*   5. the oracle expects a SET of answers: every failure the mistakes    *)
(*      can cause, in any rule order, plus "inputs spent" whenever the     *)
(*      attempt or a conflicting one may already be at a node.            *)
(* Each value of Mutation breaks one rule; the README lists them.          *)
(*                                                                         *)
(* The chain is abstracted per attempt to "not in a block", "in a block"   *)
(* and "K deep". Inputs are abstracted to groups: attempts of one intent   *)
(* in one group share an input, so at most one of them can land.           *)
(* Different intents share nothing here; the planner model checks that.   *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see specs/hello/Hello.tla.                     *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    NumIntents,    \* how many payments are attempted
    MaxSlots,      \* how many attempt records one intent holds at once
    Patience,      \* how many submissions an actor makes before giving up
    Competence,    \* "high": diagnoses are right; "low": any diagnosis
    Mistakes,      \* the mistake kinds the injector may use (see ClassOf)
    MaxRollbacks,  \* how many times a block is rolled back
    MaxEvictions,  \* how many times the local mempool evicts a transaction
    MaxAdv,        \* how many times somebody else spends an intent's input
    MaxRestarts,   \* how many times the engine restarts
    Recovery,      \* "rebuild": a restart rebuilds each machine from the
                   \* records (contract rule 7, submit.Restart); "replay":
                   \* the engine's checkpoint and log restore every machine
                   \* as it was, and only an interrupted send counts as sent
    Mutation       \* "none", or one deliberate mistake (see the README)

ASSUME Recovery \in {"rebuild", "replay"}

Intents == 1..NumIntents
Slots == 1..MaxSlots
\* An attempt is named by its intent and the record slot it occupies. A
\* slot is reused once its attempt is dead and can provably never land:
\* such an attempt has no further effect on anything.
Txs == Intents \X Slots
\* Input groups: an intent's first attempt spends group 1; a rebuild
\* after every earlier attempt is dead may move to group 2.
Groups == 1..2
Adv == Intents \X Groups

\* The failure classes, as internal/submit names them. MempoolFailure is
\* the LEDGER-level "All inputs are spent." a cardano-node gives when an
\* input is already consumed in its mempool or chain.
Classes == {"FeeTooSmall", "MissingSigner", "BadInputs", "MempoolFailure"}
\* In English: "Classes whose rule does not depend on chain state: if a
\* node names one, no node will ever accept these bytes."
Intrinsic == {"FeeTooSmall", "MissingSigner"}
\* The UTXO-level rules a node checks in some order.
RuleClasses == {"FeeTooSmall", "MissingSigner", "BadInputs"}
\* In English: "The class of failure each mistake kind causes: a fee too
\* small, a missing signer, or a stale input (one already spent long ago)."
ClassOf(m) == CASE m = "Fee"    -> "FeeTooSmall"
                [] m = "Signer" -> "MissingSigner"
                [] m = "Stale"  -> "BadInputs"
\* In English: "The mistakes fixed by acting on diagnosis d. A diagnosis of
\* MempoolFailure fixes nothing."
FixFor(d) == {m \in Mistakes : ClassOf(m) = d}

\* In English: "Every order a node may check the three rules in."
Orders == {o \in [1..3 -> RuleClasses] : \A a, b \in 1..3 : a # b => o[a] # o[b]}
\* In English: "The first failing rule under order o, when F fail."
FirstFailing(o, F) ==
    o[CHOOSE j \in 1..3 : o[j] \in F /\ \A k \in 1..(j - 1) : o[k] \notin F]

\* The machine's states.
\*   build:     the next attempt must be built (through Reserve)
\*   submit:    the current attempt is to be sent, first time or again
\*   pending:   a node accepted it; waiting to see it in a block
\*   confirmed: the planner's view has it in a block short of K deep
\*   rejected:  a node rejected it; the actor has not diagnosed it yet
\*   done:      an attempt of the intent is settled (K deep)
\*   gaveup:    the actor stopped; the planner still decides the attempts
MStates == {"build", "submit", "pending", "confirmed", "rejected", "done", "gaveup"}
Final == {"done", "gaveup"}
\* Planner records, as in specs/planner-reservations.
TStates == {"unused", "live", "settled", "dead"}
\* Where an attempt is on the node's chain (truth) or in the planner's view.
Depths == {"none", "block", "deep"}

\* Mutations that remove the attempt cap.
NoCap == Mutation = "NoCap"

VARIABLES
    \* the truth, per attempt
    reached,  \* it reached a node, so a block may include it
    inmp,     \* it is in the local node's mempool
    chain,    \* "none", "block" (rollbackable) or "deep" (immovable)
    exp,      \* the immovable chain is past its time-to-live
    adv,      \* adv[<<i, g>>]: somebody else spent group g's input, K deep
    \* the planner, per attempt
    st,       \* the record: unused, live, settled or dead
    grp,      \* the input group it spends; 0 while unused
    mist,     \* the mistakes it carries
    sent,     \* it was handed to a node at least once and may have arrived
    view,     \* where the planner's view has it
    aview,    \* the planner has seen adv[a]
    \* the retry machine, per intent
    ms,       \* the machine state
    cur,      \* the slot of the attempt the machine is working on
    tries,    \* submissions so far; persisted with the intent
    inj,      \* the mistakes the injector chose for this intent
    fixed,    \* the mistakes the actor has fixed so far
    vd,       \* the node's last rejection (a set of classes)
    diag,     \* the actor's last diagnosis
    \* ghosts and bounds
    total,    \* submissions ever made for the intent; never reset
    ever,     \* every mistake the actor has ever fixed
    n         \* how many of each bounded environment step have happened

vars == <<reached, inmp, chain, exp, adv, st, grp, mist, sent, view, aview,
          ms, cur, tries, inj, fixed, vd, diag, total, ever, n>>
truth == <<reached, inmp, chain, exp, adv>>
planner == <<st, grp, mist, sent, view, aview>>
machine == <<ms, cur, tries, inj, fixed, vd, diag, total, ever>>

-----------------------------------------------------------------------------
\* Helpers.

Min(S) == CHOOSE x \in S : \A y \in S : x <= y
Max(S) == CHOOSE x \in S : \A y \in S : x >= y
\* In English: "x, but never more than one past Patience: the counters
\* only need to tell 'within patience' from 'past it'."
Sat(x) == IF x > Patience + 1 THEN Patience + 1 ELSE x

\* In English: "The attempts of intent i."
Of(i) == {t \in Txs : t[1] = i}
\* In English: "The other attempts of t's intent that spend t's input
\* group, so that at most one of them and t can ever be on the chain."
Mates(t) == {u \in Of(t[1]) : u # t /\ grp[u] = grp[t] /\ grp[u] # 0}
\* In English: "The adversary entry for t's input group."
AdvOf(t) == <<t[1], grp[t]>>

\* In English: "At the node, t's input is already consumed: by t itself
\* (in the mempool or a block), by a conflicting attempt, or by somebody
\* else."
SpentAtNode(t) ==
    \/ inmp[t] \/ chain[t] # "none" \/ adv[AdvOf(t)]
    \/ \E u \in Mates(t) : inmp[u] \/ chain[u] # "none"
\* In English: "What the harness knows: t's input may already be consumed,
\* because t or a conflicting attempt was sent before, or the harness's own
\* adversary spent it."
MaybeSpent(t) ==
    \/ sent[t] \/ adv[AdvOf(t)]
    \/ \E u \in Mates(t) : sent[u]

\* In English: "The rules t fails at the node right now."
Fails(t) == {ClassOf(m) : m \in mist[t]} \cup
            (IF SpentAtNode(t) THEN {"BadInputs"} ELSE {})
\* In English: "Every answer a node can honestly give to t. The empty set
\* stands for acceptance. A rejection names the first failing rule under
\* some order (as gouroboros does), or every failing rule (as cardano-node
\* does), or, when an input is consumed, the mempool's own failure."
NodeVerdicts(t) ==
    IF Fails(t) = {} THEN {{}}
    ELSE {{FirstFailing(o, Fails(t))} : o \in Orders}
         \cup {Fails(t)}
         \cup (IF SpentAtNode(t) THEN {{"MempoolFailure"}} ELSE {})

\* In English: "The classes the oracle expects for t: one per injected
\* mistake, plus both 'inputs spent' classes when t's input may already be
\* consumed. Mutation SingleExpected expects one class for the first
\* mistake only; OracleIgnoresResubmit forgets that t or a conflicting
\* attempt was sent before."
Expected(t) ==
    LET mine  == IF Mutation = "SingleExpected" /\ mist[t] # {}
                 THEN {ClassOf(CHOOSE m \in mist[t] : TRUE)}
                 ELSE {ClassOf(m) : m \in mist[t]}
        maybe == IF Mutation = "OracleIgnoresResubmit"
                 THEN adv[AdvOf(t)] ELSE MaybeSpent(t)
    IN  mine \cup (IF maybe THEN {"BadInputs", "MempoolFailure"} ELSE {})
\* In English: "The oracle passes verdict v for t: an acceptance when t
\* carries no mistake, a rejection when every class it names is expected."
OracleExpects(t, v) ==
    IF v = {} THEN mist[t] = {} ELSE v \subseteq Expected(t)

\* In English: "The submitter writes t off on rejection v: t was never
\* sent before (so the rejection proves it reached no mempool), or v names
\* a rule no node will ever pass these bytes. Mutation RefuseOnSpent writes
\* off on any rejection; RefuseOnDiagnosis decides later, from the actor's
\* diagnosis."
Refusal(t, v) ==
    CASE Mutation = "RefuseOnSpent"     -> TRUE
      [] Mutation = "RefuseOnDiagnosis" -> FALSE
      [] OTHER -> ~sent[t] \/ v \cap Intrinsic # {}

\* In English: "Attempt t could still be on the chain, whatever the planner
\* thinks: it is there now, or it reached a node, carries no mistake, has
\* not expired, and its input is spent by nobody K deep."
CanLand(t) ==
    \/ chain[t] # "none"
    \/ /\ reached[t] /\ ~exp[t] /\ mist[t] = {} /\ ~adv[AdvOf(t)]
       /\ \A u \in Mates(t) : chain[u] # "deep"

\* In English: "The slots intent i may build in: never used, or holding an
\* attempt that is dead and truly can never land. Mutation NoSlotReuse
\* never reuses a slot."
Free(i) == {s \in Slots : LET t == <<i, s>> IN
                \/ st[t] = "unused"
                \/ Mutation # "NoSlotReuse" /\ st[t] = "dead" /\ ~CanLand(t)}

\* In English: "The records once the planner looks at view vw and seen
\* outside spends av: an unfinished attempt K deep is settled; one whose
\* input is spent K deep, by a conflicting attempt or by somebody else, is
\* dead; the rest stay. Mutation SettleOnBlock settles an attempt as soon
\* as the view has it in a block."
Reaped(vw, av) ==
    [t \in Txs |->
        IF st[t] # "live" THEN st[t]
        ELSE IF vw[t] = "deep" \/ (Mutation = "SettleOnBlock" /\ vw[t] = "block")
             THEN "settled"
        ELSE IF av[AdvOf(t)] \/ \E u \in Mates(t) : vw[u] = "deep"
             THEN "dead"
        ELSE "live"]

\* In English: "The records with every live attempt of intent i dead."
KillAll(i) == [t \in Txs |-> IF t[1] = i /\ st[t] = "live" THEN "dead" ELSE st[t]]

-----------------------------------------------------------------------------
\* In English: "At the start nothing is built and every intent is to be
\* built. The injector has chosen each intent's mistakes: any set."
Init ==
    /\ reached = [t \in Txs |-> FALSE]
    /\ inmp = [t \in Txs |-> FALSE]
    /\ chain = [t \in Txs |-> "none"]
    /\ exp = [t \in Txs |-> FALSE]
    /\ adv = [a \in Adv |-> FALSE]
    /\ st = [t \in Txs |-> "unused"]
    /\ grp = [t \in Txs |-> 0]
    /\ mist = [t \in Txs |-> {}]
    /\ sent = [t \in Txs |-> FALSE]
    /\ view = [t \in Txs |-> "none"]
    /\ aview = [a \in Adv |-> FALSE]
    /\ ms = [i \in Intents |-> "build"]
    /\ cur = [i \in Intents |-> 1]
    /\ tries = [i \in Intents |-> 0]
    /\ inj \in [Intents -> SUBSET Mistakes]
    /\ fixed = [i \in Intents |-> {}]
    /\ vd = [i \in Intents |-> {}]
    /\ diag = [i \in Intents |-> "none"]
    /\ total = [i \in Intents |-> 0]
    /\ ever = [i \in Intents |-> {}]
    /\ n = [roll |-> 0, evict |-> 0, adv |-> 0, restart |-> 0]

-----------------------------------------------------------------------------
\* The retry machine.

\* In English: "Build the next attempt of intent i in group g, through
\* Reserve: the intent has not paid, g's input is not known to be spent by
\* somebody else, and g conflicts with every unfinished attempt (they are
\* all in one group). It carries the injected mistakes the actor has not
\* fixed. Mutation BypassReserve takes any group."
Build(i, g) ==
    /\ ms[i] = "build"
    /\ tries[i] < Patience \/ NoCap
    /\ Free(i) # {}
    /\ \A u \in Of(i) : st[u] # "settled"
    /\ ~aview[<<i, g>>]
    /\ Mutation = "BypassReserve" \/ \A u \in Of(i) : st[u] = "live" => grp[u] = g
    /\ LET t == <<i, Min(Free(i))>> IN
       /\ st' = [st EXCEPT ![t] = "live"]
       /\ grp' = [grp EXCEPT ![t] = g]
       /\ mist' = [mist EXCEPT ![t] = inj[i] \ fixed[i]]
       /\ sent' = [sent EXCEPT ![t] = FALSE]
       /\ view' = [view EXCEPT ![t] = "none"]
       /\ reached' = [reached EXCEPT ![t] = FALSE]
       /\ inmp' = [inmp EXCEPT ![t] = FALSE]
       /\ exp' = [exp EXCEPT ![t] = FALSE]
       /\ cur' = [cur EXCEPT ![i] = t[2]]
    /\ ms' = [ms EXCEPT ![i] = "submit"]
    /\ UNCHANGED <<chain, adv, aview, tries, inj, fixed, vd, diag, total, ever, n>>

\* In English: "Send the current attempt t. Every send counts against
\* patience. The node accepts it (it then sits in the mempool), or gives no
\* answer (it may or may not have arrived; the machine sends again), or
\* rejects it with any honest verdict. A rejection that is a refusal writes
\* t off. Mutation RefuseOnUnreached writes t off when there is no answer."
Submit(i) ==
    LET t == <<i, cur[i]>> IN
    /\ ms[i] = "submit"
    /\ st[t] = "live"
    /\ tries[i] < Patience \/ NoCap
    /\ tries' = [tries EXCEPT ![i] = Sat(@ + 1)]
    /\ total' = [total EXCEPT ![i] = Sat(@ + 1)]
    /\ \/ /\ Fails(t) = {}
          /\ reached' = [reached EXCEPT ![t] = TRUE]
          /\ inmp' = [inmp EXCEPT ![t] = TRUE]
          /\ sent' = [sent EXCEPT ![t] = TRUE]
          /\ ms' = [ms EXCEPT ![i] = "pending"]
          /\ UNCHANGED <<st, vd>>
       \/ /\ \E arrived \in IF Fails(t) = {} THEN BOOLEAN ELSE {FALSE} :
                 /\ reached' = [reached EXCEPT ![t] = @ \/ arrived]
                 /\ inmp' = [inmp EXCEPT ![t] = @ \/ arrived]
          /\ sent' = [sent EXCEPT ![t] = TRUE]
          /\ IF Mutation = "RefuseOnUnreached"
             THEN /\ st' = [st EXCEPT ![t] = "dead"]
                  /\ ms' = [ms EXCEPT ![i] = "build"]
             ELSE /\ ms' = [ms EXCEPT ![i] = "submit"]
                  /\ UNCHANGED st
          /\ UNCHANGED vd
       \/ \E v \in NodeVerdicts(t) :
          /\ v # {}
          /\ vd' = [vd EXCEPT ![i] = v]
          /\ st' = IF Refusal(t, v) THEN [st EXCEPT ![t] = "dead"] ELSE st
          /\ ms' = [ms EXCEPT ![i] = "rejected"]
          /\ UNCHANGED <<reached, inmp, sent>>
    /\ UNCHANGED <<chain, exp, adv, grp, mist, view, aview, cur, inj, fixed, diag, ever, n>>

\* In English: "The actor reads the rejection and decides what it got
\* wrong: one of the classes named when competent, anything when not. It
\* fixes that and builds again. Mutation FixUndoes replaces the earlier
\* fixes instead of adding to them. Under RefuseOnDiagnosis the
\* write-off is decided here, from the diagnosis."
Classify(i, d) ==
    LET t == <<i, cur[i]>> IN
    /\ ms[i] = "rejected"
    /\ d \in IF Competence = "low" THEN Classes ELSE vd[i]
    /\ diag' = [diag EXCEPT ![i] = d]
    /\ fixed' = [fixed EXCEPT ![i] =
                    IF Mutation = "FixUndoes" THEN FixFor(d) ELSE @ \cup FixFor(d)]
    /\ ever' = [ever EXCEPT ![i] = @ \cup FixFor(d)]
    /\ st' = IF /\ Mutation = "RefuseOnDiagnosis"
                /\ st[t] = "live"
                /\ ~sent[t] \/ d \in Intrinsic
             THEN [st EXCEPT ![t] = "dead"] ELSE st
    /\ ms' = [ms EXCEPT ![i] = "build"]
    /\ UNCHANGED <<truth, grp, mist, sent, view, aview, cur, tries, inj, vd, total, n>>

\* In English: "The planner's view shows an attempt of i in a block while
\* the machine waits: it is confirmed, which is not settled."
Confirm(i) ==
    /\ ms[i] = "pending"
    /\ \E u \in Of(i) :
          /\ view[u] = "block"
          /\ cur' = [cur EXCEPT ![i] = u[2]]
    /\ ms' = [ms EXCEPT ![i] = "confirmed"]
    /\ UNCHANGED <<truth, planner, tries, inj, fixed, vd, diag, total, ever, n>>

\* In English: "The confirmed attempt left the view: it was rolled back.
\* The machine re-verifies: the planner has not written it off, so the same
\* bytes are sent again (if an input had been spent K deep the planner
\* would have written it off, and Lost would rebuild). Mutation
\* ResetOnRollback also resets the submission counter; ReleaseOnRollback
\* writes the attempt off and rebuilds."
RolledBack(i) ==
    LET t == <<i, cur[i]>> IN
    /\ ms[i] = "confirmed"
    /\ view[t] = "none"
    /\ st[t] = "live"
    /\ tries' = IF Mutation = "ResetOnRollback" THEN [tries EXCEPT ![i] = 0] ELSE tries
    /\ IF Mutation = "ReleaseOnRollback"
       THEN /\ st' = [st EXCEPT ![t] = "dead"]
            /\ ms' = [ms EXCEPT ![i] = "build"]
       ELSE /\ ms' = [ms EXCEPT ![i] = "submit"]
            /\ UNCHANGED st
    /\ UNCHANGED <<truth, grp, mist, sent, view, aview, cur, inj, fixed, vd, diag, total, ever, n>>

\* In English: "The planner reports the current attempt dead (expired, or
\* its input spent K deep): build the next one."
Lost(i) ==
    /\ ms[i] \in {"submit", "pending", "confirmed"}
    /\ st[<<i, cur[i]>>] = "dead"
    /\ ms' = [ms EXCEPT ![i] = "build"]
    /\ UNCHANGED <<truth, planner, cur, tries, inj, fixed, vd, diag, total, ever, n>>

\* In English: "The submitter notices the accepted attempt is neither in
\* the mempool nor in the view. That is an eviction, or a block the planner
\* has not seen yet. It rebuilds, through Reserve, and releases nothing.
\* Mutation ReleaseOnDrop writes the attempt off first."
Dropped(i) ==
    LET t == <<i, cur[i]>> IN
    /\ ms[i] = "pending"
    /\ st[t] = "live"
    /\ sent[t] /\ ~inmp[t] /\ view[t] = "none"
    /\ st' = IF Mutation = "ReleaseOnDrop" THEN [st EXCEPT ![t] = "dead"] ELSE st
    /\ ms' = [ms EXCEPT ![i] = "build"]
    /\ UNCHANGED <<truth, grp, mist, sent, view, aview, cur, tries, inj, fixed, vd, diag, total, ever, n>>

\* In English: "An attempt of i is settled: the intent is paid."
Done(i) ==
    /\ ms[i] \notin Final
    /\ \E u \in Of(i) : st[u] = "settled"
    /\ ms' = [ms EXCEPT ![i] = "done"]
    /\ UNCHANGED <<truth, planner, cur, tries, inj, fixed, vd, diag, total, ever, n>>

\* In English: "The actor's patience is spent: it stops for good. Its
\* attempts stay unfinished until the planner settles or writes them off.
\* Mutation ReleaseOnGiveUp writes them all off at once."
GiveUp(i) ==
    /\ ms[i] \in {"build", "submit"}
    /\ tries[i] >= Patience
    /\ ~NoCap
    /\ ms' = [ms EXCEPT ![i] = "gaveup"]
    /\ st' = IF Mutation = "ReleaseOnGiveUp" THEN KillAll(i) ELSE st
    /\ UNCHANGED <<truth, grp, mist, sent, view, aview, cur, tries, inj, fixed, vd, diag, total, ever, n>>

Machine(i) ==
    \/ \E g \in Groups : Build(i, g)
    \/ Submit(i)
    \/ \E d \in Classes : Classify(i, d)
    \/ Confirm(i) \/ RolledBack(i) \/ Lost(i) \/ Dropped(i) \/ Done(i) \/ GiveUp(i)

-----------------------------------------------------------------------------
\* The planner.

\* In English: "The planner's view catches up with where t is on the chain,
\* then settles and writes off what the view decides."
Sync(t) ==
    /\ view[t] # chain[t]
    /\ view' = [view EXCEPT ![t] = chain[t]]
    /\ st' = Reaped(view', aview)
    /\ UNCHANGED <<truth, grp, mist, sent, aview, machine, n>>

\* In English: "The planner sees somebody else's spend of a group's input
\* K deep and writes off every unfinished attempt spending it."
SyncAdv(a) ==
    /\ adv[a] /\ ~aview[a]
    /\ aview' = [aview EXCEPT ![a] = TRUE]
    /\ st' = Reaped(view, aview')
    /\ UNCHANGED <<truth, grp, mist, sent, view, machine, n>>

\* In English: "The immovable chain passes t's time-to-live without t: t is
\* dead, and the planner says so (settled expiry). Only an attempt that
\* was sent ages out here; one still waiting to be sent is the machine's.
\* Mutation NoExpiry never writes an attempt off for its time-to-live."
Expire(t) ==
    /\ Mutation # "NoExpiry"
    /\ st[t] = "live" /\ sent[t] /\ chain[t] = "none"
    /\ exp' = [exp EXCEPT ![t] = TRUE]
    /\ inmp' = [inmp EXCEPT ![t] = FALSE]
    /\ st' = [st EXCEPT ![t] = "dead"]
    /\ UNCHANGED <<reached, chain, adv, grp, mist, sent, view, aview, machine, n>>

\* In English: "The engine restarts. Records, counters and fixes are on
\* disk. The view resumes at the settled point. Every unfinished attempt is
\* treated as sent, and the newest is sent again, same bytes (it may never
\* have gone out). Mutation RestartForgetsSent treats them as never sent;
\* RestartReopens rebuilds a finished machine as well."
RestartRebuild ==
    /\ view' = [t \in Txs |-> IF view[t] = "deep" THEN "deep" ELSE "none"]
    /\ sent' = [t \in Txs |-> IF st[t] = "live"
                              THEN Mutation # "RestartForgetsSent" ELSE sent[t]]
    /\ LET live(i) == {t[2] : t \in {u \in Of(i) : st[u] = "live"}} IN
       /\ cur' = [i \in Intents |-> IF live(i) = {} THEN cur[i] ELSE Max(live(i))]
       /\ ms' = [i \in Intents |->
                   IF ms[i] \in Final /\ Mutation # "RestartReopens" THEN ms[i]
                   ELSE IF live(i) = {} THEN "build"
                   ELSE "submit"]
    /\ UNCHANGED <<truth, st, grp, mist, aview, tries, inj, fixed, vd, diag, total, ever>>

\* In English: "The engine restarts from its checkpoint and log (design A
\* of specs/engine-recovery): the records, the view and every machine are
\* as they were, since each was rebuilt from logged inputs. The one thing a
\* crash can lose is the answer to a send that was decided: the bytes may
\* have reached the node (Interrupted). So the attempt of a machine waiting
\* to send counts as sent (the Sender's resumed intents,
\* SubmitResult.Resend). Mutation RestartForgetsSent drops that mark."
Interrupted(t) == st[t] = "live" /\ ms[t[1]] = "submit" /\ t[2] = cur[t[1]]
RestartReplay ==
    /\ \E arrived \in [Txs -> BOOLEAN] :
         /\ \A t \in Txs : arrived[t] => Interrupted(t) /\ Fails(t) = {}
         /\ reached' = [t \in Txs |-> reached[t] \/ arrived[t]]
         /\ inmp' = [t \in Txs |-> inmp[t] \/ arrived[t]]
    /\ sent' = [t \in Txs |->
                   IF Interrupted(t)
                   THEN sent[t] \/ Mutation # "RestartForgetsSent"
                   ELSE sent[t]]
    /\ UNCHANGED <<chain, exp, adv, st, grp, mist, view, aview, machine>>

Restart ==
    /\ n.restart < MaxRestarts
    /\ n' = [n EXCEPT !.restart = @ + 1]
    /\ IF Recovery = "rebuild" THEN RestartRebuild ELSE RestartReplay

-----------------------------------------------------------------------------
\* The environment: anything the ledger allows.

\* In English: "A block includes t: it reached a node, carries no mistake,
\* has not expired, and its input is unspent on the chain. Conflicting
\* transactions leave the mempool. The planner's record is irrelevant: a
\* written-off attempt that can land, lands."
Include(t) ==
    /\ reached[t] /\ chain[t] = "none" /\ ~exp[t] /\ mist[t] = {}
    /\ ~adv[AdvOf(t)]
    /\ \A u \in Mates(t) : chain[u] = "none"
    /\ chain' = [chain EXCEPT ![t] = "block"]
    /\ inmp' = [u \in Txs |-> IF u = t \/ u \in Mates(t) THEN FALSE ELSE inmp[u]]
    /\ UNCHANGED <<reached, exp, adv, planner, machine, n>>

\* In English: "t's block becomes K deep and can no longer be rolled back."
Bury(t) ==
    /\ chain[t] = "block"
    /\ chain' = [chain EXCEPT ![t] = "deep"]
    /\ UNCHANGED <<reached, inmp, exp, adv, planner, machine, n>>

\* In English: "t's block is rolled back. t stays known to the network and
\* may be included again, or never."
Rollback(t) ==
    /\ n.roll < MaxRollbacks
    /\ chain[t] = "block"
    /\ chain' = [chain EXCEPT ![t] = "none"]
    /\ n' = [n EXCEPT !.roll = @ + 1]
    /\ UNCHANGED <<reached, inmp, exp, adv, planner, machine>>

\* In English: "The local mempool evicts t. Other nodes may still hold it."
Evict(t) ==
    /\ n.evict < MaxEvictions
    /\ inmp[t]
    /\ inmp' = [inmp EXCEPT ![t] = FALSE]
    /\ n' = [n EXCEPT !.evict = @ + 1]
    /\ UNCHANGED <<reached, chain, exp, adv, planner, machine>>

\* In English: "Somebody else spends group g's input of intent i, and it is
\* K deep at once: contention at a shared output."
Steal(a) ==
    /\ n.adv < MaxAdv
    /\ ~adv[a]
    /\ \A t \in Of(a[1]) : grp[t] = a[2] => chain[t] = "none"
    /\ adv' = [adv EXCEPT ![a] = TRUE]
    /\ inmp' = [t \in Txs |-> IF t[1] = a[1] /\ grp[t] = a[2] THEN FALSE ELSE inmp[t]]
    /\ n' = [n EXCEPT !.adv = @ + 1]
    /\ UNCHANGED <<reached, chain, exp, planner, machine>>

\* In English: "The run is over, in the state the model means to end in:
\* every intent is done or given up, and every attempt is settled, written
\* off or never built. (Done(i) is the machine's own step to "done", so the
\* terminal is called Finished.) Any other state with no step is a stuck
\* machine or planner, which TLC's deadlock check reports."
Finished ==
    /\ \A i \in Intents : ms[i] \in Final
    /\ \A t \in Txs : st[t] # "live"
    /\ UNCHANGED vars

Next ==
    \/ \E i \in Intents : Machine(i)
    \/ \E t \in Txs : Sync(t) \/ Expire(t) \/ Include(t) \/ Bury(t)
                      \/ Rollback(t) \/ Evict(t)
    \/ \E a \in Adv : SyncAdv(a) \/ Steal(a)
    \/ Restart
    \/ Finished

\* In English: "The machine, the planner and time keep moving; burying a
\* block is fair too. The node need never include anything, and the
\* bounded environment steps need never happen."
Fairness ==
    /\ \A i \in Intents : WF_vars(Machine(i))
    /\ \A t \in Txs : WF_vars(Sync(t)) /\ WF_vars(Expire(t)) /\ WF_vars(Bury(t))
    /\ \A a \in Adv : WF_vars(SyncAdv(a))

Spec == Init /\ [][Next]_vars
FairSpec == Spec /\ Fairness

-----------------------------------------------------------------------------
\* Invariants: true in every reachable state of the unmutated model.

TypeOK ==
    /\ reached \in [Txs -> BOOLEAN] /\ inmp \in [Txs -> BOOLEAN]
    /\ chain \in [Txs -> Depths] /\ exp \in [Txs -> BOOLEAN]
    /\ adv \in [Adv -> BOOLEAN] /\ aview \in [Adv -> BOOLEAN]
    /\ st \in [Txs -> TStates] /\ grp \in [Txs -> 0..2]
    /\ mist \in [Txs -> SUBSET Mistakes] /\ sent \in [Txs -> BOOLEAN]
    /\ view \in [Txs -> Depths]
    /\ ms \in [Intents -> MStates] /\ cur \in [Intents -> Slots]
    /\ tries \in [Intents -> 0..(Patience + 1)]
    /\ total \in [Intents -> 0..(Patience + 1)]
    /\ inj \in [Intents -> SUBSET Mistakes] /\ fixed \in [Intents -> SUBSET Mistakes]
    /\ ever \in [Intents -> SUBSET Mistakes]
    /\ vd \in [Intents -> SUBSET Classes] /\ diag \in [Intents -> Classes \cup {"none"}]

\* In English: "No intent is paid twice: the chain never holds two of its
\* attempts, even in blocks that may still roll back."
IntentAtMostOnce ==
    \A i \in Intents : \A t, u \in Of(i) :
        t # u => ~(chain[t] # "none" /\ chain[u] # "none")

\* In English: "An attempt the planner wrote off can truly never land, so
\* releasing its inputs is safe. A drop, a rollback, a transport error, an
\* 'inputs spent' answer to a resubmit and giving up prove nothing."
DeadIsDead ==
    \A t \in Txs : st[t] = "dead" => ~CanLand(t)

\* In English: "What the planner calls settled is in the immovable chain."
SettledIsFinal ==
    \A t \in Txs : st[t] = "settled" => chain[t] = "deep"

\* In English: "No actor submits more often than its patience allows, over
\* rollbacks and restarts."
AttemptsBounded ==
    \A i \in Intents : total[i] <= Patience

\* In English: "Whatever the node answers the attempt about to be sent, in
\* any rule order, the oracle expects it."
OracleSound ==
    \A i \in Intents : LET t == <<i, cur[i]>> IN
        (ms[i] = "submit" /\ st[t] = "live") =>
            \A v \in NodeVerdicts(t) : OracleExpects(t, v)

\* In English: "The record slots are enough: the bound never blocks a
\* build the actor's patience allows."
SlotsSuffice ==
    \A i \in Intents : (ms[i] = "build" /\ tries[i] < Patience) => Free(i) # {}

\* Properties of behaviours, checked under FairSpec.

\* In English: "A mistake once fixed never comes back: no attempt is built
\* carrying a mistake the actor has ever fixed. (Sending an earlier
\* attempt's bytes again, as after a restart, is not a build.)"
NoRegression ==
    [][\A t \in Txs :
         (st[t] # "live" /\ st'[t] = "live") => mist'[t] \cap ever[t[1]] = {}]_vars

\* In English: "Giving up and being done are final."
FinalIsFinal ==
    [][\A i \in Intents : ms[i] \in Final => ms'[i] = ms[i]]_vars

\* In English: "Every intent ends: done or given up."
Terminates == \A i \in Intents : <>[](ms[i] \in Final)

\* In English: "Every attempt ends: settled or written off. An intent the
\* actor gave up on is finished by the planner's rules alone."
AttemptsEnd ==
    \A t \in Txs : [](st[t] = "live" => <>(st[t] \in {"settled", "dead"}))

-----------------------------------------------------------------------------
\* Probes. Each says a state is unreachable; its *Bug.cfg must find it.

\* In English: "Nobody ever gives up."
ProbeNoGiveUp == \A i \in Intents : ms[i] # "gaveup"
\* In English: "An intent given up on is never paid afterwards."
ProbeNoLateSettle ==
    \A i \in Intents : ms[i] = "gaveup" => \A t \in Of(i) : st[t] # "settled"
\* In English: "A resubmit is never answered 'inputs spent' while its own
\* bytes sit in a block."
ProbeNoResubmitMempoolFailure ==
    \A i \in Intents : LET t == <<i, cur[i]>> IN
        ~(ms[i] = "rejected" /\ vd[i] = {"MempoolFailure"} /\ chain[t] # "none")
\* In English: "The actor never misdiagnoses and still gets paid."
ProbeNoMisdiagnosedPay ==
    \A i \in Intents : ~(ms[i] = "done" /\ diag[i] \notin vd[i] /\ diag[i] # "none")
\* In English: "With two mistakes the node never names only the second."
ProbeNoOrderSplit ==
    \A i \in Intents : ~(ms[i] = "rejected" /\ vd[i] = {"MissingSigner"}
                         /\ "Fee" \in mist[<<i, cur[i]>>])
\* In English: "An attempt rebuilt on a fresh input group never settles."
ProbeNoFreshGroupSettles ==
    \A t \in Txs : ~(st[t] = "settled" /\ grp[t] = 2)
=============================================================================
