# hack

Developer scripts, run through make targets. All read no stdin and bound
their waits.

| File | Used by | Does |
|---|---|---|
| `up.sh` | `make up` | deploys operator, genesis, pools and engine UI to `CONTEXT` (default `docker-desktop`); idempotent |
| `down.sh` | `make down` | removes what `up.sh` deployed, pools first |
| `kind-up.sh`, `kind-down.sh`, `kind.yaml` | `make kind-up`, `make kind-down`, `make e2e` | the throwaway kind cluster (one control plane, two workers) and its plain-HTTP registry |
| `podstats.sh` | `sample.sh` | one line per pod: memory (MiB) and CPU (millicores) from the kubelet summary API |
| `sample.sh` | by hand | appends `podstats.sh` output to a TSV every `INTERVAL` seconds, `COUNT` times |
| `summarize.py` | by hand | steady and peak memory and CPU per pod kind from a `sample.sh` TSV |
| `devup/` | `up.sh` | Go helper: memory check, network manifests, `ATTACKS` resolution |

Each script's header comment lists its environment variables. See
[`docs/single-machine.md`](../docs/single-machine.md) for sizing.
