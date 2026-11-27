# charts/continuum

The Helm chart that installs continuum into one namespace of any Kubernetes
cluster: the operator, the genesis Job, the `ContinuumNetwork` and its pools,
and the engine with its UI. `make up` drives it; see the repository
[README](../../README.md#running-on-kubernetes). Every value is described in
[`values.yaml`](values.yaml) and typed in `values.schema.json`.

| Path | Holds |
|---|---|
| `crds/` | the CRDs, copied from `config/crd/bases` by `make manifests` |
| `files/operator-role.yaml` | the operator's rules, copied from `config/rbac/role.yaml` |
| `templates/operator.yaml` | ServiceAccount, ClusterRole and binding (named after the namespace), Deployment |
| `templates/genesis.yaml` | seed Secret, genesis volume, the Job's RBAC, the Job |
| `templates/network.yaml` | the `devnet` network and pools `p0`..`pN` |
| `templates/engine.yaml` | RBAC, Services, the engine StatefulSet |
| `ci/nondefault-values.yaml` | the second values file `make chart-check` renders |

Do not edit `crds/` or `files/` by hand: `make check-manifests` fails when
they differ from `config/`.

## Start order

Nothing waits on Helm hooks; each component waits for what it needs:

1. The genesis Job pulls the node images (`genesis.prepull`), renders the
   genesis with systemStart `genesis.startDelay` ahead, and publishes the
   `devnet-genesis` ConfigMap and the `keys-<i>` Secrets.
2. The operator creates every pool at once; a pool's pod starts when the
   kubelet can mount its Secret and the ConfigMap.
3. The engine mounts `devnet-genesis` and its init container waits until pool
   0's node-to-client bridge accepts a connection.

Install without `--wait`: the pools and the engine become ready only after
the Job has published, which can take longer than Helm's default timeout on
a first pull. `make up` waits itself, with bounds.

## Upgrades and resets

The genesis is rendered once. The Job's name carries a digest of its image
and arguments, so an upgrade with a new genesis image replaces the Job, and
the new one keeps the volume's genesis and republishes it. Changing the pool
counts, the seed, the network parameters or `genesis.overrides` changes the
genesis itself: the new Job fails with "holds a devnet for spec", and the
release must be uninstalled (with its namespace) first. `make up` does that,
and resets the engine's run, when `devup run` reports a change.

`helm uninstall` leaves the CRDs, as Helm does for `crds/`.
