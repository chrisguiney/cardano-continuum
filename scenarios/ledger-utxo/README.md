# scenarios/ledger-utxo

Scenarios of the `ledger-utxo` family, from `Utxo.tla` of the gouroboros
models. A `<Name>/0.json` directory holds the first trace of that config.

| Scenario | Invariant | Source |
|---|---|---|
| `UtxoGoBadInputsLateBabbageBug/0` | `GoMissingInputIsBadInputs` | trace |
| `UtxoGoIntervalInclusiveBug/0` | `GoAgrees` | trace |
| `UtxoGoSpentOnlyCollUnresolvedBug/0` | `GoAgrees` | trace |
| `UtxoGoSpentOnlyRefsUnresolvedBug/0` | `GoAgrees` | trace |
| `UtxoGoTtlExclusiveBug/0` | `GoAgrees` | trace |
| `UtxoProbeNoBurnBug` | `ProbeNoBurn` | trace |
| `walk-mary-d6-s35` | `Stop` | walk |

Format and regeneration: [`../README.md`](../README.md).
