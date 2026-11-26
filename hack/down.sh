#!/usr/bin/env bash
# Remove what hack/up.sh deployed. Pools go first, while the operator still
# runs: it holds a fence finalizer on every keyed pod, and a namespace
# deleted after the operator is gone hangs on them. Every wait is bounded.
#
# Environment: CONTEXT, NAMESPACE, UI_PORT, K_BIN, CONTINUUM_STATE_DIR.
set -uo pipefail
exec </dev/null

CONTEXT=${CONTEXT:-docker-desktop}
NAMESPACE=${NAMESPACE:-continuum}
K=${K_BIN:-kubectl}
state=${CONTINUUM_STATE_DIR:-${HOME}/.cache/continuum/$NAMESPACE}
k() { "$K" --context "$CONTEXT" "$@"; }
log() { printf '%s down: %s\n' "$(date +%H:%M:%S)" "$*"; }

if [ -f "$state/forward.pid" ]; then
  pid=$(cat "$state/forward.pid")
  # The supervisor's children are its kubectl port-forwards.
  pkill -P "$pid" 2>/dev/null
  kill "$pid" 2>/dev/null
  rm -f "$state/forward.pid"
fi

if ! k get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log "namespace $NAMESPACE is gone"
  exit 0
fi

log "deleting pools"
k -n "$NAMESPACE" delete continuumpools --all --wait=true --timeout=90s
k -n "$NAMESPACE" wait --for=delete pod -l continuum.blinklabs.io/workload --timeout=120s
log "deleting the engine"
k -n "$NAMESPACE" delete statefulset continuum-engine --ignore-not-found --wait=true --timeout=60s
log "deleting namespace $NAMESPACE"
k delete namespace "$NAMESPACE" --ignore-not-found --wait=true --timeout=180s
k delete clusterrolebinding continuum-operator --ignore-not-found >/dev/null
log "done"
