# cardano-continuum

A stochastic, adversarial Cardano population running on a live devnet.

cardano-continuum starts a devnet from a blank genesis and keeps it running. A
population of simulated wallets, pools, DReps and contracts pays, delegates,
votes and deploys with randomised timing, and retires or goes dormant over
time. Some actors misbehave on purpose. The devnet is the test subject: the
project is a stress, conformance and differential test harness for Cardano
node implementations.

## Components

| Name | Role |
| --- | --- |
| `continuum-engine` | Runs the simulated population against a devnet |
| `continuum-operator` | Kubernetes operator that deploys networks and pools |
| `continuum-forger` | Block producer that forges blocks on purpose to misbehave |
| `continuum-genesis` | Genesis Job that writes a devnet's genesis files |
| `continuum-lab` | Notebooks and scripts for analysing run output |
| `continuum` | Command-line interface over the components above |

## Running on Kubernetes

`make up` deploys the whole system onto Docker Desktop's Kubernetes: the
operator and its CRDs, the genesis Job, a devnet of cardano-node and dingo
pools, and the engine serving the live UI. It is idempotent; a second run
repairs only what is missing.

Prerequisites:

- Docker Desktop with Kubernetes enabled (kubectl context `docker-desktop`).
  Give its VM about 48 GiB of memory; `make up` warns below that
  (`MIN_MEM_GIB`). Sizing is in [docs/single-machine.md](docs/single-machine.md#budgets-and-sizing).
- `kubectl`, `docker`, `curl`, Go 1.27 and `make` on the host. Images are
  built with ko through `go run`, so ko needs no install.
- A local image registry on port 5005 (5000 is taken by macOS AirPlay):

  ```sh
  docker run -d --name continuum-registry --restart unless-stopped \
    -p 127.0.0.1:5005:5000 registry:2.8.3
  ```

Deploy, then open the UI:

```sh
make up                                    # 3 cardano-node + 1 dingo pool, 100 wallets, preprod profile
open http://127.0.0.1:8088
```

`make up` builds and pushes the continuum images to
`localhost:5005/continuum` (override with `CONTINUUM_REGISTRY`) and imports
them into the node's containerd, because the node cannot pull from a
registry on the host. It pre-pulls the cardano-node and dingo images, runs
the genesis, waits until every pool forges on one chain, deploys the engine
and keeps a `kubectl port-forward` to its UI running. The UI listens on
`127.0.0.1` only because its API is unauthenticated; from another host use
`ssh -L 8088:127.0.0.1:8088 <host>`.

Variables, passed as `make up NAME=value`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `POOLS` | `3` | cardano-node pools |
| `DINGO_POOLS` | `1` | dingo pools |
| `WALLETS` | `100` | simulated wallets |
| `PROFILES` | `preprod` | behaviour profile set from `profiles/` (`preprod`, `preview`, `mainnet`, `m1`, `m3`) |
| `SCALE` | `200` | multiplier on every profile's activity rates |
| `SEED` | fixed 64-hex value | run seed for the genesis and the population |
| `ATTACKS` | every tier-1 attack | comma-separated list, or `none` |
| `ENGINE_POOLS` | `0` | pool operators the engine simulates (keep 0 with `preprod` and `preview`) |
| `CONTEXT`, `NAMESPACE` | `docker-desktop`, `continuum` | where to deploy |
| `UI_PORT`, `UI_ADDRESS` | `8088`, `127.0.0.1` | the forwarded UI |

The genesis fixes the pool count, so run `make down` before changing `POOLS`
or `DINGO_POOLS`. When the engine binary, seed, profiles or attacks differ
from the deployed run, `make up` resets the network itself; `KEEP_RUN=1`
makes it refuse instead. Remove everything with:

```sh
make down
```

`make kind-up`, `make e2e` and `make kind-down` use a separate, throwaway
kind cluster for the end-to-end tests, not for development.

## Development

Requires Go 1.27 (see `go.mod`) and `make`. Run the full local check, which is
the same set of steps CI runs on push to `main`:

```sh
make check
```

`make check` builds, vets, checks formatting, runs the tests with the race
detector and the `debug` build tag, and runs `golangci-lint`. Install
`golangci-lint` v2.15.0 first; CI uses that version.

Simulate a wallet population paying each other on the in-process fake chain,
then replay the log and check that every decision is reproduced:

```sh
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --epochs 2 --out run1
go run ./cmd/continuum replay --seed 7 --wallets 100 run1/events.log
```

`replay` takes the same seed and population flags as `run`; the log header
refuses a log recorded under different ones. `make determinism` runs the
determinism tests under the race detector at `GOMAXPROCS=1` and `8`.

`--attack` adds one adversary actor per named tier-1 attack (`dust-flood`,
`utxo-contention`, `malformed-cbor`, `front-run`, `double-spend`), each with
its own budget and a per-epoch cost ceiling. `exunits-block-gap` is opt-in:
on a fake run it switches the chain to a genesis whose per-transaction
ExUnits exceed the per-block ones, and on a devnet it needs that genesis
(see [docs/single-machine.md](docs/single-machine.md#adversaries)).
Adversaries run only on a devnet: the engine refuses them unless the node's
genesis passes the network guard.
Compare an attack run with a control run on the same seed:

```sh
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --profiles m3 --out control
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --profiles m3 --attack front-run --out attack
go run ./cmd/continuum compare control/events.log attack/events.log
```

The table puts honest failure rate, confirmation latency, retries, UTxO
growth, node rejections and oracle findings beside each adversary's cost,
landed steps and detected steps. A star marks the attack's success metric.
On the fake chain a front-runner's transaction always reaches the node
before the honest one it outbids. Cardano mempools keep the first
transaction they see, whatever its fee, so this stands in for an adversary
whose transaction reaches the block producer first.

On a live devnet the same engine runs against a cardano-node's
node-to-client endpoint, funding its wallets from the genesis faucet and
resuming from `--state` after a restart:

```sh
CONTINUUM_SEED=<the genesis Job's seed> go run ./cmd/continuum run \
  --node pool-0:3002 --genesis shelley-genesis.json --state state --wallets 20 --epochs 0
```

`deploy/engine` runs it in Kubernetes as a one-replica StatefulSet. It reaches
a pool through the operator's `ntc` sidecar, which bridges the node's socket
to TCP port 3002 (cardano-node pools only for now). `make e2e` (about an
hour) boots the devnet in kind and runs `TestPayments` against it.

The operator resolves dev images by digest itself. Inside its pod
`localhost:<port>` is the pod, not the registry the images were pushed to,
so give it `--registry-mirror localhost:5001=<host the pod reaches>`.

## Web UI

`continuum serve --log run1/events.log` serves the web UI and its API on
`127.0.0.1:8088`. The single-page app (Preact, Vite, TypeScript) is in `web/`;
its build, `web/dist`, is committed and embedded in the binary, so Go builds
need no Node. After changing `web/src`, run `make web` and commit `web/dist`;
`make web-test` typechecks and runs the unit tests. Screenshots are in
`docs/screenshots/`; the `-sample` ones use stand-in API data because a fake
run has no pools and writes its log only when it ends.

TLA+ models live under `models/`. Install the pinned tools once, then check
every model:

```sh
make models-install
make models
```

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
