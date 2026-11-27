# config

Kubernetes manifests generated from Go markers by controller-gen. Nothing here
is edited by hand.

- [`crd/bases/`](crd/bases/README.md): the CustomResourceDefinitions.
- [`rbac/`](rbac/README.md): the operator's ClusterRole.

`make manifests` regenerates both; `make check-manifests` (part of
`make check`) fails if regenerating changes anything under `config/` or `api/`.
