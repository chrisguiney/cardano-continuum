# deploy/operator

`operator.yaml`: the operator's ServiceAccount, its single-replica Deployment
(`--leader-elect=false`) and a ClusterRoleBinding to the `continuum-operator`
ClusterRole in [`config/rbac/role.yaml`](../../config/rbac/role.yaml).
`hack/up.sh` applies the CRDs and that role first, then replaces the image and
the binding's namespace in this file and applies it.
