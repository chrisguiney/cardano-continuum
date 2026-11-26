---------------------------- MODULE UtxoVectors ----------------------------
(* Generated into a scratch directory by the continuum harvester; not part  *)
(* of the model repository. It EXTENDS the model's Utxo module and writes,  *)
(* for every state TableSpec reaches, the ledger and each family's          *)
(* expressible transactions with SpecAccepts, as one JSON file per state.   *)
EXTENDS Utxo, Json, TLC, FiniteSets, Naturals

OutCode(o) == IF o = None THEN "n"
              ELSE ToString(o.coin) \o ToString(o.tok)
                   \o (IF o.lock = "key" THEN "k" ELSE "s")

Ok(F) == {tx \in F : Expressible(tx)}

Rows(fam, F) ==
    {[family |-> fam, tx |-> tx, specAccepts |-> SpecAccepts(tx)] : tx \in Ok(F)}

AllRows ==
    Rows("inputs", FamInputs) \cup Rows("balance", FamBalance)
    \cup Rows("outputs", FamOutputs) \cup Rows("interval", FamInterval)
    \cup Rows("phase2", FamPhase2)

Counts ==
    [inputs   |-> Cardinality(Ok(FamInputs)),
     balance  |-> Cardinality(Ok(FamBalance)),
     outputs  |-> Cardinality(Ok(FamOutputs)),
     interval |-> Cardinality(Ok(FamInterval)),
     phase2   |-> Cardinality(Ok(FamPhase2))]

\* Each state names its own file, so parallel workers never write one path.
\* ntx separates the partial ledgers TableSpec builds on the way.
DumpName ==
    "vectors/" \o ToString(ntx) \o "_" \o OutCode(utxo["a"]) \o "_"
    \o OutCode(utxo["b"]) \o "_" \o ToString(slot) \o "_" \o ToString(reward)
    \o ".json"

Dump ==
    JsonSerialize(DumpName,
        <<[state  |-> [utxo |-> utxo, slot |-> slot,
                       registered |-> registered, reward |-> reward],
           counts |-> Counts,
           rows   |-> AllRows]>>)

\* True in every state; the dump is its side effect.
VecDump == Dump \/ TRUE
=============================================================================
