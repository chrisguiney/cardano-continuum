# hack

Developer scripts, run through make targets. All read no stdin and bound
their waits.

| File | Used by | Does |
|---|---|---|
| `up.sh` | `make up` | installs or upgrades the Helm chart in `CONTEXT` (default: the current context) and `NAMESPACE`, resets the network when the run changes, waits for one chain and forwards `svc/continuum-ui` |
| `down.sh` | `make down` | deletes the pools, uninstalls the chart and deletes the namespace |
| `podstats.sh` | `sample.sh` | one line per pod: memory (MiB) and CPU (millicores) from the kubelet summary API |
| `sample.sh` | by hand | appends `podstats.sh` output to a TSV every `INTERVAL` seconds, `COUNT` times |
| `summarize.py` | by hand | steady and peak memory and CPU per pod kind from a `sample.sh` TSV |
| `market-snapshot.py` | `make market-snapshot` | refetches public FX (Frankfurter) and ADA (CoinGecko) rates and a year of history into `internal/profile/fxsnapshot.json`, fitting each currency's vol, drift and reversion; takes about two minutes for CoinGecko's rate limit |
| `devup/` | `up.sh`, `make chart-check` | Go helper: memory check, `ATTACKS` resolution, the engine's run identity, pool 0's bridge address |

Each script's header comment lists its environment variables. See
[`docs/single-machine.md`](../docs/single-machine.md) for sizing.
