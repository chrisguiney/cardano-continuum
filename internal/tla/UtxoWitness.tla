---------------------------- MODULE UtxoWitness ----------------------------
(* Generated into a scratch directory by the continuum harvester; not part  *)
(* of the model repository. It EXTENDS the model's Utxo module so that a    *)
(* counterexample names the transactions behind it, which Utxo.tla keeps    *)
(* under existential quantifiers:                                           *)
(*   - WSpec is Spec with a history variable, last, holding the transaction *)
(*     the step applied;                                                    *)
(*   - WTableSpec is TableSpec with last left empty, and the Wit* invariants *)
(*     write the transactions that make a table invariant false in a state, *)
(*     with the reference's verdict for each, before that invariant fails.  *)
EXTENDS Utxo, Json, TLC

VARIABLE last

wvars == <<vars, last>>

WTableSpec == TableInit /\ last = <<>> /\ [][TableNext /\ UNCHANGED last]_wvars

WApply == \E t \in LedgerTxs :
            Apply(t) /\ last' = <<[tx |-> Concrete(t), accepts |-> TRUE]>>

WSpec == Init /\ last = <<>> /\ [][WApply \/ (Done /\ UNCHANGED last)]_wvars

Rows(S) == {[tx |-> tx, specAccepts |-> SpecAccepts(tx)] : tx \in S}

\* Writes the witnesses of invariant inv in this state. One file: TLC stops
\* at the first violating state.
Dump(inv, S) ==
    JsonSerialize("witness.json",
        <<[invariant |-> inv,
           state |-> [utxo |-> utxo, slot |-> slot,
                      registered |-> registered, reward |-> reward],
           rows |-> Rows(S)]>>)

WitGoAgrees ==
    GoAgrees \/ Dump("GoAgrees", {tx \in Checked : GoAccepts(tx) # SpecAccepts(tx)})
WitGoBadInputsComplete ==
    GoBadInputsComplete \/ Dump("GoBadInputsComplete",
        {tx \in Checked : (GoBadInputs(tx) \subseteq Live)
                          # (SpecReadInputs(tx) \subseteq Live)})
WitGoMissingInputIsBadInputs ==
    GoMissingInputIsBadInputs \/ Dump("GoMissingInputIsBadInputs",
        {tx \in Checked : ~(SpecReadInputs(tx) \subseteq Live)
                          /\ GoFirstLookupFailure(tx) # "BadInputs"})
WitProbeNoTokenOutputBelowMin ==
    ProbeNoTokenOutputBelowMin \/ Dump("ProbeNoTokenOutputBelowMin",
        {tx \in Checked : SpecAccepts(tx) /\ OutCoin(tx) < MinUtxo})
WitProbeNoCollateralAccepted ==
    ProbeNoCollateralAccepted \/ Dump("ProbeNoCollateralAccepted",
        {tx \in Checked : SpecAccepts(tx) /\ tx.rdmrs})
WitProbeNoReturnAccepted ==
    ProbeNoReturnAccepted \/ Dump("ProbeNoReturnAccepted",
        {tx \in Checked : SpecAccepts(tx) /\ tx.rdmrs /\ tx.ret # NoRet
                          /\ tx.tcoll # -1})
WitProbeNoOverlapAccepted ==
    ProbeNoOverlapAccepted \/ Dump("ProbeNoOverlapAccepted",
        {tx \in Checked : SpecAccepts(tx) /\ tx.refs \cap tx.ins # {}})
=============================================================================
