#!/usr/bin/env bash
# Print one line per pod of NAMESPACE: working-set memory (MiB) and CPU
# (millicores), read from the kubelet summary API, so no metrics-server is
# needed. Columns: time, pod, mem_mib, cpu_m. Reads no stdin. On a cluster of
# several nodes, NODE names the one to read.
#
# Environment: CONTEXT (default: the current context), NAMESPACE, NODE
# (default: the first node).
set -euo pipefail
exec </dev/null
CONTEXT=${CONTEXT:-$(kubectl config current-context)}
NAMESPACE=${NAMESPACE:-continuum}
k() { kubectl --context "$CONTEXT" "$@"; }
node=${NODE:-$(k get nodes -o jsonpath='{.items[0].metadata.name}')}
now=$(date +%H:%M:%S)
k get --raw "/api/v1/nodes/$node/proxy/stats/summary" |
  jq -r --arg ns "$NAMESPACE" --arg now "$now" '
    .pods[] | select(.podRef.namespace == $ns)
    | [$now, .podRef.name, ((.memory.workingSetBytes // 0) / 1048576 | floor),
       ((.cpu.usageNanoCores // 0) / 1000000 | floor)] | @tsv'
