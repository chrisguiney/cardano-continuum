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
the engine with its live API and the web UI in front of it. `make up` builds the images and drives the
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
`continuum-operator`, `continuum-genesis` and `continuum-ui` with ko for `PLATFORM`
(default `linux/` and this machine's architecture), pushes them and records
their digests in `.tools/images`. It then applies the chart's CRDs, which
Helm never upgrades, and runs `helm upgrade --install` with those digests
and the variables below. The UI comes up first: as soon as the `continuum-ui`
Deployment is Ready, `make up` starts a `kubectl port-forward` to its Service
on `127.0.0.1` and prints the URL, before the devnet has forged anything. The
page shows a "devnet is starting" banner with empty views, polls the proxy's
`/api/_ui/status`, and fills in by itself once the engine answers. `make up`
then waits, bounded, for the genesis Job, for every pool to forge on one
chain and for the engine, keeping the forward running. The UI is its own Deployment (`continuum-ui proxy`), which
forwards `/api` to the engine's ClusterIP `continuum-api` Service, so a
change to `web/` alone rolls the UI pod and keeps the run: the run identity
names only the engine image, and the engine binary does not link the UI.
Neither Service has authentication; from another host use
`ssh -L 8088:127.0.0.1:8088 <host>`. `ui.service.type` makes the UI a
NodePort or LoadBalancer instead, reachable by anything that reaches it.

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
| `ENGINE_POOLS` | `0` | pool operators the engine simulates beside the profiles' pool populations, which operate the genesis pools (keep 0 with `preprod` and `preview`) |
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

## Protocol version and hard forks

The devnet starts at protocol version 10.0 (Conway, past the bootstrap
phase, so DReps vote), where mainnet, preprod and preview are at 11.0. The
`hard_fork` scenario forks it: on the UI's Controls page, inject
`hard_fork` (or `hard_fork:11.0`), or post the control to the live API:

```sh
curl -X POST http://127.0.0.1:8088/api/control \
  -d '{"request_id":"fork","op":"inject","scenario":"hard_fork"}'
```

The richest actor with a registered stake key proposes a
HardForkInitiation to that version, which must be able to follow the one
in force: the next major with minor 0, or the next minor. The committee,
the DReps and the stake pools vote on it, and it needs all three
thresholds: two thirds of the committee, 60% of the DRep stake and 51% of
the pool stake, a pool that did not vote counting as no. Once the votes
pass, an epoch boundary ratifies it and the next boundary enacts it; the
scenario refuses a second fork until then. The Governance page and the
Overview show the version in force and the pending fork.

Every node must reach the new version at that boundary: a node still at
the old one is a fatal `protocol_version_mismatch` finding. From 11.0 the
engine sends a committee vote with its hot-key authorisation in one
transaction, which cardano-node's mempool refuses below 11.

## The ADA market

A profile file's `markets:` section (`profiles/m1.yaml` spells out every
default) gives the wallets an ADA market (`internal/market`): an ADA/USD
path, each currency's USD rate and each exchange's quotes, all a function of
the run seed and simulated time, compressed so minutes of a fake run see
rallies and drawdowns. Each exchange is a wallet actor whose hot wallet the
genesis or the faucet funds. A wallet has one primary currency and, now and
then, more; it buys ADA with fiat (the exchange pays it on chain, less a
withdrawal fee) and sells ADA to an exchange, at the best effective rate.
Fiat stays off chain, so the value check covers every lovelace.

In a market run, wallet births per currency rise with ADA's gains against
that currency, and a profile with `hazards.market: true` goes dormant and
abandons faster in a prolonged fall, selling its ADA first; these lifecycle
rates are synthetic parameters, not fitted from chain data. Under water, a
hodl wallet holds; any other sells by its `loss_aversion` trait and its
impatience. The Markets page, `/api/markets` and the fake run's `markets:`
summary line show the rates, the flows by exchange, births by currency and
who sold or held in the largest drawdown.

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

### Smart contracts

`script_call`, `mint` and `burn` run Aiken validators: a shared lock,
vesting, escrow and a free-mint policy, in `contracts/` (see
[contracts/README.md](contracts/README.md)). Their compiled blueprint and
the Go registry generated from it are committed, so Go builds need no
Aiken. `make contracts` rebuilds them with the pinned compiler (downloaded,
checksum-verified, into `.tools/aiken`); `make contracts-check` fails when
the rebuild differs from what is committed, and runs in CI. Every
submitted script transaction's event-log record carries each redeemer's
run: validator, script hash, purpose, datum and redeemer, units declared
and used, and the verdict. The web UI shows them on the transaction page
and on the Contracts page.

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

`continuum-ui serve --log run1/events.log` serves the web UI and its API on
`127.0.0.1:8088`; it takes `continuum serve`'s flags. `continuum serve` and
`continuum run --serve` answer the JSON API only, and
`continuum-ui proxy --api http://127.0.0.1:8088 --addr 127.0.0.1:8089` puts
the UI in front of either. The single-page app (React, Vite, TypeScript,
Tailwind CSS, shadcn/ui) is in `web/`; its build, `web/dist`, is committed and embedded in
`continuum-ui` (never in `continuum`), so Go builds need no Node. After changing `web/src`, run `make web` and commit `web/dist`;
`make web-test` typechecks and runs the unit tests. Screenshots are in
`docs/screenshots/`; the `-sample` ones use stand-in API data because a fake
run has no pools and writes its log only when it ends.

TLA+ models live under `models/`. Install the pinned tools once, then check
every model:

```sh
make models-install
make models
```

`make models-changed` checks only the models changed since `BASE`
(default `origin/main`), or all of them when the model runner or its tools
changed.

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
