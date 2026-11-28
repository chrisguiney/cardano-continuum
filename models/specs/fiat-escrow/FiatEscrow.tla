----------------------------- MODULE FiatEscrow -----------------------------
(***************************************************************************)
(* One human's fiat ledger beside its buy orders.                          *)
(*                                                                         *)
(* README.md pins the commit modelled, maps each action to its code and   *)
(* lists the drift. The code the ledger runs through:                    *)
(*   Place    internal/engine/internal/cohort/money.go Money.Fund: a buy  *)
(*            order holds its fiat (Order.Cents) when it is placed        *)
(*   Settle   internal/engine/internal/cohort/desk.go Desk.Settled: the  *)
(*            order's ADA is K deep; the held cents are spent             *)
(*   Release  the order is released or given up (internal/engine/        *)
(*            internal/pay/retry.go); its pending record goes, and its   *)
(*            hold with it                                                 *)
(*   Pay      internal/engine/internal/household Bill, applied by         *)
(*            cohort Money.bill: an expense paid from available fiat      *)
(*   Earn     household Payday: income added to fiat                      *)
(*   Restart  the engine restored from a snapshot: fiat from the humans   *)
(*            section, open orders from the pay section                   *)
(*                                                                         *)
(* Fiat lives off chain in integer cents. What an open buy holds is not   *)
(* a field of its own: it is the Cents of the open order's record, which  *)
(* the pay section already snapshots. Available fiat is fiat less the     *)
(* holds; an expense or a new order reads only the available amount.    *)
(*                                                                         *)
(* Rules the unmutated model states, each broken by one Mutation:        *)
(*   1. placing an order holds its cents ("debit_at_settle" holds         *)
(*      nothing and only debits when the ADA settles);                    *)
(*   2. the hold is the open order's own record, restored with it         *)
(*      ("stored_hold" keeps it in a field of its own that a restore      *)
(*      does not bring back, and settles what that field holds);         *)
(*   3. releasing an order drops its hold ("release_leak" keeps it).     *)
(*                                                                         *)
(* No rule of the Agda ledger specification applies: fiat never touches   *)
(* the chain, and no ledger rule governs it.                             *)
(*                                                                         *)
(* Not modelled: the ADA side (specs/submit-retry and                    *)
(* specs/planner-reservations), FX, partial fills, several humans.        *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    Orders,      \* the buy orders the human may place
    Cents,       \* what each order costs
    Fiat0,       \* the fiat the human starts with
    Income,      \* one payday's income
    Expense,     \* one bill
    Paydays,     \* paydays in the run
    Bills,       \* bills in the run
    Restarts,    \* engine restarts in the run
    Mutation     \* "none" or one rule broken (see the header)

ASSUME Cents > 0 /\ Fiat0 >= 0 /\ Income >= 0 /\ Expense > 0
ASSUME Mutation \in {"none", "debit_at_settle", "stored_hold", "release_leak"}

\* Order states: new (not placed), open, settled (traded), released.
States == {"new", "open", "settled", "released"}

VARIABLES
    fiat,      \* the human's fiat in cents
    order,     \* each order's state
    stored,    \* stored_hold only: the hold field of each order
    earned,    \* income received so far
    paid,      \* expenses paid so far
    paydays,   \* paydays left
    bills,     \* bills left
    restarts   \* restarts left

vars == <<fiat, order, stored, earned, paid, paydays, bills, restarts>>

Sum(f(_), S) ==
    LET Rec[T \in SUBSET S] ==
        IF T = {} THEN 0 ELSE LET x == CHOOSE y \in T : TRUE IN f(x) + Rec[T \ {x}]
    IN Rec[S]

Open == {o \in Orders : order[o] = "open"}
Settled == {o \in Orders : order[o] = "settled"}

\* What the human's open orders hold, as the code computes it: from the
\* order records (rule 2), or from the stored field.
RecordHold(o) == IF order[o] = "open" THEN Cents ELSE 0
Hold(o) ==
    CASE Mutation = "debit_at_settle" -> 0
      [] Mutation \in {"stored_hold", "release_leak"} -> stored[o]
      [] OTHER -> RecordHold(o)
Held == Sum(Hold, Orders)
Available == fiat - Held

Init ==
    /\ fiat = Fiat0
    /\ order = [o \in Orders |-> "new"]
    /\ stored = [o \in Orders |-> 0]
    /\ earned = 0
    /\ paid = 0
    /\ paydays = Paydays
    /\ bills = Bills
    /\ restarts = Restarts

\* A tranche or a scored buy: placed only when the available fiat covers it.
Place(o) ==
    /\ order[o] = "new"
    /\ Available >= Cents
    /\ order' = [order EXCEPT ![o] = "open"]
    /\ stored' = IF Mutation = "debit_at_settle" THEN stored ELSE [stored EXCEPT ![o] = Cents]
    /\ UNCHANGED <<fiat, earned, paid, paydays, bills, restarts>>

\* The ADA is K deep: the held cents are spent.
Settle(o) ==
    /\ order[o] = "open"
    /\ order' = [order EXCEPT ![o] = "settled"]
    /\ fiat' = fiat - (IF Mutation = "stored_hold" THEN stored[o] ELSE Cents)
    /\ stored' = [stored EXCEPT ![o] = 0]
    /\ UNCHANGED <<earned, paid, paydays, bills, restarts>>

\* Released or given up: the pending record goes.
Release(o) ==
    /\ order[o] = "open"
    /\ order' = [order EXCEPT ![o] = "released"]
    /\ stored' = IF Mutation = "release_leak" THEN stored ELSE [stored EXCEPT ![o] = 0]
    /\ UNCHANGED <<fiat, earned, paid, paydays, bills, restarts>>

Earn ==
    /\ paydays > 0
    /\ paydays' = paydays - 1
    /\ fiat' = fiat + Income
    /\ earned' = earned + Income
    /\ UNCHANGED <<order, stored, paid, bills, restarts>>

\* A bill is paid from the available fiat, floored at zero; the rest is
\* short and forces a sale of ADA (not modelled: it is a new order's
\* concern, not the ledger's).
Pay ==
    /\ bills > 0
    /\ LET amt == IF Available >= Expense THEN Expense ELSE IF Available > 0 THEN Available ELSE 0
       IN /\ fiat' = fiat - amt
          /\ paid' = paid + amt
    /\ bills' = bills - 1
    /\ UNCHANGED <<order, stored, earned, paydays, restarts>>

\* A restore brings back fiat and the order records; a hold stored in a
\* field of its own outside them does not come back.
Restart ==
    /\ restarts > 0
    /\ restarts' = restarts - 1
    /\ stored' = IF Mutation = "stored_hold" THEN [o \in Orders |-> 0] ELSE stored
    /\ UNCHANGED <<fiat, order, earned, paid, paydays, bills>>

\* No order open and every bounded input spent: the run is over. An order
\* never placed is a tranche the human could not afford.
Done ==
    /\ \A o \in Orders : order[o] # "open"
    /\ paydays = 0 /\ bills = 0 /\ restarts = 0

Next ==
    \/ \E o \in Orders : Place(o) \/ Settle(o) \/ Release(o)
    \/ Earn \/ Pay \/ Restart
    \/ (Done /\ UNCHANGED vars)

Spec == Init /\ [][Next]_vars

\* The engine settles or releases every open order in the end.
FairSpec == Spec /\ \A o \in Orders : WF_vars(Settle(o) \/ Release(o))

TypeOK ==
    /\ fiat \in Int
    /\ order \in [Orders -> States]
    /\ stored \in [Orders -> Nat]
    /\ earned \in Nat /\ paid \in Nat

\* Fiat never goes below what open orders hold.
AvailableNonNegative == fiat - Sum(RecordHold, Orders) >= 0

\* Every cent is accounted for: what the human started with and earned,
\* less the bills paid and the orders settled.
Conservation == fiat = Fiat0 + earned - paid - Cents * Cardinality(Settled)

\* What the ledger treats as held is exactly the open orders' cents.
HoldsMatchOpenOrders == \A o \in Orders : Hold(o) = RecordHold(o)

\* Liveness: an order placed ends as exactly one of traded or released.
Ends == \A o \in Orders : order[o] = "open" ~> order[o] \in {"settled", "released"}
=============================================================================
