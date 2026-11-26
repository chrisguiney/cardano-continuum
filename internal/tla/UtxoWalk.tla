------------------------------ MODULE UtxoWalk ------------------------------
(* Generated into a scratch directory by the continuum harvester; not part  *)
(* of the model repository. A random walk over Utxo.tla's ledger: each step *)
(* offers one expressible transaction of LedgerTxs, applies it if the       *)
(* reference accepts it and leaves the ledger alone if not, and records the *)
(* transaction and the verdict in last. Even steps offer only accepted      *)
(* transactions: nearly every member of LedgerTxs is rejected, so a uniform *)
(* walk would never leave the starting ledger. Stop is false after          *)
(* WalkDepth steps, which is how a simulation run ends with its trace       *)
(* dumped; a ledger with nothing left to accept ends the walk as a          *)
(* deadlock.                                                                *)
EXTENDS Utxo

CONSTANT WalkDepth

VARIABLES last, n

wvars == <<vars, last, n>>

WTry == \E t \in LedgerTxs :
          LET tx == Concrete(t) IN
          /\ Expressible(tx)
          /\ (n % 2 = 0) => SpecAccepts(tx)
          /\ IF SpecAccepts(tx) THEN Apply(t) ELSE UNCHANGED vars
          /\ last' = <<[tx |-> tx, accepts |-> SpecAccepts(tx)]>>
          /\ n' = n + 1

WWalk == Init /\ last = <<>> /\ n = 0 /\ [][WTry]_wvars

Stop == n < WalkDepth
=============================================================================
