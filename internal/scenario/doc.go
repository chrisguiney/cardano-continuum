// Package scenario turns TLA+ model output into transactions a node must
// answer the way the model says, and runs them.
//
// A [Scenario] is abstract: a model family, where it came from, its
// provenance and the model's own state. An [Adapter] is the family's
// refinement mapping: it says whether a scenario has a concrete form
// (Applicable), what the target must provide first (Preconditions), the
// concrete steps (Concretize) and what a node must answer to each
// (Expected). [Run] executes the steps against a [Target] and judges every
// answer with the verdict oracle; a verdict that is not the model's is a
// finding of kind [SpecDisagreement].
//
//	```mermaid
//	flowchart LR
//	  tlc[TLC: Bug cfgs, simulation] --> tla[internal/tla: exports, vectors]
//	  tla --> src[FromUtxo / FromWitnesses / FromCodecSeed / SampleVectors]
//	  src --> lib[(scenarios/: abstract, versioned)]
//	  lib --> ad[Adapter: Applicable, Preconditions, Concretize, Expected]
//	  ad -- Plan --> run[Run]
//	  run -- submit, wait, fault --> tgt[Target: fake chain, or a devnet]
//	  run -- Attempt --> oracle[oracle.Judge]
//	  oracle --> sum[Summary: pass, spec_disagreement, unconcretizable]
//	```
//
// Scenarios come from four sources. Mode A replays the counterexample of a
// Bug configuration: internal/tla reruns it under a wrapper module that
// writes out the transactions the model keeps under quantifiers. The vector
// table is every boundary transaction of Utxo.tla with SpecAccepts. Mode B
// is a TLC simulation walk over Utxo.tla's ledger. The codec table is the
// codec models' input domains, enumerated here.
//
// Every adapter writes Conway transactions at protocol version 10, and pins
// the target's parameters so the model's unit boundaries are real ones. One
// model coin is a unit U of lovelace: the largest minimum-UTxO value among
// the output shapes the scenario writes.
//
// # ledger-utxo (Utxo.tla)
//
//	model                          concrete
//	place a, b, c                  output 0, 1 of the setup transaction; a place
//	                               that never held an output is index 100+
//	later output at Fresh(u)       output 0 of the transaction that made it,
//	                               bound to the place the model's next state shows
//	coin k                         k * U lovelace
//	lock "key" / "script"          enterprise address of the scratch actor's
//	                               payment key / of the native script all []
//	token                          asset "t" under the policy all []
//	MinFee = 1, fee f              minFeeA = 0, minFeeB = U; fee f * U
//	MinUtxo = 1                    coinsPerUTxOByte unchanged; U is the minimum
//	                               of the largest output shape
//	KeyDeposit = 1                 keyDeposit = U
//	CollPct = 150, MaxColl = 2     collateralPercentage 150, maxCollateralInputs 2
//	delta, tdelta                  one lovelace, one token
//	slot s; bounds lo, hi          validated slot now; now - s + lo, now - s + hi;
//	                               0 is "no bound" from Allegra on
//	Shelley TTL hi (inclusive)     invalid_hereafter now - s + hi + 1
//	cert "reg"                     vote_reg_deleg_cert to "always abstain", deposit U
//	cert "dereg"                   unreg_cert, refund U
//	wdrl                           withdrawal of the whole reward balance
//	ret, tcoll                     collateral return of coin * U; total collateral
//
// Preserved exactly: an upper or lower bound equal to the slot is equal to
// the validated slot; the fee at, one unit under and above the minimum fee;
// value off by exactly one lovelace or one token; the collateral ratio, by
// linearity, when the balance is exactly 150% of the fee; a missing input is
// a reference to an output that does not exist; an output of one model coin
// is exactly the ledger minimum when it has the scenario's largest shape.
//
// Lost: an output of a smaller shape holding one model coin is above its
// minimum by the size difference; an output of zero model coins holds zero
// lovelace, not the minimum less one; the rejection's class is checked only
// against the family's whole class list, since SpecAccepts is a boolean.
// The model's eras are read as Conway, which holds for every cell except
// those reported as unconcretizable: Mary's token output below the coin
// minimum, a reference input that is also spent (refused from protocol
// version 9 to 10), a configuration that mutates the reference itself,
// redeemers (no Plutus script is attached, so the collateral rules that run
// only with redeemers are not reached), an output over the maximum value
// size, a phase-2-invalid transaction on a target that refuses them, and a
// reward balance on a target that cannot make one. A rejected step that
// cannot be carried is skipped; an accepted one ends the scenario there.
//
// # ledger-witnesses (Witnesses.tla)
//
//	model                          concrete
//	key v1                         the scratch actor: its payment key for the
//	                               sources in, coll, signer; its stake key for
//	                               wdrl, deleg, reg0
//	key v2; "bad"                  the second actor's payment key; its witness
//	                               with a signature that does not verify
//	wits                           exactly those vkey witnesses (the funder's is
//	                               added only where it is not the key under test)
//	source deleg                   vote_deleg_cert of a registered credential
//	source reg0                    the tag-0 registration, which needs no witness
//	native script cell             the spent output is locked by that script
//	leaf sig k                     signature leaf for k's key hash
//	bounds lo, hi; leaf slots      one model slot is one slot, placed so the
//	                               interval holds the validated slot
//
// Preserved: an expiry leaf equal to the upper bound, a start leaf equal to
// the lower bound, an absent bound. Not carried: bootstrap keys and
// witnesses (Byron addresses), pool, genesis-key and voter sources, an
// empty validity interval, and the script, datum and integrity-hash cells.
//
// # codec (cbor-codec, ledger-codec, dingo txsize-framing)
//
//	model                          concrete
//	MultiAssetQuantity             the quantity's bytes in an output's asset map,
//	                               the mint, an output's bare coin or the coin of
//	                               a [coin, assets] pair; head, tag 2/3 bignum,
//	                               the tag in a two-byte head, behind a duplicate
//	PreConwaySetTag                inputs, collateral, reference inputs, required
//	                               signers or the vkey witnesses untagged, under
//	                               tag 258 or under tag 259
//	ValueSetMembers                what tag 258 holds on the inputs: a definite
//	                               or indefinite list, a uint, a map
//	PlutusConstrFields             the constructor as an output's inline datum
//	TxSizeFraming                  the transaction's outer array as 84, 98 04 or
//	                               9f ... ff
//
// The verdict is the model's reference rule (RefAccepts, RefDecodes) read at
// the target's protocol version, restated beside each form. A refused
// encoding must be rejected as undecodable: a rejection by a ledger rule
// means the node decoded it, and is a disagreement. A decodable encoding may
// be accepted or rejected by a ledger rule. Lost: the model's own protocol
// version and era, and for TxSizeFraming everything but the premise that
// each framing decodes. Models with no transaction-level form in Conway
// (rationals, protocol versions, cost models, Plutus data re-encoding) are
// unconcretizable.
//
// Families owned by other adversaries are listed in [OwnerOf].
package scenario
