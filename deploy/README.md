# deploy

Hand-written Kubernetes manifests for the system's components. `hack/up.sh`
(`make up`) applies the operator, genesis and engine ones, substituting image
references and the namespace; the end-to-end tests apply the genesis and
engine ones too.

| Directory | Component |
|---|---|
| [`operator/`](operator/README.md) | the operator |
| [`genesis/`](genesis/README.md) | the genesis Job |
| [`engine/`](engine/README.md) | the engine StatefulSet and its RBAC |
| [`lab/`](lab/README.md) | the calibration notebook (not applied by `make up`) |

The CRDs and the operator's ClusterRole are generated, in
[`config/`](../config/README.md).
