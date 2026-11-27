------------------------ MODULE PlannerReservations ------------------------
(***************************************************************************)
(* The planner's reservation store.                                        *)
(*                                                                         *)
(* Models cardano-continuum at 243b05a; README.md has the code map and     *)
(* the drift table.                                                        *)
(*   Offer        internal/plan/ledger.go:112   Ledger.Available           *)
(*                internal/plan/state.go:38     state.available            *)
(*   Build        internal/plan/ledger.go:122   Ledger.Reserve             *)
(*                internal/plan/state.go:98     state.reserve              *)
(*   Persist      internal/plan/ledger.go:73    Ledger.commit              *)
(*   Process      internal/plan/ledger.go:134   Ledger.Advance             *)
(*                internal/plan/state.go:182,197  state.forward, rollback  *)
(*   SubmitFails  internal/plan/ledger.go:171   Ledger.Refused             *)
(*   ObserveDrop  internal/plan/ledger.go:193   Ledger.DropNotice          *)
(*   Restart      internal/plan/ledger.go:60    Open (decode), ResumePoint *)
(*   Deliver      internal/engine/internal/pay/retry.go:121  Retry.Answer  *)
(*                                                                         *)
(* The planner decides which outputs a transaction may spend. It holds a   *)
(* reservation on every output an unfinished transaction spends, and it    *)
(* chains: a transaction may spend the change output of one that is not    *)
(* confirmed yet. It learns about the chain late (a queue of roll-forward  *)
(* and roll-backward events) and about the mempool only by a notice that a *)
(* transaction is no longer there, which is also what it sees when the     *)
(* transaction has just been put in a block.                               *)
(*                                                                         *)
(* A payment (an "intent") may be attempted several times. Cardano has no  *)
(* nonces: two attempts that spend different outputs are two payments.     *)
(*                                                                         *)
(* The model answers one question: what is the weakest rule for holding    *)
(* and releasing reservations, and for building a second attempt, under    *)
(* which the planner never races itself, never pays twice, never leaks a   *)
(* reservation and never builds on an output that was rolled back? The     *)
(* rule the unmutated model states is:                                     *)
(*   1. an attempt holds its inputs until it is SETTLED (K blocks deep in  *)
(*      the planner's view) or DOOMED (it provably can never be included:  *)
(*      an input is spent by something else K deep, or the K-deep tip has  *)
(*      passed its time-to-live, or it was refused before reaching any     *)
(*      node, or a transaction it builds on is doomed);                    *)
(*   2. a drop notice releases nothing;                                    *)
(*   3. a second attempt of an intent must be unable to land together with *)
(*      every unfinished earlier attempt (they share an input, or build on *)
(*      transactions that do);                                             *)
(*   4. an attempt is on disk before it is handed to a node, and a restart *)
(*      resumes the view no earlier than the settled point.                *)
(* Each value of Mutation replaces one part of that rule with a simpler    *)
(* one a Go planner would plausibly use, and a *Bug.cfg shows the          *)
(* invariant it breaks. The README lists them.                             *)
(*                                                                         *)
(* Outputs carry no value: fees, minimum outputs and balancing are not     *)
(* this model's concern. A block holds one transaction. Slots are block    *)
(* positions.                                                              *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see specs/hello/Hello.tla.                     *)
(***************************************************************************)
EXTENDS Integers, FiniteSets, Sequences

CONSTANTS
    NumGenesis,       \* how many outputs exist before the planner starts
    MaxTx,            \* how many transactions the planner builds
    NumIntents,       \* how many payments it tries to make
    K,                \* the security parameter: a rollback undoes at most K blocks
    TtlDelta,         \* a transaction built at view length n is valid up to block n + TtlDelta
    MaxChain,         \* the longest chain the node grows
    MaxIns,           \* the most inputs one transaction spends
    MaxRollbacks,     \* how many times the node rolls back
    MaxAdv,           \* how many outputs somebody else spends
    MaxDrops,         \* how many times the mempool evicts a transaction
    MaxFails,         \* how many submissions end in an error
    MaxRestarts,      \* how many times the engine restarts
    RebuildNeedsDrop, \* TRUE: a second attempt is only built after a drop notice
    Mutation          \* "none", or one deliberate mistake (see the README)

\* The outputs that exist at the start. They are numbers above 100 so that
\* they never collide with a transaction's number.
\* Literally: "Genesis is defined as 101 through 100 plus NumGenesis."
Genesis == 101..(100 + NumGenesis)
\* Literally: "Txs is defined as 1 through MaxTx."
Txs == 1..MaxTx
\* Literally: "Intents is defined as 1 through NumIntents."
Intents == 1..NumIntents
\* Every output there can be. Transaction t makes one output the planner
\* can spend again, its change, and that output is called t too.
\* Literally: "Outs is defined as the union of Genesis and Txs."
Outs == Genesis \cup Txs
\* What a block can hold: planner transaction t, or somebody else's
\* transaction spending genesis output g, written as g.
\* Literally: "Items is defined as the union of Txs and Genesis."
Items == Txs \cup Genesis

\* The states of a planner transaction, as the planner records them.
\*   unused:   not built yet
\*   volatile: built and reserved, but only in memory (mutation PersistLate)
\*   live:     on disk, reserved, possibly at a node, not finished
\*   settled:  K deep in the view; finished, it paid
\*   dead:     the planner has written it off; finished, it did not pay
\*   lost:     a restart forgot it (mutation PersistLate)
\* Literally: "States is defined as the set containing "unused", "volatile",
\*   "live", "settled", "dead" and "lost"."
States == {"unused", "volatile", "live", "settled", "dead", "lost"}
\* In English: "The states in which the planner still counts on a transaction."
\* Literally: "Unfinished is defined as the set containing "volatile" and
\*   "live"."
Unfinished == {"volatile", "live"}

VARIABLES
    \* the node and the network (the truth)
    chain,      \* the node's chain: a sequence of Items, one per block
    floor,      \* how many blocks from the start can never be rolled back
    network,    \* planner transactions that reached a node: any may be put in a block
    mempool,    \* planner transactions in the local node's mempool
    events,     \* chain-sync events the planner has not processed yet
    n,          \* how many of each bounded environment step have happened
    \* the planner
    tx,         \* tx[t]: the record [ins, intent, ttl, st] of transaction t
    res,        \* res[o]: the intent holding output o, or 0
    view,       \* the chain as far as the planner has processed it
    vfloor,     \* how many blocks of the view are settled; never decreases
    hint        \* transactions the planner has had a drop notice for

\* Literally: "vars is defined as the tuple of chain, floor, network, mempool,
\*   events, n, tx, res, view, vfloor and hint."
vars == <<chain, floor, network, mempool, events, n, tx, res, view, vfloor, hint>>

-----------------------------------------------------------------------------
\* Helpers.

\* Literally: "Max of a and b is defined as: if a is greater than or equal to
\*   b then a, else b."
Max(a, b) == IF a >= b THEN a ELSE b
\* In English: "The smallest number in S."
\* Literally: "Least of S is defined as the x, chosen from S, such that for
\*   all y in S, x is less than or equal to y."
Least(S) == CHOOSE x \in S : \A y \in S : x <= y
\* In English: "The largest number in S."
\* Literally: "Greatest of S is defined as the x, chosen from S, such that
\*   for all y in S, x is greater than or equal to y."
Greatest(S) == CHOOSE x \in S : \A y \in S : x >= y
\* In English: "The first m blocks of s."
\* Literally: "Prefix of s and m is defined as the subsequence of s from
\*   element 1 through element m."
Prefix(s, m) == SubSeq(s, 1, m)
\* In English: "The set of items somewhere in s."
\* Literally: "Blocks of s is defined as the set of element j of s, for every
\*   j that is a member of 1 through the length of s."
Blocks(s) == {s[j] : j \in 1..Len(s)}
\* In English: "The outputs block item b spends, reading transaction bodies
\* from f: a planner transaction spends its inputs, and somebody else's
\* transaction g spends genesis output g."
\* Literally: "SpentBy of f and b is defined as: if b is a member of Txs then
\*   f of b's ins, else the set containing b."
SpentBy(f, b) == IF b \in Txs THEN f[b].ins ELSE {b}
\* In English: "Every output spent somewhere in s."
\* Literally: "Spent of f and s is defined as the union of all the sets in
\*   the set of SpentBy of f and element j of s, for every j that is a member
\*   of 1 through the length of s."
Spent(f, s) == UNION {SpentBy(f, s[j]) : j \in 1..Len(s)}
\* In English: "The outputs that exist and are unspent after chain s: the
\* genesis outputs and the change of every planner transaction in s, less
\* whatever s spends."
\* Literally: "Utxo of f and s is defined as the union of Genesis and the
\*   intersection of Blocks of s and Txs, without the members of Spent of f
\*   and s."
Utxo(f, s) == (Genesis \cup (Blocks(s) \cap Txs)) \ Spent(f, s)

\* In English: "Transaction t and every transaction it builds on, directly
\* or through others. A transaction only spends outputs of transactions
\* built before it, so this ends."
\* Literally: "Anc of t is defined as the union of the set containing t and
\*   the union of all the sets in the set of Anc of p, for every p that is a
\*   member of the intersection of tx of t's ins and Txs."
RECURSIVE Anc(_)
Anc(t) == {t} \cup UNION {Anc(p) : p \in tx[t].ins \cap Txs}

\* In English: "A new transaction spending ins can never be on one chain
\* together with transaction p: it, or something it builds on, shares an
\* input with p or with something p builds on. The ledger includes at most
\* one of two transactions that share an input, so at most one of the new
\* transaction and p can ever land."
\* Literally: "Excludes of ins and p is defined as: letting mine be the union
\*   of all the sets in the set of Anc of q, for every q that is a member of
\*   the intersection of ins and Txs: there exists a y in Anc of p such that
\*   the intersection of ins and tx of y's ins does not equal the empty set,
\*   or there exists an x in mine such that x does not equal y and the
\*   intersection of tx of x's ins and tx of y's ins does not equal the empty
\*   set."
Excludes(ins, p) ==
    LET mine == UNION {Anc(q) : q \in ins \cap Txs}
    IN  \E y \in Anc(p) :
            \/ ins \cap tx[y].ins # {}
            \/ \E x \in mine : x # y /\ tx[x].ins \cap tx[y].ins # {}

\* In English: "A new transaction spending ins could land: no two different
\* transactions among it and everything it builds on share an input.
\* Without this the planner could build on two attempts of which only one
\* can ever be on the chain. No invariant below needs it (they hold with
\* the line removed); it only keeps the planner from reserving outputs for
\* a transaction that is stillborn."
\* Literally: "Coherent of ins is defined as: letting mine be the union of
\*   all the sets in the set of Anc of q, for every q that is a member of the
\*   intersection of ins and Txs: for all x in mine, the intersection of ins
\*   and tx of x's ins equals the empty set, and for all y in mine, if x does
\*   not equal y then the intersection of tx of x's ins and tx of y's ins
\*   equals the empty set."
Coherent(ins) ==
    LET mine == UNION {Anc(q) : q \in ins \cap Txs}
    IN  \A x \in mine :
            /\ ins \cap tx[x].ins = {}
            /\ \A y \in mine : x # y => tx[x].ins \cap tx[y].ins = {}

\* In English: "Judged from view v with vf settled blocks and the records f,
\* transaction t can never be included:
\*   - one of its inputs is spent by something else in a settled block, or
\*   - the settled tip has passed its time-to-live and it is not in a
\*     settled block, or
\*   - a transaction it builds on is dead, lost, or doomed itself.
\* m is the mutation in force: three of them weaken one line each."
\* Literally: "Doomed of m, f, v, vf and t is defined as: letting deep be, if
\*   m equals "DeadOnUnsettledConflict" then the length of v, else vf: there
\*   exists an o in f of t's ins such that there exists a j in 1 through deep
\*   such that element j of v does not equal t and o is a member of SpentBy of
\*   f and element j of v; or, if m equals "ExpireAtTip" then the length of v
\*   is greater than or equal to f of t's ttl and t is not a member of Blocks
\*   of v, else vf is greater than or equal to f of t's ttl and t is not a
\*   member of Blocks of Prefix of v and vf; or m does not equal "NoCascade"
\*   and there exists a p in the intersection of f of t's ins and Txs such
\*   that f of p's st is a member of the set containing "dead" and "lost", or
\*   f of p's st is a member of Unfinished and Doomed of m, f, v, vf and p."
RECURSIVE Doomed(_, _, _, _, _)
Doomed(m, f, v, vf, t) ==
    LET deep == IF m = "DeadOnUnsettledConflict" THEN Len(v) ELSE vf
    IN  \/ \E o \in f[t].ins : \E j \in 1..deep :
               v[j] # t /\ o \in SpentBy(f, v[j])
        \/ IF m = "ExpireAtTip"
           THEN Len(v) >= f[t].ttl /\ t \notin Blocks(v)
           ELSE vf >= f[t].ttl /\ t \notin Blocks(Prefix(v, vf))
        \/ /\ m # "NoCascade"
           /\ \E p \in f[t].ins \cap Txs :
                  \/ f[p].st \in {"dead", "lost"}
                  \/ f[p].st \in Unfinished /\ Doomed(m, f, v, vf, p)

\* In English: "The records after the planner looks at view v with vf
\* settled blocks: an unfinished transaction in a settled block becomes
\* settled, an unfinished doomed one becomes dead, the rest stay. Mutation
\* SettleAtTip counts any block of the view as settled."
\* Literally: "Reap of m, f, v and vf is defined as: letting deep be, if m
\*   equals "SettleAtTip" then the length of v, else vf: the function that
\*   maps every t in Txs to: if f of t's st is not a member of Unfinished then
\*   f of t, else if t is a member of Blocks of Prefix of v and deep then f of
\*   t except that its st field is "settled", else if Doomed of m, f, v, vf
\*   and t then f of t except that its st field is "dead", else f of t."
Reap(m, f, v, vf) ==
    LET deep == IF m = "SettleAtTip" THEN Len(v) ELSE vf
    IN  [t \in Txs |->
            IF f[t].st \notin Unfinished THEN f[t]
            ELSE IF t \in Blocks(Prefix(v, deep))
                 THEN [f[t] EXCEPT !.st = "settled"]
            ELSE IF Doomed(m, f, v, vf, t)
                 THEN [f[t] EXCEPT !.st = "dead"]
            ELSE f[t]]

\* In English: "The reservations once the records are f: an output stays
\* reserved exactly while some unfinished transaction of the holding intent
\* spends it."
\* Literally: "Released of f is defined as the function that maps every o in
\*   Outs to: if there exists a t in Txs such that f of t's st is a member of
\*   Unfinished and f of t's intent equals res of o and o is a member of f of
\*   t's ins, then res of o, else 0."
Released(f) ==
    [o \in Outs |->
        IF \E t \in Txs : /\ f[t].st \in Unfinished
                          /\ f[t].intent = res[o]
                          /\ o \in f[t].ins
        THEN res[o] ELSE 0]

\* In English: "A chain-sync event: block b was added, or the chain was cut
\* back to m blocks."
\* Literally: "Fwd of b is defined as the record whose field kind is "fwd"
\*   and field arg is b."
Fwd(b) == [kind |-> "fwd", arg |-> b]
\* Literally: "Back of m is defined as the record whose field kind is "back"
\*   and field arg is m."
Back(m) == [kind |-> "back", arg |-> m]

-----------------------------------------------------------------------------
\* In English: "At the start the chain and the view are empty, nothing has
\* been built or reserved, and no environment step has been used."
\* Literally: "Init is defined as: chain equals the empty sequence, and floor
\*   equals 0, and network equals the empty set, and mempool equals the empty
\*   set, and events equals the empty sequence, and n equals the record whose
\*   fields roll, adv, drop, fail and restart are 0, and tx equals the
\*   function that maps every t in Txs to the record whose field ins is the
\*   empty set, field intent is 0, field ttl is 0 and field st is "unused",
\*   and res equals the function that maps every o in Outs to 0, and view
\*   equals the empty sequence, and vfloor equals 0, and hint equals the
\*   empty set."
Init ==
    /\ chain = <<>>
    /\ floor = 0
    /\ network = {}
    /\ mempool = {}
    /\ events = <<>>
    /\ n = [roll |-> 0, adv |-> 0, drop |-> 0, fail |-> 0, restart |-> 0]
    /\ tx = [t \in Txs |-> [ins |-> {}, intent |-> 0, ttl |-> 0, st |-> "unused"]]
    /\ res = [o \in Outs |-> 0]
    /\ view = <<>>
    /\ vfloor = 0
    /\ hint = {}

-----------------------------------------------------------------------------
\* The planner.

\* In English: "The outputs the planner may hand to intent i: unspent in the
\* view, or the change of an unfinished transaction the view does not hold
\* yet (chaining), and in either case free or already held by i itself."
\* Literally: "Offer of i is defined as the set of every o in the union of
\*   Utxo of tx and view and the set of every p in Txs such that tx of p's st
\*   is a member of Unfinished and p is not a member of Blocks of view, such
\*   that res of o is a member of the set containing 0 and i."
Offer(i) ==
    {o \in Utxo(tx, view) \cup
           {p \in Txs : tx[p].st \in Unfinished /\ p \notin Blocks(view)} :
        res[o] \in {0, i}}

\* In English: "The planner builds the next transaction, for intent i,
\* spending ins, and reserves ins for i. The intent must not have paid
\* already. If the intent has unfinished attempts (prior), this is a second
\* attempt: it must be unable to land together with each of them. The
\* inputs must not contradict each other (Coherent). Mutation
\* FreshInputs drops that; ConflictWithLatestOnly asks it of the newest one
\* only. With RebuildNeedsDrop a second attempt also waits for a drop
\* notice. The record is on disk at once, except under PersistLate.
\* Mutation PartialReserve reserves only the first of several inputs."
\* Literally: "Build of i and ins is defined as: letting free be the set of
\*   every u in Txs such that tx of u's st equals "unused", and prior be the
\*   set of every p in Txs such that tx of p's intent equals i and tx of p's
\*   st is a member of Unfinished: free does not equal the empty set; and for
\*   all p in Txs, if tx of p's intent equals i then tx of p's st does not
\*   equal "settled"; and i equals 1 or there exists a p in Txs such that tx
\*   of p's intent equals i minus 1; and ins does not equal the empty set and
\*   ins is a subset of Offer of i and the number of members of ins is less
\*   than or equal to MaxIns; and Coherent of ins; and if prior does not
\*   equal the empty set, then
\*   if RebuildNeedsDrop then the intersection of prior and hint does not
\*   equal the empty set, and in the case Mutation equals "FreshInputs", TRUE;
\*   in the case Mutation equals "ConflictWithLatestOnly", Excludes of ins
\*   and Greatest of prior; in every other case, for all p in prior, Excludes
\*   of ins and p; and tx in the next state equals tx except that its Least
\*   of free entry is the record whose field ins is ins, field intent is i,
\*   field ttl is the length of view plus TtlDelta, and field st is, if
\*   Mutation equals "PersistLate" then "volatile", else "live"; and res in
\*   the next state equals the function that maps every o in Outs to, if o is
\*   a member of ins then i, else res of o; and the tuple of chain, floor,
\*   network, mempool, events, n, view, vfloor and hint is unchanged."
\* The third line only fixes the order intents are first used in: intents
\* are interchangeable, so nothing is lost.
Build(i, ins) ==
    LET free  == {u \in Txs : tx[u].st = "unused"}
        prior == {p \in Txs : tx[p].intent = i /\ tx[p].st \in Unfinished}
    IN  /\ free # {}
        /\ \A p \in Txs : tx[p].intent = i => tx[p].st # "settled"
        /\ i = 1 \/ \E p \in Txs : tx[p].intent = i - 1
        /\ ins # {} /\ ins \subseteq Offer(i) /\ Cardinality(ins) <= MaxIns
        /\ Coherent(ins)
        /\ prior # {} =>
              /\ RebuildNeedsDrop => prior \cap hint # {}
              /\ CASE Mutation = "FreshInputs" -> TRUE
                   [] Mutation = "ConflictWithLatestOnly" ->
                          Excludes(ins, Greatest(prior))
                   [] OTHER -> \A p \in prior : Excludes(ins, p)
        /\ tx' = [tx EXCEPT ![Least(free)] =
                     [ins |-> ins, intent |-> i, ttl |-> Len(view) + TtlDelta,
                      st |-> IF Mutation = "PersistLate" THEN "volatile"
                             ELSE "live"]]
        /\ res' = [o \in Outs |->
                     IF o \in ins /\ (Mutation # "PartialReserve" \/ o = Least(ins))
                     THEN i ELSE res[o]]
        /\ UNCHANGED <<chain, floor, network, mempool, events, n, view, vfloor, hint>>

\* In English: "Under PersistLate only: the record of a transaction built in
\* memory reaches the disk."
\* Literally: "Persist of t is defined as: tx of t's st equals "volatile",
\*   and tx in the next state equals tx except that its t entry's st field is
\*   "live", and the tuple of chain, floor, network, mempool, events, n, res,
\*   view, vfloor and hint is unchanged."
Persist(t) ==
    /\ tx[t].st = "volatile"
    /\ tx' = [tx EXCEPT ![t].st = "live"]
    /\ UNCHANGED <<chain, floor, network, mempool, events, n, res, view, vfloor, hint>>

\* In English: "Transaction t reaches a node: from now on any block may
\* hold it, whatever the local mempool does. Only a transaction on disk is
\* handed over, except under PersistLate."
\* Literally: "Deliver of t is defined as: tx of t's st equals "live", or
\*   Mutation equals "PersistLate" and tx of t's st equals "volatile"; and t
\*   is not a member of network; and network in the next state equals the
\*   union of network and the set containing t; and mempool in the next state
\*   equals the union of mempool and the set containing t; and the tuple of
\*   chain, floor, events, n, tx, res, view, vfloor and hint is unchanged."
Deliver(t) ==
    /\ \/ tx[t].st = "live"
       \/ Mutation = "PersistLate" /\ tx[t].st = "volatile"
    /\ t \notin network
    /\ network' = network \cup {t}
    /\ mempool' = mempool \cup {t}
    /\ UNCHANGED <<chain, floor, events, n, tx, res, view, vfloor, hint>>

\* In English: "The submission of t ends in an error and the planner writes
\* t off, with everything built on it, and releases what they held. This is
\* sound only when t never reached a node (a refusal, or giving up before
\* sending). Mutation ReleaseOnSubmitError also does it when t did reach
\* one, as after a timeout or a broken connection."
\* Literally: "SubmitFails of t is defined as: tx of t's st equals "live";
\*   and t is not a member of network or Mutation equals
\*   "ReleaseOnSubmitError"; and n's fail is less than MaxFails; and n in the
\*   next state equals n except that its fail field is the old entry plus 1;
\*   and tx in the next state equals Reap of Mutation, tx except that its t
\*   entry's st field is "dead", view and vfloor; and res in the next state
\*   equals Released of tx in the next state; and the tuple of chain, floor,
\*   network, mempool, events, view, vfloor and hint is unchanged."
SubmitFails(t) ==
    /\ tx[t].st = "live"
    /\ t \notin network \/ Mutation = "ReleaseOnSubmitError"
    /\ n.fail < MaxFails
    /\ n' = [n EXCEPT !.fail = @ + 1]
    /\ tx' = Reap(Mutation, [tx EXCEPT ![t].st = "dead"], view, vfloor)
    /\ res' = Released(tx')
    /\ UNCHANGED <<chain, floor, network, mempool, events, view, vfloor, hint>>

\* In English: "The planner notices that t, which it sent, is neither in
\* the mempool nor in its view. That is true when the mempool evicted t and
\* equally when a block the planner has not processed yet holds t. The
\* notice is remembered and changes nothing else. Mutation ReleaseOnDrop
\* treats it as the end of t: t and everything built on it are written off
\* and their inputs released."
\* Literally: "ObserveDrop of t is defined as: RebuildNeedsDrop or Mutation
\*   equals "ReleaseOnDrop"; and tx of t's st equals "live"; and t is a member
\*   of network; and t is not a member of mempool; and t is not a member of
\*   Blocks of view; and t is not a member of hint; and hint in the next state
\*   equals the union of hint and the set containing t; and if Mutation
\*   equals "ReleaseOnDrop" then tx in the next state equals Reap of
\*   Mutation, tx except that its t entry's st field is "dead", view and
\*   vfloor, and res in the next state equals Released of tx in the next
\*   state, else the tuple of tx and res is unchanged; and the tuple of
\*   chain, floor, network, mempool, events, n, view and vfloor is unchanged."
\* The first line switches the action off where a notice has no effect, so
\* TLC does not count states that differ only in notices nobody reads.
ObserveDrop(t) ==
    /\ RebuildNeedsDrop \/ Mutation = "ReleaseOnDrop"
    /\ tx[t].st = "live"
    /\ t \in network
    /\ t \notin mempool
    /\ t \notin Blocks(view)
    /\ t \notin hint
    /\ hint' = hint \cup {t}
    /\ IF Mutation = "ReleaseOnDrop"
       THEN /\ tx' = Reap(Mutation, [tx EXCEPT ![t].st = "dead"], view, vfloor)
            /\ res' = Released(tx')
       ELSE UNCHANGED <<tx, res>>
    /\ UNCHANGED <<chain, floor, network, mempool, events, n, view, vfloor>>

\* In English: "The planner processes the next chain-sync event: its view
\* gains a block or is cut back. The settled point moves up if the view is
\* now long enough, never down. Then it settles and writes off whatever the
\* new view decides, and releases what those transactions held. Mutation
\* SettledPointFollowsTip keeps the settled point K behind the view's tip,
\* so a rollback moves it back; SettleOneShallow puts it K - 1 behind."
\* Literally: "Process is defined as: events does not equal the empty
\*   sequence; and letting e be the head of events, and v be, if e's kind
\*   equals "fwd" then view with e's arg appended, else Prefix of view and e's
\*   arg, and vf be Max of, if Mutation equals "SettledPointFollowsTip" then
\*   0 else vfloor, and the length of v minus K: view in the
\*   next state equals v, and vfloor in the next state equals vf, and tx in
\*   the next state equals Reap of Mutation, tx, v and vf, and res in the next
\*   state equals Released of tx in the next state; and events in the next
\*   state equals the tail of events; and the tuple of chain, floor, network,
\*   mempool, n and hint is unchanged."
Process ==
    /\ events # <<>>
    /\ LET e  == Head(events)
           v  == IF e.kind = "fwd" THEN Append(view, e.arg)
                 ELSE Prefix(view, e.arg)
           vf == Max(IF Mutation = "SettledPointFollowsTip" THEN 0 ELSE vfloor,
                     Len(v) - (IF Mutation = "SettleOneShallow" THEN K - 1 ELSE K))
       IN  /\ view' = v
           /\ vfloor' = vf
           /\ tx' = Reap(Mutation, tx, v, vf)
           /\ res' = Released(tx')
    /\ events' = Tail(events)
    /\ UNCHANGED <<chain, floor, network, mempool, n, hint>>

\* In English: "The engine restarts. Records and reservations on disk
\* survive; a record only in memory is lost, with its reservations and all
\* drop notices. The view resumes from the settled point, and chain-sync
\* delivers the node's chain from there, so events in flight are replaced.
\* Mutation RestartFromOrigin rebuilds the view from the first block
\* instead."
\* Literally: "Restart is defined as: n's restart is less than MaxRestarts;
\*   and n in the next state equals n except that its restart field is the
\*   old entry plus 1; and letting kept be the function that maps every t in
\*   Txs to, if tx of t's st equals "volatile" then tx of t except that its st
\*   field is "lost", else tx of t, and vf be, if Mutation equals
\*   "RestartFromOrigin" then 0, else vfloor, and v be Prefix of view and vf:
\*   view in the next state equals v, and vfloor in the next state equals vf,
\*   and events in the next state equals the function that maps every j in 1
\*   through the length of chain minus vf to Fwd of element vf plus j of
\*   chain, and tx in the next state equals Reap of Mutation, kept, v and vf,
\*   and res in the next state equals Released of tx in the next state; and
\*   hint in the next state equals the empty set; and the tuple of chain,
\*   floor, network and mempool is unchanged."
Restart ==
    /\ n.restart < MaxRestarts
    /\ n' = [n EXCEPT !.restart = @ + 1]
    /\ LET kept == [t \in Txs |-> IF tx[t].st = "volatile"
                                  THEN [tx[t] EXCEPT !.st = "lost"] ELSE tx[t]]
           vf   == IF Mutation = "RestartFromOrigin" THEN 0 ELSE vfloor
           v    == Prefix(view, vf)
       IN  /\ view' = v
           /\ vfloor' = vf
           /\ events' = [j \in 1..(Len(chain) - vf) |-> Fwd(chain[vf + j])]
           /\ tx' = Reap(Mutation, kept, v, vf)
           /\ res' = Released(tx')
    /\ hint' = {}
    /\ UNCHANGED <<chain, floor, network, mempool>>

-----------------------------------------------------------------------------
\* The environment. It is an adversary: it does anything the ledger and the
\* security parameter allow.

\* In English: "The node adds a block holding item b. Whatever b spends must
\* be unspent on the chain. The immovable part of the chain grows to K
\* behind the new tip if that is further than before, and the planner will
\* hear of the block."
\* Literally: "Extend of b is defined as: the length of chain is less than
\*   MaxChain; and SpentBy of tx and b is a subset of Utxo of tx and chain;
\*   and chain in the next state equals chain with b appended; and floor in
\*   the next state equals Max of floor and the length of chain plus 1 minus
\*   K; and events in the next state equals events with Fwd of b appended;
\*   and mempool in the next state equals mempool without the members of the
\*   set containing b."
Extend(b) ==
    /\ Len(chain) < MaxChain
    /\ SpentBy(tx, b) \subseteq Utxo(tx, chain)
    /\ chain' = Append(chain, b)
    /\ floor' = Max(floor, Len(chain) + 1 - K)
    /\ events' = Append(events, Fwd(b))
    /\ mempool' = mempool \ {b}

\* In English: "A block includes planner transaction t. Any transaction
\* that ever reached a node can be included, in the mempool or not, as long
\* as it is not on the chain already and the block is within its
\* time-to-live."
\* Literally: "IncludeTx of t is defined as: t is a member of network; and t
\*   is not a member of Blocks of chain; and the length of chain plus 1 is
\*   less than or equal to tx of t's ttl; and Extend of t; and the tuple of
\*   network, n, tx, res, view, vfloor and hint is unchanged."
IncludeTx(t) ==
    /\ t \in network
    /\ t \notin Blocks(chain)
    /\ Len(chain) + 1 <= tx[t].ttl
    /\ Extend(t)
    /\ UNCHANGED <<network, n, tx, res, view, vfloor, hint>>

\* In English: "Somebody else's transaction spends genesis output g, as in
\* contention for a shared script output."
\* Literally: "AdvSpend of g is defined as: n's adv is less than MaxAdv; and
\*   Extend of g; and n in the next state equals n except that its adv field
\*   is the old entry plus 1; and the tuple of network, tx, res, view, vfloor
\*   and hint is unchanged."
AdvSpend(g) ==
    /\ n.adv < MaxAdv
    /\ Extend(g)
    /\ n' = [n EXCEPT !.adv = @ + 1]
    /\ UNCHANGED <<network, tx, res, view, vfloor, hint>>

\* In English: "The node rolls back d blocks, at most K and never into the
\* immovable part. Rolled-back transactions stay known to the network, so a
\* later block may include them again or never: the planner may rely on
\* neither."
\* Literally: "Rollback of d is defined as: n's roll is less than
\*   MaxRollbacks; and the length of chain minus d is greater than or equal to
\*   floor; and chain in the next state equals Prefix of chain and the length
\*   of chain minus d; and events in the next state equals events with Back
\*   of the length of chain minus d appended; and n in the next state equals
\*   n except that its roll field is the old entry plus 1; and the tuple of
\*   floor, network, mempool, tx, res, view, vfloor and hint is unchanged."
Rollback(d) ==
    /\ n.roll < MaxRollbacks
    /\ Len(chain) - d >= floor
    /\ chain' = Prefix(chain, Len(chain) - d)
    /\ events' = Append(events, Back(Len(chain) - d))
    /\ n' = [n EXCEPT !.roll = @ + 1]
    /\ UNCHANGED <<floor, network, mempool, tx, res, view, vfloor, hint>>

\* In English: "The local mempool evicts t. Other nodes may still hold it."
\* Literally: "MempoolDrop of t is defined as: t is a member of mempool; and
\*   n's drop is less than MaxDrops; and mempool in the next state equals
\*   mempool without the members of the set containing t; and n in the next
\*   state equals n except that its drop field is the old entry plus 1; and
\*   the tuple of chain, floor, network, events, tx, res, view, vfloor and
\*   hint is unchanged."
MempoolDrop(t) ==
    /\ t \in mempool
    /\ n.drop < MaxDrops
    /\ mempool' = mempool \ {t}
    /\ n' = [n EXCEPT !.drop = @ + 1]
    /\ UNCHANGED <<chain, floor, network, events, tx, res, view, vfloor, hint>>

-----------------------------------------------------------------------------
\* The truth, which the planner cannot see.

\* In English: "Transaction t can never be on the chain again, whatever the
\* node does from here: an input is spent by something else in the immovable
\* part; or the immovable part has passed its time-to-live without it; or it
\* never reached a node and the planner has given it up; or a transaction it
\* builds on can never land."
\* Literally: "CannotLand of t is defined as: there exists an o in tx of t's
\*   ins such that there exists a j in 1 through floor such that element j of
\*   chain does not equal t and o is a member of SpentBy of tx and element j
\*   of chain; or floor is greater than or equal to tx of t's ttl and t is not
\*   a member of Blocks of Prefix of chain and floor; or t is not a member of
\*   network and tx of t's st is a member of the set containing "dead" and
\*   "lost"; or there exists a p in the intersection of tx of t's ins and Txs
\*   such that CannotLand of p."
RECURSIVE CannotLand(_)
CannotLand(t) ==
    \/ \E o \in tx[t].ins : \E j \in 1..floor :
           chain[j] # t /\ o \in SpentBy(tx, chain[j])
    \/ floor >= tx[t].ttl /\ t \notin Blocks(Prefix(chain, floor))
    \/ t \notin network /\ tx[t].st \in {"dead", "lost"}
    \/ \E p \in tx[t].ins \cap Txs : CannotLand(p)

\* In English: "The run is over, in the state the model means to end in:
\* the planner has processed every chain-sync event, and for every
\* transaction it still counts on, waiting for the node is right. Either
\* the transaction could still land, or the node holds more of the chain
\* as immovable than the planner's view can show (vfloor < floor): after a
\* restart chain-sync replays only the current chain, so a block made
\* immovable by a longer chain that was since rolled back is settled only
\* once the chain grows again, which the bounded node may never do. Any
\* other state with no step, such as a caught-up planner holding a
\* transaction it could already know can never land, is a stuck planner,
\* which TLC's deadlock check reports."
Done ==
    /\ events = <<>>
    /\ \A t \in Txs : tx[t].st \in Unfinished => ~CannotLand(t) \/ vfloor < floor
    /\ UNCHANGED vars

-----------------------------------------------------------------------------
\* In English: "A step is one planner step or one environment step, or the
\* end of the run."
\* Literally: "Next is defined as: there exists an i in Intents and an ins in
\*   the set of subsets of Outs such that Build of i and ins; or there exists
\*   a t in Txs such that Persist of t, or Deliver of t, or SubmitFails of t,
\*   or ObserveDrop of t, or IncludeTx of t, or MempoolDrop of t; or there
\*   exists a g in Genesis such that AdvSpend of g; or there exists a d in 1
\*   through K such that Rollback of d; or Process; or Restart; or Done."
Next ==
    \/ \E i \in Intents, ins \in SUBSET Outs : Build(i, ins)
    \/ \E t \in Txs : \/ Persist(t)
                      \/ Deliver(t)
                      \/ SubmitFails(t)
                      \/ ObserveDrop(t)
                      \/ IncludeTx(t)
                      \/ MempoolDrop(t)
    \/ \E g \in Genesis : AdvSpend(g)
    \/ \E d \in 1..K : Rollback(d)
    \/ Process
    \/ Restart
    \/ Done

\* Literally: "Spec is defined as: Init holds at the start, and always, every
\*   step is a Next step or a step that leaves vars unchanged."
Spec == Init /\ [][Next]_vars

\* In English: "The planner keeps processing chain-sync events: the one
\* step that must make progress for a transaction to finish. Building,
\* sending and the node need no fairness."
Fairness == WF_vars(Process)

LiveSpec == Spec /\ Fairness

-----------------------------------------------------------------------------
\* Invariants: true in every reachable state of the unmutated model.

\* In English: "Every variable holds the kind of value it should."
\* Literally: "TypeOK is defined as: chain is a member of the set of
\*   sequences of members of Items and the length of chain is less than or
\*   equal to MaxChain; and floor is a member of 0 through MaxChain; and
\*   network is a subset of Txs; and mempool is a subset of network; and for
\*   all j in 1 through the length of events, element j of events's kind is a
\*   member of the set containing "fwd" and "back"; and tx is a member of the
\*   set of functions from Txs to the set of records whose ins is a subset of
\*   Outs, intent is a member of 0 through NumIntents, ttl is a member of 0
\*   through MaxChain plus TtlDelta and st is a member of States; and res is
\*   a member of the set of functions from Outs to 0 through NumIntents; and
\*   view is a member of the set of sequences of members of Items; and vfloor
\*   is a member of 0 through MaxChain; and hint is a subset of Txs."
TypeOK ==
    /\ chain \in Seq(Items) /\ Len(chain) <= MaxChain
    /\ floor \in 0..MaxChain
    /\ network \subseteq Txs
    /\ mempool \subseteq network
    /\ \A j \in 1..Len(events) : events[j].kind \in {"fwd", "back"}
    /\ tx \in [Txs -> [ins : SUBSET Outs, intent : 0..NumIntents,
                       ttl : 0..(MaxChain + TtlDelta), st : States]]
    /\ res \in [Outs -> 0..NumIntents]
    /\ view \in Seq(Items)
    /\ vfloor \in 0..MaxChain
    /\ hint \subseteq Txs

\* In English: "What the planner calls settled is the immovable start of
\* the node's chain: the two agree block for block. The rest of the model's
\* reasoning stands on this."
\* Literally: "SettledViewIsChain is defined as: vfloor is less than or equal
\*   to floor, and vfloor is less than or equal to the length of view, and for
\*   all j in 1 through vfloor, element j of view equals element j of chain."
SettledViewIsChain ==
    /\ vfloor <= floor
    /\ vfloor <= Len(view)
    /\ \A j \in 1..vfloor : view[j] = chain[j]

\* In English: "No double reservation: every input of an unfinished
\* transaction is reserved for that transaction's intent, so two intents
\* never count on one output."
\* Literally: "ReservedWhileUnfinished is defined as: for all t in Txs, if tx
\*   of t's st is a member of Unfinished, then for all o in tx of t's ins, res
\*   of o equals tx of t's intent."
ReservedWhileUnfinished ==
    \A t \in Txs : tx[t].st \in Unfinished =>
        \A o \in tx[t].ins : res[o] = tx[t].intent

\* In English: "No leaked reservation: every reservation is held for an
\* unfinished transaction that spends the output, is not settled, and is
\* not doomed by the unmutated rule. A reservation nobody can ever use up
\* is a leak."
\* Literally: "NoLeak is defined as: for all o in Outs, if res of o does not
\*   equal 0, then there exists a t in Txs such that tx of t's st is a member
\*   of Unfinished, and tx of t's intent equals res of o, and o is a member of
\*   tx of t's ins, and t is not a member of Blocks of Prefix of view and
\*   vfloor, and Doomed of "none", tx, view, vfloor and t is not true."
NoLeak ==
    \A o \in Outs : res[o] # 0 =>
        \E t \in Txs : /\ tx[t].st \in Unfinished
                       /\ tx[t].intent = res[o]
                       /\ o \in tx[t].ins
                       /\ t \notin Blocks(Prefix(view, vfloor))
                       /\ ~Doomed("none", tx, view, vfloor, t)

\* In English: "No spend of a rolled-back output: an unfinished transaction
\* only builds on transactions the planner still tracks (unfinished ones),
\* or on settled ones that are in its view. It never builds on a dead or
\* lost one, nor on one it stopped tracking that the view no longer holds."
\* Literally: "InputsTracked is defined as: for all t in Txs, if tx of t's st
\*   is a member of Unfinished, then for all p in the intersection of tx of
\*   t's ins and Txs, tx of p's st is a member of Unfinished, or tx of p's st
\*   equals "settled" and p is a member of Blocks of view."
InputsTracked ==
    \A t \in Txs : tx[t].st \in Unfinished =>
        \A p \in tx[t].ins \cap Txs :
            \/ tx[p].st \in Unfinished
            \/ tx[p].st = "settled" /\ p \in Blocks(view)

\* In English: "A drop is only a hint: the planner writes off only
\* transactions that truly can never land."
\* Literally: "DeadIsDead is defined as: for all t in Txs, if tx of t's st
\*   equals "dead" then CannotLand of t."
DeadIsDead ==
    \A t \in Txs : tx[t].st = "dead" => CannotLand(t)

\* In English: "What the planner calls settled is in the immovable part of
\* the node's chain."
\* Literally: "SettledIsFinal is defined as: for all t in Txs, if tx of t's
\*   st equals "settled" then t is a member of Blocks of Prefix of chain and
\*   floor."
SettledIsFinal ==
    \A t \in Txs : tx[t].st = "settled" => t \in Blocks(Prefix(chain, floor))

\* In English: "The planner never races itself: when transactions of two
\* intents spend the same output, the later one was built only after the
\* earlier one, if it ever reached a node, could no longer land. So no
\* planner transaction is ever refused for an input another intent's
\* transaction took."
\* Literally: "NoSelfConflict is defined as: for all t1 and t2 in Txs, if t1
\*   is less than t2, and t1 is a member of network, and tx of t2's st does
\*   not equal "unused", and tx of t1's intent does not equal tx of t2's
\*   intent, and the intersection of tx of t1's ins and tx of t2's ins does
\*   not equal the empty set, then CannotLand of t1."
NoSelfConflict ==
    \A t1, t2 \in Txs :
        /\ t1 < t2
        /\ t1 \in network
        /\ tx[t2].st # "unused"
        /\ tx[t1].intent # tx[t2].intent
        /\ tx[t1].ins \cap tx[t2].ins # {}
        => CannotLand(t1)

\* In English: "No intent is paid twice: the node's chain never holds two
\* attempts of one intent."
\* Literally: "IntentAtMostOnce is defined as: for all i in Intents, the
\*   number of members of the set of every t in the intersection of Blocks of
\*   chain and Txs such that tx of t's intent equals i is less than or equal
\*   to 1."
IntentAtMostOnce ==
    \A i \in Intents :
        Cardinality({t \in Blocks(chain) \cap Txs : tx[t].intent = i}) <= 1

-----------------------------------------------------------------------------
\* Liveness, checked under LiveSpec.

\* In English: "Every transaction the planner counts on finishes once the
\* truth decides it: one that can never land is eventually written off
\* (no reservation is held for ever), and one in the immovable part of the
\* chain is eventually settled."
Finishes ==
    \A t \in Txs :
        (tx[t].st \in Unfinished
         /\ (CannotLand(t) \/ t \in Blocks(Prefix(chain, floor))))
        ~> tx[t].st \notin Unfinished

-----------------------------------------------------------------------------
\* Probes: each says "this never happens" about something that should be
\* able to happen. A Probe*Bug.cfg must find a counterexample; if it passed,
\* the configs that pass would be passing for want of interesting states.

\* In English: "Never are three unfinished transactions chained, each
\* spending the one before, with none of them in the view."
\* Literally: "ProbeNoChainOfThree is defined as: it is not true that there
\*   exist a, b and c in Txs such that the set containing a, b and c is a
\*   subset of the set of every t in Txs such that tx of t's st is a member
\*   of Unfinished and t is not a member of Blocks of view, and a is a member
\*   of tx of b's ins, and b is a member of tx of c's ins."
ProbeNoChainOfThree ==
    ~\E a, b, c \in Txs :
        /\ {a, b, c} \subseteq
               {t \in Txs : tx[t].st \in Unfinished /\ t \notin Blocks(view)}
        /\ a \in tx[b].ins
        /\ b \in tx[c].ins

\* In English: "Never is a transaction the planner had a drop notice for on
\* the node's chain."
\* Literally: "ProbeNoDropNoticeThenIncluded is defined as: the intersection
\*   of hint and Blocks of chain equals the empty set."
ProbeNoDropNoticeThenIncluded == hint \cap Blocks(chain) = {}

\* In English: "Never does a second attempt settle while the first, which
\* reached a node, is written off."
\* Literally: "ProbeNoRebuildSettles is defined as: it is not true that there
\*   exist t1 and t2 in Txs such that t1 is less than t2, and tx of t1's
\*   intent equals tx of t2's intent, and t1 is a member of network, and tx of
\*   t1's st equals "dead", and tx of t2's st equals "settled"."
ProbeNoRebuildSettles ==
    ~\E t1, t2 \in Txs :
        /\ t1 < t2 /\ tx[t1].intent = tx[t2].intent
        /\ t1 \in network /\ tx[t1].st = "dead" /\ tx[t2].st = "settled"

\* In English: "Never does the first attempt settle after a second one,
\* which also reached a node, was built: the drop notice is never wrong."
\* Literally: "ProbeNoOriginalSettlesAfterRebuild is defined as: it is not
\*   true that there exist t1 and t2 in Txs such that t1 is less than t2, and
\*   tx of t1's intent equals tx of t2's intent, and t2 is a member of
\*   network, and tx of t1's st equals "settled", and tx of t2's st equals
\*   "dead"."
ProbeNoOriginalSettlesAfterRebuild ==
    ~\E t1, t2 \in Txs :
        /\ t1 < t2 /\ tx[t1].intent = tx[t2].intent
        /\ t2 \in network /\ tx[t1].st = "settled" /\ tx[t2].st = "dead"

\* In English: "Never is an unfinished transaction built on one that the
\* view held and the node has since rolled back."
\* Literally: "ProbeNoParentRolledBackUnderChild is defined as: it is not
\*   true that there exist p and c in Txs such that tx of c's st is a member
\*   of Unfinished, and p is a member of tx of c's ins, and p is a member of
\*   Blocks of view, and p is not a member of Blocks of chain."
ProbeNoParentRolledBackUnderChild ==
    ~\E p, c \in Txs :
        /\ tx[c].st \in Unfinished /\ p \in tx[c].ins
        /\ p \in Blocks(view) /\ p \notin Blocks(chain)

\* In English: "Never are two unfinished attempts of one intent kept apart
\* only by what they build on: they share no input themselves."
\* Literally: "ProbeNoRebuildOnReplacedParent is defined as: it is not true
\*   that there exist t1 and t2 in Txs such that t1 is less than t2, and tx of
\*   t1's intent equals tx of t2's intent, and tx of t1's st is a member of
\*   Unfinished, and tx of t2's st is a member of Unfinished, and the
\*   intersection of tx of t1's ins and tx of t2's ins equals the empty set."
ProbeNoRebuildOnReplacedParent ==
    ~\E t1, t2 \in Txs :
        /\ t1 < t2 /\ tx[t1].intent = tx[t2].intent
        /\ tx[t1].st \in Unfinished /\ tx[t2].st \in Unfinished
        /\ tx[t1].ins \cap tx[t2].ins = {}

\* In English: "Never is a transaction that reached a node written off by
\* its time-to-live alone, with every input still unspent in the view."
\* Literally: "ProbeNoExpiry is defined as: it is not true that there exists
\*   a t in Txs such that tx of t's st equals "dead", and t is a member of
\*   network, and tx of t's ins is a subset of Utxo of tx and view."
ProbeNoExpiry ==
    ~\E t \in Txs :
        /\ tx[t].st = "dead" /\ t \in network
        /\ tx[t].ins \subseteq Utxo(tx, view)

\* In English: "Never does an output pass from one intent to another: no
\* two transactions of different intents spend the same output."
\* Literally: "ProbeNoReservationReuse is defined as: it is not true that
\*   there exist t1 and t2 in Txs such that tx of t1's intent does not equal
\*   tx of t2's intent, and the intersection of tx of t1's ins and tx of t2's
\*   ins does not equal the empty set."
ProbeNoReservationReuse ==
    ~\E t1, t2 \in Txs :
        /\ tx[t1].intent # tx[t2].intent
        /\ tx[t1].ins \cap tx[t2].ins # {}

\* In English: "Never does a restart happen while a transaction is on the
\* node's chain, unfinished for the planner, and missing from the resumed
\* view."
\* Literally: "ProbeNoRestartMidFlight is defined as: it is not true that n's
\*   restart is greater than 0 and there exists a t in Txs such that tx of t's
\*   st equals "live", and t is a member of Blocks of chain, and t is not a
\*   member of Blocks of view, and tx of t's ins is not a subset of Genesis."
ProbeNoRestartMidFlight ==
    ~(/\ n.restart > 0
      /\ \E t \in Txs : /\ tx[t].st = "live"
                        /\ t \in Blocks(chain)
                        /\ t \notin Blocks(view)
                        /\ ~(tx[t].ins \subseteq Genesis))
=============================================================================
