--------------------------- MODULE DexContention ---------------------------
(***************************************************************************)
(* Swappers racing for one constant-product pool output.                  *)
(*                                                                         *)
(* README.md pins the commit modelled, maps each action to its code and   *)
(* lists the drift. The code a swap runs through:                         *)
(*   Build    internal/engine/internal/intent/spec.go buildSwap: reads    *)
(*            the pool output from the view at build time, spends the     *)
(*            intent's reserved coins (Maker.Attempt)                     *)
(*   Include  internal/fakechain: the ledger's UTXO rule (inputs exist)  *)
(*            then the pool validator (contracts/templates/validators/    *)
(*            pool.ak), which reads the datum of the output it spends     *)
(*   Reject   internal/engine/internal/pay/retry.go Retry.Answer with     *)
(*            BadInputsUTxO; the oracle's expected set for a pool spend   *)
(*            (intent kind swap: kind.again, Attempt.Expected)            *)
(*   Sync     internal/chain View.Apply / Rollback                        *)
(*   GiveUp   the retry machine's patience (submit.Machine)               *)
(*                                                                         *)
(* Each actor has one swap intent. The pool is one output; every swap     *)
(* spends it and makes the next version. A swap is valid when the pool    *)
(* version it spends is the current one and its actor's coin is unspent  *)
(* (the UTXO rule, phase 1). The pool validator then checks that the new  *)
(* pool output keeps the reserves product of the datum it spends: a swap  *)
(* computed from one version's datum but spending another fails phase 2   *)
(* and forfeits collateral. The engine reads the pool from its view,      *)
(* which lags the chain until Sync. A loser hears BadInputs and rebuilds  *)
(* against the pool it now sees, up to Patience attempts, every attempt  *)
(* spending the intent's one reserved coin, so at most one can land.     *)
(*                                                                         *)
(* Rules the unmutated model states, each broken by one Mutation:        *)
(*   1. a rebuild reads the pool's reference AND datum from the view at  *)
(*      build time ("stale_datum" keeps the first attempt's datum);       *)
(*   2. every attempt of an intent spends the intent's reserved coin     *)
(*      ("fresh_coins" gives each attempt its own);                       *)
(*   3. giving up releases the intent's reservation ("keep_reservation"  *)
(*      does not);                                                         *)
(*   4. the oracle expects BadInputs from any pool spend, first attempts  *)
(*      included ("narrow_expect" expects it only from a rebuild).       *)
(*                                                                         *)
(* Not modelled: amounts, fees, slippage, the reserves themselves (a     *)
(* version stands for a datum), several pools, batchers, adversaries     *)
(* that front-run, K-deep settlement (a swap on chain once rollbacks are *)
(* spent is final), the planner's write-off rule for attempts that may   *)
(* still land (specs/planner-reservations; here giving up waits until    *)
(* no rollback remains).                                                  *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    Actors,        \* the swappers
    Patience,      \* attempts an actor builds before giving up
    MaxRollbacks,  \* blocks the chain may roll back, in all
    Mutation       \* "none" or one rule broken (see the header)

ASSUME Patience >= 1 /\ MaxRollbacks >= 0
ASSUME Mutation \in {"none", "stale_datum", "fresh_coins", "keep_reservation", "narrow_expect"}

\* An attempt: its actor, its number, the pool version it spends, the
\* version whose datum it computed the new pool from, and the coin it
\* spends besides the pool.
Attempt(a, n, p, d, c) == [actor |-> a, n |-> n, pool |-> p, datum |-> d, coin |-> c]

\* Actor states: building (the next attempt is due), waiting (an attempt
\* is out), settled (one landed), gaveup.
States == {"building", "waiting", "settled", "gaveup"}

VARIABLES
    chain,      \* the swaps on chain, oldest first; the pool's version is its length
    sent,       \* every attempt ever sent: a node may include any valid one
    seen,       \* the pool version the engine's view has
    st,         \* each actor's state
    tries,      \* attempts each actor has built
    first,      \* the pool version each actor's first attempt read
    reserved,   \* the coins the planner holds for unfinished intents
    rollbacks,  \* rollbacks left
    lost,       \* a swap landed whose pool validator failed: collateral forfeited
    missed      \* the oracle met a rejection outside its expected set

vars == <<chain, sent, seen, st, tries, first, reserved, rollbacks, lost, missed>>

\* Each actor's reserved coin is named by the actor; a fresh coin by the
\* actor and the attempt.
Coin(a) == <<a, 0>>
FreshCoin(a, n) == <<a, n>>
Coins == {Coin(a) : a \in Actors} \cup {FreshCoin(a, n) : a \in Actors, n \in 1..Patience}

Version == Len(chain)
Spent == {chain[i].coin : i \in 1..Len(chain)}
\* In English: "the UTXO rule: the pool output it spends is the current
\* one and its coin is unspent."
Valid(t) == t.pool = Version /\ t.coin \notin Spent
Landed(a) == {i \in 1..Len(chain) : chain[i].actor = a}
Mine(a) == {t \in sent : t.actor = a}

TypeOK ==
    /\ seen \in 0..(Cardinality(Actors) * Patience)
    /\ st \in [Actors -> States]
    /\ tries \in [Actors -> 0..Patience]
    /\ reserved \subseteq Coins
    /\ rollbacks \in 0..MaxRollbacks
    /\ lost \in BOOLEAN /\ missed \in BOOLEAN

Init ==
    /\ chain = <<>>
    /\ sent = {}
    /\ seen = 0
    /\ st = [a \in Actors |-> "building"]
    /\ tries = [a \in Actors |-> 0]
    /\ first = [a \in Actors |-> 0]
    /\ reserved = {Coin(a) : a \in Actors}
    /\ rollbacks = MaxRollbacks
    /\ lost = FALSE
    /\ missed = FALSE

\* Build: the actor reads the pool from the view and sends an attempt.
Build(a) ==
    /\ st[a] = "building"
    /\ tries[a] < Patience
    /\ LET n == tries[a] + 1
           d == IF Mutation = "stale_datum" /\ n > 1 THEN first[a] ELSE seen
           c == IF Mutation = "fresh_coins" THEN FreshCoin(a, n) ELSE Coin(a)
       IN /\ sent' = sent \cup {Attempt(a, n, seen, d, c)}
          /\ tries' = [tries EXCEPT ![a] = n]
          /\ first' = IF n = 1 THEN [first EXCEPT ![a] = seen] ELSE first
          /\ reserved' = reserved \cup {c}
    /\ st' = [st EXCEPT ![a] = "waiting"]
    /\ UNCHANGED <<chain, seen, rollbacks, lost, missed>>

\* Include: a node puts a valid attempt in a block. The pool validator
\* passes only when the attempt's datum is the version it spends.
Include(t) ==
    /\ t \in sent
    /\ Valid(t)
    /\ chain' = Append(chain, t)
    /\ lost' = (lost \/ t.datum # t.pool)
    /\ UNCHANGED <<sent, seen, st, tries, first, reserved, rollbacks, missed>>

\* Reject: a node refuses the actor's outstanding attempt because the pool
\* output it spends is gone (BadInputsUTxO). The oracle checks that its
\* expected set held that class; the actor builds again.
Expects(t) == Mutation # "narrow_expect" \/ t.n > 1

Reject(t) ==
    /\ t \in sent
    /\ st[t.actor] = "waiting"
    /\ t.n = tries[t.actor]
    /\ t.pool < Version
    /\ missed' = (missed \/ ~Expects(t))
    /\ st' = [st EXCEPT ![t.actor] = "building"]
    /\ UNCHANGED <<chain, sent, seen, tries, first, reserved, rollbacks, lost>>

\* Sync: the view catches up with the chain.
Sync ==
    /\ seen # Version
    /\ seen' = Version
    /\ UNCHANGED <<chain, sent, st, tries, first, reserved, rollbacks, lost, missed>>

\* Rollback: the last block leaves the chain; its swap is again a sent
\* attempt a node may include.
Rollback ==
    /\ rollbacks > 0
    /\ Len(chain) > 0
    /\ chain' = SubSeq(chain, 1, Len(chain) - 1)
    /\ rollbacks' = rollbacks - 1
    /\ UNCHANGED <<sent, seen, st, tries, first, reserved, lost, missed>>

\* Settle: an actor's swap is on chain and no rollback can take it off.
Settle(a) ==
    /\ st[a] \in {"waiting", "building"}
    /\ rollbacks = 0
    /\ Landed(a) # {}
    /\ st' = [st EXCEPT ![a] = "settled"]
    /\ reserved' = reserved \ {t.coin : t \in Mine(a)}
    /\ UNCHANGED <<chain, sent, seen, tries, first, rollbacks, lost, missed>>

\* GiveUp: the actor's patience is spent and its last attempt was
\* refused; once no rollback remains nothing of its can land.
GiveUp(a) ==
    /\ st[a] = "building"
    /\ tries[a] = Patience
    /\ rollbacks = 0
    /\ \A t \in Mine(a) : ~Valid(t)
    /\ st' = [st EXCEPT ![a] = "gaveup"]
    /\ reserved' = IF Mutation = "keep_reservation" THEN reserved
                   ELSE reserved \ {t.coin : t \in Mine(a)}
    /\ UNCHANGED <<chain, sent, seen, tries, first, rollbacks, lost, missed>>

\* Bury: the chain grows K blocks past the last swap; no rollback can
\* reach the swaps any more.
Bury ==
    /\ rollbacks > 0
    /\ rollbacks' = 0
    /\ UNCHANGED <<chain, sent, seen, st, tries, first, reserved, lost, missed>>

\* Done: every intent ended; the run stops here, not stuck.
Done ==
    /\ \A a \in Actors : st[a] \in {"settled", "gaveup"}
    /\ UNCHANGED vars

Next ==
    \/ \E a \in Actors : Build(a) \/ Settle(a) \/ GiveUp(a)
    \/ \E t \in sent : Include(t) \/ Reject(t)
    \/ Sync
    \/ Rollback
    \/ Bury
    \/ Done

Spec == Init /\ [][Next]_vars

\* Fairness: the view syncs, the chain buries its blocks, actors build,
\* settle and give up when they can, refusals arrive, and nodes keep
\* including valid swaps. Include is strongly fair: a swap is valid only
\* between other swaps. Attempts are finite, so fairness over "some swap
\* is included" is enough for every intent to end.
FairSpec ==
    /\ Spec
    /\ WF_vars(Sync)
    /\ WF_vars(Bury)
    /\ \A a \in Actors : WF_vars(Build(a)) /\ WF_vars(Settle(a)) /\ WF_vars(GiveUp(a))
    /\ SF_vars(\E t \in sent : Include(t))
    /\ WF_vars(\E t \in sent : Reject(t))

---------------------------------------------------------------------------
\* Safety.

\* In English: "an intent's swap lands at most once."
PaidOnce == \A a \in Actors : Cardinality(Landed(a)) <= 1

\* In English: "no swap on chain failed the pool validator: a loser of the
\* race fails phase 1 (BadInputs) and never forfeits collateral."
CollateralSafe == ~lost

\* In English: "an ended intent holds no reservation."
NoLeak == \A a \in Actors : st[a] \in {"settled", "gaveup"} => \A t \in Mine(a) : t.coin \notin reserved

\* In English: "every refusal of a pool spend was in the oracle's
\* expected set."
OracleSound == ~missed

\* Liveness: every intent ends, by a swap or by giving up.
Ends == <>(\A a \in Actors : st[a] \in {"settled", "gaveup"})

\* State bound: versions grow only with swaps, which are bounded by
\* actors times patience.
Bound == Len(chain) <= Cardinality(Actors) * Patience
=============================================================================
