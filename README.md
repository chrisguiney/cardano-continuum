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

The Helm chart in [`charts/continuum`](charts/continuum/README.md) installs
the whole system into one namespace of any Kubernetes cluster: the operator
and its CRDs, the genesis Job, a devnet of cardano-node and dingo pools, and
the engine serving the live UI. `make up` builds the images and drives the
chart; `helm install` works as well.

Prerequisites:

- A cluster with about 48 GiB of allocatable memory for the default devnet
  (`make up` warns below `MIN_MEM_GIB`), and a default StorageClass or
  `genesis.storage.storageClassName` and `engine.storage.storageClassName`.
  Sizing is in [docs/single-machine.md](docs/single-machine.md#budgets-and-sizing).
- An image registry you can push to and the cluster's nodes can pull from,
  named by `CONTINUUM_REGISTRY` (`host[:port]/path`). For a plain-HTTP
  registry also set `CONTINUUM_REGISTRY_INSECURE=true`; the nodes' container
  runtime must allow it too, which is the cluster's own configuration.
- `kubectl` with a context for the cluster, `jq`, `curl`, Go 1.27 and `make`.
  ko and helm run through `go run` at pinned versions, so neither needs an
  install.

Deploy, then open the UI:

```sh
export CONTINUUM_REGISTRY=registry.example.com/continuum
make up CONTEXT=my-cluster                 # 3 cardano-node + 1 dingo pool, 100 wallets, preprod profile
open http://127.0.0.1:8088
```

`make up` runs `make images`, which builds `continuum`,
`continuum-operator` and `continuum-genesis` with ko for `PLATFORM`
(default `linux/` and this machine's architecture), pushes them and records
their digests in `.tools/images`. It then applies the chart's CRDs, which
Helm never upgrades, and runs `helm upgrade --install` with those digests
and the variables below. It waits, bounded, for the genesis Job, for every
pool to forge on one chain and for the engine, and keeps a
`kubectl port-forward` to the UI running. The UI listens on the engine pod's
`127.0.0.1` because its API is unauthenticated; from another host use
`ssh -L 8088:127.0.0.1:8088 <host>`. `engine.ui.expose=true` puts it behind
a Service instead, reachable by anything that reaches the Service.

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
| `CONTEXT` | the current context | kubectl context; read once, never switched |
| `NAMESPACE`, `RELEASE` | `continuum`, `continuum` | where to install, and the Helm release |
| `VALUES` | none | an extra chart values file (the variables above take precedence) |
| `IMAGE_REF` | `digest` | `tag` names the images by a tag of their digest instead (see [docs/single-machine.md](docs/single-machine.md)) |
| `PLATFORM` | `linux/<this machine's arch>` | the nodes' platform, for `make images` |
| `UI_PORT`, `UI_ADDRESS` | `8088`, `127.0.0.1` | the forwarded UI |

The genesis fixes the pool counts, the seed and the genesis overrides, and
an engine resumes only under the binary, seed, profiles and attacks that
started its run. When any of them differ from the deployed release,
`make up` resets the network (`make down`, then a fresh install); `KEEP_RUN=1`
makes it refuse instead. Remove everything with:

```sh
make down
```

Without make, push the images yourself and install the chart:

```sh
helm upgrade --install continuum charts/continuum -n continuum --create-namespace \
  --set images.registry=$CONTINUUM_REGISTRY \
  --set images.engine.digest=sha256:... --set images.operator.digest=sha256:... \
  --set images.genesis.digest=sha256:...
```

The pools and the engine become ready only once the genesis Job has
pulled the node images and published the genesis, which on a first pull can
outlast Helm's default timeout: with `--wait`, pass a `--timeout` of 15
minutes or more. Every value is described in
[`charts/continuum/values.yaml`](charts/continuum/values.yaml).

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

The chart runs it in Kubernetes as a one-replica StatefulSet. It reaches
a pool through the operator's `ntc` sidecar, which bridges the node's socket
to TCP port 3002 (cardano-node pools only for now).

The operator resolves dev images by digest itself. Inside its pod
`localhost:<port>` is the pod, not the registry the images were pushed to,
so give it a mirror the pod reaches (chart value `operator.registryMirror`,
`localhost:<port>=<host the pod reaches>`).

### End-to-end tests

`make e2e` (about 90 minutes on a small cluster, bounded by `E2E_TIMEOUT`,
default 150m; `go test -short` skips the slowest) runs
`test/e2e` against a cluster you provide. It never picks a cluster itself:
`E2E_CONTEXT` must name the kubectl context, and every call is pinned to it.

```sh
make e2e E2E_CONTEXT=my-cluster CONTINUUM_REGISTRY=registry.example.com/continuum
```

Each test builds the images with ko, installs the chart into a namespace of
its own (`continuum-e2e-<test>`), and uninstalls it and deletes the
namespace afterwards. The CRDs are applied and left in place. When the
nodes reach the registry under another name than this machine pushes to,
set `E2E_CLUSTER_REGISTRY` to the nodes' name for it. CI does not run the
suite: GitHub's runners have no cluster.

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
