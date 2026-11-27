#!/usr/bin/env bash
# Remove what hack/up.sh installed: the Helm release RELEASE and the
# namespace. Pools go first, while the operator still runs: it holds a fence
# finalizer on every keyed pod, and a namespace deleted after the operator is
# gone hangs on them. The CRDs stay, as Helm leaves them. Every wait is
# bounded. CONTEXT empty means kubectl's current context, read once.
#
# Environment: CONTEXT, NAMESPACE, RELEASE, HELM, K_BIN, CONTINUUM_STATE_DIR.
set -uo pipefail
exec </dev/null

CONTEXT=${CONTEXT:-}
NAMESPACE=${NAMESPACE:-continuum}
RELEASE=${RELEASE:-continuum}
HELM=${HELM:-helm}
K=${K_BIN:-kubectl}
state=${CONTINUUM_STATE_DIR:-${HOME}/.cache/continuum/$NAMESPACE}
if [ -z "$CONTEXT" ]; then
  CONTEXT=$("$K" config current-context 2>/dev/null) || { echo "kubectl has no current context: set CONTEXT" >&2; exit 1; }
fi
k() { "$K" --context "$CONTEXT" "$@"; }
# shellcheck disable=SC2086
helm_() { $HELM --kube-context "$CONTEXT" "$@"; }
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

log "deleting pools in $CONTEXT/$NAMESPACE"
k -n "$NAMESPACE" delete continuumpools --all --wait=true --timeout=90s
k -n "$NAMESPACE" wait --for=delete pod -l continuum.blinklabs.io/workload --timeout=120s
log "uninstalling release $RELEASE"
helm_ -n "$NAMESPACE" uninstall "$RELEASE" --ignore-not-found --wait --timeout 3m
log "deleting namespace $NAMESPACE"
k delete namespace "$NAMESPACE" --ignore-not-found --wait=true --timeout=180s
log "done"
