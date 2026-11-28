# cardano-continuum

A stochastic, adversarial simulator for Cardano networks.

cardano-continuum is an agent-based simulator. It starts a real Cardano devnet
from a blank genesis, with cardano-node and dingo pools side by side, and
drives it with a simulated population of users for as long as you let it run.

What it simulates:

- **Wallets** that pay each other, delegate, trade ADA on exchanges, go
  dormant and leave, with randomised timing drawn from profiles fitted to
  mainnet, preprod and preview.
- **Stake pools, DReps and a constitutional committee** that register,
  propose and vote on governance actions, through to ratification and
  enactment.
- **Smart contracts** written in Aiken: locks, vesting, escrow and minting.
- **An ADA market** with several currencies and exchanges, where rallies
  bring new users and drawdowns make some sell and others hold.
- **Adversaries** that misbehave on purpose: dust floods, double spends,
  front-running, malformed transactions and more.

What it checks: the devnet is the test subject. An oracle watches every
transaction and block and files a finding when nodes disagree, a rule is
broken, the chain forks too deep or stalls, or value goes missing. That makes
it a stress, conformance and differential test harness for Cardano node
implementations.

Every run is a function of its seed: the same seed replays the same decisions
byte for byte, so any finding can be reproduced.

## Components

| Name | Role |
| --- | --- |
| `continuum-engine` | Runs the simulated population against a devnet |
| `continuum-ui` | Serves the web UI and forwards its API calls to the engine |
| `continuum-operator` | Kubernetes operator that deploys networks and pools |
| `continuum-forger` | Block producer that forges bad blocks on purpose |
| `continuum-genesis` | Genesis Job that writes a devnet's genesis files |
| `continuum-lab` | Notebooks and scripts for analysing run output |
| `continuum` | Command-line interface over the components above |

## Running on Kubernetes

The Helm chart in [`charts/continuum`](charts/continuum/README.md) installs
everything into one namespace of any Kubernetes cluster: the operator and its
CRDs, the genesis Job, a devnet of cardano-node and dingo pools, the engine,
and the web UI. `make up` builds the images and installs the chart for you.

### Prerequisites

- A cluster with about 48 GiB of allocatable memory for the default devnet
  (`make up` warns below `MIN_MEM_GIB`), and a default StorageClass (or set
  `genesis.storage.storageClassName` and `engine.storage.storageClassName`).
  Sizing is in [docs/single-machine.md](docs/single-machine.md#budgets-and-sizing).
- An image registry you can push to and the cluster's nodes can pull from,
  named by `CONTINUUM_REGISTRY` (`host[:port]/path`). For a plain-HTTP
  registry, also set `CONTINUUM_REGISTRY_INSECURE=true`, and allow it in the
  nodes' container runtime.
- `kubectl` with a context for the cluster, `jq`, `curl`, Go 1.27 and `make`.
  ko and helm run through `go run` at pinned versions, so you don't install
  them.

### Deploy

```sh
export CONTINUUM_REGISTRY=registry.example.com/continuum
make up CONTEXT=my-cluster PROFILES=mainnet
open http://127.0.0.1:8088
```

The UI opens within about a minute, before the devnet has forged a block. It
shows a "devnet is starting" banner and fills in by itself once the engine is
running, usually a few minutes later.

To remove everything:

```sh
make down
```

### What `make up` does

1. Builds the four images (`continuum`, `continuum-operator`,
   `continuum-genesis`, `continuum-ui`) with ko for `PLATFORM`, pushes them,
   and records their digests in `.tools/images`.
2. Applies the chart's CRDs (Helm never upgrades CRDs itself), then runs
   `helm upgrade --install` with those digests and the variables below.
3. As soon as the UI is ready, starts a `kubectl port-forward` to it on
   `127.0.0.1` and prints the URL.
4. Waits, with time limits, for the genesis Job, for every pool to forge on
   one chain, and for the engine.

The UI runs apart from the engine: a change under `web/` only restarts the UI
pod, and the simulation keeps running. Neither the UI nor the API has
authentication. From another host, tunnel to it with
`ssh -L 8088:127.0.0.1:8088 <host>`; setting `ui.service.type` to NodePort or
LoadBalancer exposes it to anything that can reach the cluster.

### Variables

Pass them as `make up NAME=value`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROFILES` | `preprod` | behaviour profile set from `profiles/` (`preprod`, `preview`, `mainnet`, `m1`, `m3`) |
| `POOLS` | `3` | cardano-node pools |
| `DINGO_POOLS` | `1` | dingo pools |
| `WALLETS` | `100` | simulated wallets at the start |
| `SCALE` | `200` | multiplier on every profile's activity rates |
| `SEED` | fixed 64-hex value | run seed for the genesis and the population |
| `ATTACKS` | every tier-1 attack | comma-separated list, or `none` |
| `ENGINE_POOLS` | `0` | extra pool operators the engine simulates; keep 0 with `preprod`, `preview` and `mainnet`, whose pool populations operate the genesis pools |
| `CONTEXT` | the current context | kubectl context; read once, never switched |
| `NAMESPACE`, `RELEASE` | `continuum`, `continuum` | where to install, and the Helm release name |
| `VALUES` | none | an extra chart values file (the variables above take precedence) |
| `IMAGE_REF` | `digest` | `tag` names the images by a tag of their digest instead (see [docs/single-machine.md](docs/single-machine.md)) |
| `PLATFORM` | `linux/<this machine's arch>` | the nodes' platform, for `make images` |
| `UI_PORT`, `UI_ADDRESS` | `8088`, `127.0.0.1` | where the UI is forwarded |

### When a deploy resets the devnet

A running engine can only resume its run under the binary, seed, profiles and
attacks that started it, and the genesis fixes the pool counts and seed. If
any of these differ from what's deployed, `make up` resets the devnet (a
`make down`, then a fresh install). Set `KEEP_RUN=1` to make it refuse
instead. Changes to the UI alone never reset it.

### Installing the chart without make

Push the images yourself, then:

```sh
helm upgrade --install continuum charts/continuum -n continuum --create-namespace \
  --set images.registry=$CONTINUUM_REGISTRY \
  --set images.engine.digest=sha256:... --set images.operator.digest=sha256:... \
  --set images.genesis.digest=sha256:...
```

The pools and the engine only become ready after the genesis Job has pulled
the node images and published the genesis. On a first pull that can outlast
Helm's default timeout, so with `--wait` pass `--timeout 15m` or more. Every
value is described in [`charts/continuum/values.yaml`](charts/continuum/values.yaml).

## Profiles

A profile set describes the population: which kinds of wallet exist, how
often they act, what they prefer to do, and how they come and go. The fitted
sets (`mainnet`, `preprod`, `preview`) are measured from each network's chain
data; `m1` and `m3` are small hand-written sets for tests.

The mainnet set is the richest: wallets, pools, DReps, contracts and
governance proposals, plus the ADA market. Its fitted rates are slow (a wallet
sends about once in half an hour at scale 1), so the default `SCALE=200`
speeds everything up enough to watch.

## The ADA market

Profiles with a `markets:` section (`mainnet`, `preprod`, `preview`, and `m1`,
which spells out every default) give the wallets an ADA market:

- An ADA/USD price that moves through bull and bear spells and sudden jumps,
  and an exchange rate for each currency. Time is compressed, so a run of
  minutes sees rallies and drawdowns.
- Several exchanges, each quoting slightly different prices. An exchange is a
  wallet on chain: buying ADA means the exchange pays you on chain, less a
  withdrawal fee; selling means you pay the exchange. Fiat stays off chain.
- Each wallet has a home currency, and sometimes others, and trades where the
  price is best for it.
- New wallets arrive faster in a currency against which ADA is rising. In a
  long fall, wallets go dormant and leave faster, selling their ADA first.
  These rates are synthetic, not fitted from chain data.
- In a loss, a "hodl" wallet holds on; others sell according to their loss
  aversion and impatience.

The Markets page shows the prices, the trades by exchange, new wallets by
currency, and who sold or held in the largest drawdown.

## Running without a cluster

The engine also runs against an in-process fake chain, which is the fastest
way to try a profile or reproduce a finding:

```sh
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --profiles mainnet --scale 200 --epochs 12 --out run1
go run ./cmd/continuum replay --seed 7 --wallets 100 --profiles mainnet --scale 200 run1/events.log
go run ./cmd/continuum-ui serve --log run1/events.log
```

`replay` checks that every decision is reproduced. It takes the same seed and
population flags as `run`, and refuses a log recorded under different ones.
`continuum-ui serve` shows a recorded run in the web UI at
`http://127.0.0.1:8088`.

## Adversaries

`--attack` (or `ATTACKS` for `make up`) adds one adversary actor per named
attack: `dust-flood`, `utxo-contention`, `malformed-cbor`, `front-run` and
`double-spend`. Each has its own budget and a per-epoch spending ceiling.
`exunits-block-gap` is opt-in: it needs a genesis whose per-transaction
execution limits exceed the per-block ones (see
[docs/single-machine.md](docs/single-machine.md#adversaries)). The engine
refuses adversaries unless the node's genesis is a devnet's.

To measure an attack, compare it with a control run on the same seed:

```sh
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --profiles m3 --out control
go run ./cmd/continuum run --fake --seed 7 --wallets 100 --profiles m3 --attack front-run --out attack
go run ./cmd/continuum compare control/events.log attack/events.log
```

The comparison puts the honest population's failure rate, confirmation
latency, retries, UTxO growth, node rejections and findings beside each
adversary's cost and how many of its steps landed or were caught. A star
marks the metric that says whether the attack worked. On the fake chain a
front-runner's transaction always reaches the node first, which stands in for
an adversary that reaches the block producer first: Cardano mempools keep the
first transaction they see, whatever its fee.

## Smart contracts

Script transactions run Aiken validators from [`contracts/`](contracts/README.md):
a shared lock, vesting, escrow and a free-mint policy. The compiled
blueprint and the Go registry generated from it are committed, so Go builds
don't need Aiken.

- `make contracts` rebuilds them with the pinned compiler, downloaded and
  checksum-verified into `.tools/aiken`.
- `make contracts-check` fails when a rebuild differs from what's committed;
  CI runs it.

Each script run is recorded with its validator, script hash, purpose, datum,
redeemer, execution units declared and used, and the result. The web UI shows
them on the transaction page and the Contracts page.

## Web UI

The UI is a single-page app (React, Vite, TypeScript, Tailwind CSS,
shadcn/ui) in `web/`. Its build, `web/dist`, is committed and embedded in
`continuum-ui`, so Go builds don't need Node.

- `continuum-ui serve --log run1/events.log` shows a recorded run.
- `continuum-ui proxy --api http://127.0.0.1:8088 --addr 127.0.0.1:8089`
  puts the UI in front of a running engine's API (`continuum serve` and
  `continuum run --serve` serve the API only).
- After changing `web/src`, run `make web` and commit `web/dist`;
  `make web-test` typechecks and runs the unit tests.

Screenshots are in `docs/screenshots/`.

## Development

Requires Go 1.27 (see `go.mod`), `make`, and golangci-lint v2.15.0, the
version CI uses.

```sh
make check          # build, vet, format, lint, race tests, chart and manifest checks: what CI runs
make determinism    # same seed, same log; replay; crash recovery
```

### Live devnet internals

On a live devnet the engine talks to a cardano-node over node-to-client,
funds its wallets from the genesis faucet, and resumes from `--state` after a
restart. The chart runs it as a one-replica StatefulSet, reaching a pool
through the operator's `ntc` sidecar, which bridges the node's socket to TCP
port 3002 (cardano-node pools only for now). To run it by hand:

```sh
CONTINUUM_SEED=<the genesis Job's seed> go run ./cmd/continuum run \
  --node pool-0:3002 --genesis shelley-genesis.json --state state --wallets 20 --epochs 0
```

The operator resolves dev images by digest itself. Inside its pod,
`localhost:<port>` is the pod, not your registry, so give it a mirror the pod
can reach: chart value `operator.registryMirror`,
`localhost:<port>=<host the pod reaches>`.

### TLA+ models

The designs behind recovery, retries, the planner, forging, the stake floor
and governance are TLA+ models under `models/`. Install the pinned tools
once, then check them:

```sh
make models-install
make models           # every model
make models-changed   # only those changed since BASE (default origin/main)
```

### End-to-end tests

`make e2e` runs `test/e2e` against a cluster you provide. It takes about 90
minutes on a small cluster (`E2E_TIMEOUT`, default 150m; `go test -short`
skips the slowest tests). It never picks a cluster itself: `E2E_CONTEXT` must
name the kubectl context.

```sh
make e2e E2E_CONTEXT=my-cluster CONTINUUM_REGISTRY=registry.example.com/continuum
```

Each test installs the chart into its own namespace
(`continuum-e2e-<test>`) and removes it afterwards; the CRDs stay. If the
nodes reach the registry under a different name than the one this machine
pushes to, set `E2E_CLUSTER_REGISTRY`. CI doesn't run this suite, because
GitHub's runners have no cluster.

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
