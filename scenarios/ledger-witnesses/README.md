# scenarios/ledger-witnesses

Scenarios of the `ledger-witnesses` family, from `Witnesses.tla` of the
gouroboros models. Each `<Name>/0.json` is the first trace of that config.

| Scenario | Invariant |
|---|---|
| `WitnessesGoExpireExclusiveBug/0` | `GoAgrees` |
| `WitnessesGoStartAbsentOkBug/0` | `GoAgrees` |
| `WitnessesProbeNoTimelockAcceptedBug/0` | `ProbeNoTimelockAccepted` |

Format and regeneration: [`../README.md`](../README.md).
