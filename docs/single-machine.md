# Running continuum on one machine

`make up` deploys the whole system onto Docker Desktop's Kubernetes (context
`docker-desktop`, namespace `continuum`); `make down` removes it.

```sh
make up                                    # 3 cardano-node + 1 dingo pool, 100 wallets, preprod profile
make up POOLS=8 DINGO_POOLS=2 WALLETS=300  # a bigger devnet
make down
```

Variables: `CONTEXT`, `NAMESPACE`, `PROFILES`, `POOLS` (cardano-node),
`DINGO_POOLS`, `WALLETS`, `SCALE`, `SEED` (64 hex), `ENGINE_POOLS` (pool operators the
engine simulates; keep 0 with `preprod` and `preview`, which have no pool
profile), `ATTACKS`, `UI_PORT`, `UI_ADDRESS`.

## Adversaries

`make up` runs the engine's tier-1 adversaries by default: one actor per
attack, each with the built-in 5,000 ADA budget and 300 ADA per-epoch cost
ceiling (`internal/adversary/profiles.go`). The default is every attack a
stock genesis supports: `dust-flood`, `utxo-contention`, `malformed-cbor`,
`front-run` and `double-spend`. A correctly behaving network rejects every
malformed and double-spend transaction; those rejections are the verdicts the
oracle expects, not findings.

```sh
make up                                     # the default attacks
make up ATTACKS=none                        # an honest network only
make up ATTACKS=dust-flood,exunits-block-gap
```

`exunits-block-gap` is opt-in. It needs a genesis whose per-transaction
execution budget exceeds the per-block one, which `continuum-genesis` refuses
unless told it is deliberate. Naming the attack makes `make up` pass
`-override=maxTxExUnits.memory=140000000 -override=maxTxExUnits.steps=20000000000
-allow-inconsistent-exunits` to the genesis Job (`go run ./hack/devup attacks
<ATTACKS>` prints them). The adversary then submits transactions that fit the
transaction budget but no block; the oracle requires that none is ever
forged and files a fatal `block_exunits_over_limit` for any block over
`maxBlockExUnits`. The genesis fixes the attacks' genesis needs, so change
`ATTACKS` only after `make down`.

The tier-2 economic models in `internal/adversary` (proposal spam, mempool
flood, pledge splitting) are standalone calculations with no engine step,
and the tier-2 rogue peer (`internal/rogue`) is a node-to-node client the
engine does not run, so `make up` cannot enable either.

`SCALE` (default 200, passed to the engine's `--scale`) multiplies every
profile's activity rates and keeps their shapes. The preprod profile gives
about 0.2 transactions per day per active unit, which at wall-clock speed is
near silence for 100 wallets; a scale of 200 gives a few transactions a
minute. The trade-off: a scaled run is a stress picture, not the measured
network, and grading against the profile's target uses the unscaled target,
so rates and counts compared with it are off by the factor. Use `SCALE=1`
for a run that is graded.

`make up` checks that the context exists, that allocatable memory is at least
48 GiB (a warning only; Docker Desktop with 48 GB reports about 47 GiB, so
expect the warning there) and that the registry answers, then builds the
operator, genesis and engine images with ko into `localhost:5005/continuum`,
loads them into the node, installs the CRDs and operator, runs the genesis
Job, creates the network and pools, waits for one chain (10 blocks forged,
every cardano-node tip within k), starts the engine with `--serve` and
port-forwards its UI. It is idempotent. The genesis fixes the pool count and
the seed, so change either only after `make down`.

An engine resumes its event log only under the binary, seed, profile set
and attacks that wrote it; a pod given another refuses its log ("eventlog:
header mismatch") and crash-loops. `make up` records those on the engine
StatefulSet (annotation `continuum.blinklabs.io/run`, computed by `go run
./hack/devup run`), and when a deploy would change them it logs why, for
example "engine binary changed: a run can't be resumed by a different
binary; resetting the network", and runs `make down` first, so the chain
and the engine store start fresh. `make up KEEP_RUN=1` refuses instead.
A deploy from before the annotation existed counts as changed.

`make down` deletes the pools while the operator still runs, since the
operator's fence finalizer on each keyed pod blocks namespace deletion
otherwise, then the engine, then the namespace.

## Reaching the UI

`http://127.0.0.1:8088` on the machine running Docker Desktop: a supervised
`kubectl port-forward` to the engine pod's loopback (pid and log under
`~/.cache/continuum/<namespace>/`). The API has no authentication, so it
listens on `127.0.0.1` only. From another host use
`ssh -L 8088:127.0.0.1:8088 <host>`; `UI_ADDRESS=0.0.0.0` exposes it to the
LAN and with it the control endpoints.

## The node cannot pull from the host registry

Docker Desktop's node routes every pull through a mirror that fails for a
registry on the host. `make up` therefore pushes with ko, pulls into the host's
Docker, and imports into the node's containerd (as `kind load` does); pods
name the image by a tag derived from the digest, with the default
IfNotPresent policy. Set `NODE_CONTAINER` if the node container is not
`desktop-control-plane`.

## Measurements

Docker Desktop, 1 node, 14 CPUs, 46.96 GiB allocatable, arm64, Conway devnet
(epoch 500 slots of 1 s, k=5, f=0.05), preprod engine profile. Memory is the
kubelet summary API's working set (there is no metrics-server), CPU its
usage in millicores, sampled every 20 s over three epochs (the chain reached
epoch 3). Collected with `hack/sample.sh` and `hack/summarize.py`. "Steady" is
the median of the last third of samples; per pod.

| Pod | Small run: steady | peak | Big run: steady | peak |
| --- | --- | --- | --- | --- |
| cardano-node pool | 73 MiB, 4m | 75 MiB, 8m | 73 MiB, 4m | 84 MiB, 7m |
| dingo pool | 533 MiB, 16m | 541 MiB, 22m | 436 MiB, 16m | 566 MiB, 24m |
| engine | 26 MiB, 1m | 28 MiB, 2m | 31 MiB, 1m | 32 MiB, 11m |
| operator | 25 MiB, 2m | 27 MiB, 5m | 27 MiB, 4m | 29 MiB, 10m |
| all pods, total | 803 MiB | 819 MiB (43m CPU) | 1515 MiB | 1801 MiB (87m CPU) |

Small run: 3 cardano-node + 1 dingo, 100 wallets. Big run: 8 cardano-node +
2 dingo, 300 wallets. The whole node container (Kubernetes control plane
included, `docker stats`) held 2.05 GiB with the small run and 3.17 GiB with
the big one. Every run ended on one chain (all tips equal), no pod restarted,
the engine reported no incidents and conserved supply. Memory did not grow
from epoch to epoch.

Time to a running UI: about 9 minutes the first time (image build and
pre-pull), then about 8 minutes: the genesis start delay is 2 minutes and the
chain needs 10 blocks (f=0.05) before the engine starts.

The devnet chain is tiny. A dingo or cardano-node following a real network
(preprod or mainnet) needs far more memory than the table; the table sizes
devnet pools only.

## Budgets and sizing

The pool drivers request 250m CPU and 1024 MiB per pool, and the budget counts
requests, not usage. Measured usage is far below the request (73 MiB for a
cardano-node, about 540 MiB for dingo), so the request is the binding number,
not the machine.

`ContinuumNetwork.spec.budget` now defaults to 16 pools, 4000m CPU and
16384 MiB when omitted (`v1alpha1.DefaultMaxPools`, `DefaultCPUMillis`,
`DefaultMemoryMiB`; `TestBudgetDefaults` pins the CRD defaults to them). That
is a third of 48 GiB in requests, and sixteen dingo pools would use about
9 GiB measured, so the budget leaves four times headroom for chain growth,
longer runs and the engine. `make up` accepts at most 16 pools
(`devup.MaxPools`).

Measured up to 10 pools; 16 is an extrapolation from the per-pool numbers
(16 x dingo 566 MiB peak is about 9 GiB, plus about 1.8 GiB node baseline),
not a run. Raise the budget in the ContinuumNetwork to go beyond it, and
watch `hack/podstats.sh`.
