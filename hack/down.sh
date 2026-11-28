#!/usr/bin/env bash
# Remove what hack/up.sh installed: the Helm release RELEASE and the
# namespace. The ContinuumNetwork and pools go first, and down.sh waits until
# every pool pod is gone while the operator still runs: it holds a fence
# finalizer on every keyed pod, and a namespace deleted after the operator is
# gone hangs on them. A pod that outlasts the wait, or a namespace that does
# not finish deleting, fails the script; finalizers are never stripped. The CRDs stay, as Helm leaves them. Every wait is
# bounded. CONTEXT empty means kubectl's current context, read once.
#
# Environment: CONTEXT, NAMESPACE, RELEASE, HELM, K_BIN, CONTINUUM_STATE_DIR,
# GONE_TRIES and GONE_GAP (the pool-removal wait, 60 tries two seconds apart).
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
  rm -f "$state/forward.pid" "$state/forward.target"
fi

if ! k get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log "namespace $NAMESPACE is gone"
  exit 0
fi

fail() { log "ERROR: $*" >&2; exit 1; }

# remaining lists what a pool teardown has not removed: pool pods and pool
# resources.
remaining() {
  k -n "$NAMESPACE" get pods -l continuum.blinklabs.io/workload -o name 2>/dev/null
  k -n "$NAMESPACE" get continuumpools -o name 2>/dev/null
}

# The ContinuumNetwork goes first: while it exists the operator recreates
# any pool deleted under it, and a pod recreated mid-teardown carries the
# fence finalizer that only the operator removes.
log "deleting pools in $CONTEXT/$NAMESPACE"
k -n "$NAMESPACE" delete continuumnetworks --all --wait=true --timeout=90s || fail "could not delete the ContinuumNetwork"
k -n "$NAMESPACE" delete continuumpools --all --wait=true --timeout=90s || fail "could not delete the ContinuumPools"
left=""
for ((i = 0; i < ${GONE_TRIES:-60}; i++)); do
  left=$(remaining)
  [ -z "$left" ] && break
  sleep "${GONE_GAP:-2}"
done
if [ -n "$left" ]; then
  fail "still present with the operator running: $(echo $left | tr '\n' ' ') -- a pod holding the continuum.blinklabs.io/fence finalizer needs the operator to release it; check kubectl -n $NAMESPACE describe on each, and do not uninstall the release until they are gone"
fi
log "uninstalling release $RELEASE"
helm_ -n "$NAMESPACE" uninstall "$RELEASE" --ignore-not-found --wait --timeout 3m || fail "helm uninstall failed"
log "deleting namespace $NAMESPACE"
k delete namespace "$NAMESPACE" --ignore-not-found --wait=true --timeout=180s ||
  fail "namespace $NAMESPACE did not finish deleting: $(k get namespace "$NAMESPACE" -o jsonpath='{.status.conditions[*].message}' 2>/dev/null)"
log "done"
