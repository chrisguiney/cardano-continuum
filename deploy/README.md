# deploy

Hand-written Kubernetes manifests that are not part of the cluster install.
The system itself installs with the Helm chart in
[`charts/continuum`](../charts/continuum/README.md) (`make up`).

| Directory | Component |
|---|---|
| [`lab/`](lab/README.md) | the calibration notebook |

The CRDs and the operator's ClusterRole are generated, in
[`config/`](../config/README.md), and copied into the chart.
