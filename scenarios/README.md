# Scenario library

Each file is one scenario in abstract form (format version 1): a model
family, where it came from (`trace`, `vector`, `walk` or `table`), its
provenance (model repository, commit, spec, cfg, invariant) and the model
state the adapter in `internal/scenario` turns into transactions. The file's
path below this directory is its id.

Only a small curated set is committed. Regenerate the rest, into
`scenarios/generated` (ignored by git):

    make scenarios TLA_REPO=https://github.com/chrisguiney/gouroboros-tla-models TLA_REF=cb40c04
    make vectors   TLA_REPO=... TLA_REF=...
    go run ./cmd/continuum scenario vectors scenarios/vectors/*.vectors.jsonl

Run them against the in-memory chain:

    go run ./cmd/continuum scenario run scenarios
    go run ./cmd/continuum scenario run -v scenarios/generated
    go run ./cmd/continuum scenario run ledger-utxo/UtxoGoTtlExclusiveBug/0

The harvest needs TLC with `-dumpTrace` (the 1.8.0 jar of `make models-install`).

## Known disagreements

Two committed scenarios are answered against the model by the fake chain,
which validates with gouroboros v0.220.1. They are kept as regression
attacks: each passes once the dependency follows the reference.

- `ledger-utxo/UtxoGoSpentOnlyCollUnresolvedBug/0`: a transaction with no
  redeemers that names a collateral input that does not exist is accepted.
  The reference's BadInputsUTxO covers collateral and reference inputs;
  gouroboros' Conway `UtxoValidateBadInputsUtxo` checks spent inputs only,
  and the fake chain discards the "not found" that the collateral witness
  rule reports instead, expecting BadInputs to carry it.
- `codec/MultiAssetQuantityMainBug` and
  `codec/table/MultiAssetQuantity/outputAsset-five-bignumLongTag`: an output
  asset quantity written as a CBOR bignum (`c24105`, `d8024105`) is decoded
  and then rejected only because the value does not balance. The reference
  reads quantities with `decodeWord64` and refuses the bytes.
