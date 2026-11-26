-------------------------- MODULE WitnessesWitness --------------------------
(* Generated into a scratch directory by the continuum harvester; not part  *)
(* of the model repository. It EXTENDS the model's Witnesses module. Each   *)
(* Wit* invariant writes the cells that make a table invariant false in an  *)
(* environment, with the reference's verdict for each, before that          *)
(* invariant fails. Witnesses.tla keeps those cells under quantifiers, so a *)
(* counterexample alone names only the environment.                         *)
EXTENDS Witnesses, Json, TLC

Cells(kind, S, Acc(_)) == {[kind |-> kind, cell |-> c, accepts |-> Acc(c)] : c \in S}

Dump(inv, rows) ==
    JsonSerialize("witness.json",
        <<[invariant |-> inv,
           env |-> [wits |-> wits, lo |-> lo, hi |-> hi],
           rows |-> rows]>>)

WitGoAgrees ==
    GoAgrees \/ Dump("GoAgrees",
        Cells("key", {c \in KeyCells : GoKeyAccepts(c) # SpecKeyAccepts(c)},
              SpecKeyAccepts)
        \cup Cells("native",
              {s \in Scripts : GoNativeAccepts(s) # SpecNativeAccepts(s)},
              SpecNativeAccepts)
        \cup Cells("script",
              {c \in ScriptCells : GoScriptAccepts(c) # SpecScriptAccepts(c)},
              SpecScriptAccepts)
        \cup (IF PlutusEra
              THEN Cells("datum",
                     {c \in DatumCells : GoDatumAccepts(c) # SpecDatumAccepts(c)},
                     SpecDatumAccepts)
                   \cup Cells("integrity",
                     {c \in IntegrityCells :
                        GoIntegrityAccepts(c) # SpecIntegrityAccepts(c)},
                     SpecIntegrityAccepts)
              ELSE {}))

WitProbeNoBootstrapAuthorizes ==
    ProbeNoBootstrapAuthorizes \/ Dump("ProbeNoBootstrapAuthorizes",
        Cells("key", {c \in KeyCells : SpecKeyAccepts(c) /\ c.key = "b"
                                       /\ c.src # "reg0"}, SpecKeyAccepts))
WitProbeNoOwnerExemption ==
    ProbeNoOwnerExemption \/ Dump("ProbeNoOwnerExemption",
        Cells("key", {c \in KeyCells : c.src = "owner" /\ SpecKeyAccepts(c)
                                       /\ c.key \notin wits}, SpecKeyAccepts))
WitProbeNoTimelockAccepted ==
    ProbeNoTimelockAccepted \/ Dump("ProbeNoTimelockAccepted",
        Cells("native",
              {s \in Scripts : /\ SpecNativeAccepts(s) /\ s.op = "all"
                               /\ Len(s.subs) = 2 /\ s.subs[1].op = "sig"
                               /\ s.subs[2].op = "expire"},
              SpecNativeAccepts))
WitProbeNoRefScriptAccepted ==
    ProbeNoRefScriptAccepted \/ Dump("ProbeNoRefScriptAccepted",
        Cells("script", {c \in ScriptCells : SpecScriptAccepts(c) /\ ~c.wit},
              SpecScriptAccepts))
WitProbeNoInlineDatumAccepted ==
    ProbeNoInlineDatumAccepted \/ Dump("ProbeNoInlineDatumAccepted",
        Cells("datum", {c \in DatumCells : SpecDatumAccepts(c)
                                           /\ c.datum = "inline"},
              SpecDatumAccepts))
=============================================================================
