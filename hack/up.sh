#!/usr/bin/env bash
# Install or upgrade the continuum Helm chart (charts/continuum) into
# NAMESPACE of the cluster CONTEXT names: operator, genesis, a
# ContinuumNetwork with POOLS cardano-node pools and DINGO_POOLS dingo pools,
# and the engine serving the live UI. Then wait, bounded, for the pools to
# share one chain and the engine to be Ready, and keep a port-forward to the
# UI running. Any Kubernetes distribution works; nothing here starts or
# configures a cluster.
#
# CONTEXT empty means kubectl's current context. It is read once and passed
# to every kubectl and helm call, so the current context is never changed
# and a switch while this runs does not redirect it.
#
# IMAGES is the file `make images` wrote: one digest reference per line,
# CONTINUUM_REGISTRY/<name>@sha256:..., for continuum, continuum-operator
# and continuum-genesis. The chart gets the registry and the digests.
#
# ATTACKS selects the engine's tier-1 adversaries: empty (the default) is
# every attack a stock genesis supports, "none" runs none, and a
# comma-separated list runs those. exunits-block-gap is opt-in: naming it
# also renders the genesis with per-transaction ExUnits above the per-block
# ones (devup attacks prints the overrides).
#
# An engine resumes its event log only under the binary, seed, profiles and
# attacks that wrote it, and the genesis fixes the pool counts and the
# genesis overrides. This script computes the run identity (devup run) and
# passes it as engine.runIdentity, which the chart records as the engine
# StatefulSet's continuum.blinklabs.io/run annotation. When the deployed
# annotation or the deployed release's pool values differ, it logs why
# ("engine binary changed: a run can't be resumed by a different binary;
# resetting the network") and runs hack/down.sh first, so the chain and the
# engine store start fresh. With KEEP_RUN=1 it refuses instead.
#
# Every wait is bounded and nothing reads stdin. Written for bash 3.2, so an
# empty array is expanded as ${a[@]+"${a[@]}"} under set -u.
#
# Environment (the Makefile passes these): CONTEXT, NAMESPACE, RELEASE,
# PROFILES, POOLS, DINGO_POOLS, WALLETS, SCALE, SEED (64 hex), ATTACKS,
# ENGINE_POOLS, KEEP_RUN, CONTINUUM_REGISTRY, IMAGES, HELM, VALUES (an extra
# values file), MIN_MEM_GIB, UI_PORT, UI_ADDRESS.
set -euo pipefail
exec </dev/null

CONTEXT=${CONTEXT:-}
NAMESPACE=${NAMESPACE:-continuum}
RELEASE=${RELEASE:-continuum}
PROFILES=${PROFILES:-preprod}
POOLS=${POOLS:-3}
DINGO_POOLS=${DINGO_POOLS:-1}
WALLETS=${WALLETS:-100}
# SCALE multiplies every profile's activity rates (the engine's --scale).
SCALE=${SCALE:-200}
SEED=${SEED:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}
ATTACKS=${ATTACKS:-}
KEEP_RUN=${KEEP_RUN:-}
REGISTRY=${CONTINUUM_REGISTRY:?set CONTINUUM_REGISTRY to the registry make images pushed to}
IMAGES=${IMAGES:?set IMAGES to the file make images wrote}
HELM=${HELM:-helm}
VALUES=${VALUES:-}
MIN_MEM_GIB=${MIN_MEM_GIB:-48}
UI_PORT=${UI_PORT:-8088}
UI_ADDRESS=${UI_ADDRESS:-127.0.0.1}
# ENGINE_POOLS is the number of pool operators the engine simulates; the
# preprod and preview profiles have no pool profile, so it must stay 0 there.
ENGINE_POOLS=${ENGINE_POOLS:-0}
K=${K_BIN:-kubectl}
root=$(cd "$(dirname "$0")/.." && pwd)
chart=$root/charts/continuum
state=${CONTINUUM_STATE_DIR:-${HOME}/.cache/continuum/$NAMESPACE}
total=$((POOLS + DINGO_POOLS))
k() { "$K" --context "$CONTEXT" "$@"; }
# HELM may be a command with arguments (go run ...), so it is split on purpose.
# shellcheck disable=SC2086
helm_() { $HELM --kube-context "$CONTEXT" "$@"; }
devup() { (cd "$root" && go run ./hack/devup "$@"); }
log() { printf '%s up: %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# retry N SECONDS DESCRIPTION CMD...: run CMD up to N times, SECONDS apart.
retry() {
  local n=$1 gap=$2 what=$3
  shift 3
  local i
  for ((i = 0; i < n; i++)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep "$gap"
  done
  die "timed out waiting for $what"
}

preflight() {
  if [ -z "$CONTEXT" ]; then
    CONTEXT=$("$K" config current-context 2>/dev/null) || die "kubectl has no current context: set CONTEXT"
  fi
  "$K" config get-contexts -o name | grep -qxF "$CONTEXT" || die "no kubectl context named $CONTEXT"
  k version >/dev/null 2>&1 || die "context $CONTEXT does not answer"
  log "context $CONTEXT, namespace $NAMESPACE, release $RELEASE"
  local node
  node=$(k get nodes -o jsonpath='{.items[0].status.allocatable.memory}')
  devup mem "$node" "$MIN_MEM_GIB" || die "cannot read the node's memory"
  [ "$POOLS" -ge 1 ] || die "POOLS must be at least 1: the engine follows pool 0, a cardano-node pool"
  [[ $SCALE =~ ^[0-9]+([.][0-9]+)?$ && $SCALE =~ [1-9] ]] || die "SCALE must be a positive number"
  [[ $SEED =~ ^[0-9a-f]{64}$ ]] || die "SEED must be 64 lowercase hex characters"
  [ -s "$IMAGES" ] || die "$IMAGES is empty: run make images"
  resolve_attacks
}

# resolve_attacks sets ENGINE_ATTACKS, the engine's --attack value, and
# OVERRIDES and ALLOW_INCONSISTENT, the genesis values those attacks need.
resolve_attacks() {
  local plan arg
  plan=$(devup attacks "$ATTACKS") || die "ATTACKS=$ATTACKS refused"
  ENGINE_ATTACKS=$(printf '%s\n' "$plan" | sed -n 1p)
  OVERRIDES=()
  ALLOW_INCONSISTENT=false
  while IFS= read -r arg; do
    case $arg in
      -override=*) OVERRIDES+=("${arg#-override=}") ;;
      -allow-inconsistent-exunits) ALLOW_INCONSISTENT=true ;;
      "") ;;
      *) die "devup attacks printed an unknown genesis argument $arg" ;;
    esac
  done < <(printf '%s\n' "$plan" | sed -n '2,$p')
  log "attacks: ${ENGINE_ATTACKS:-none}${OVERRIDES[*]+ (genesis overrides: ${OVERRIDES[*]})}"
}

# digest NAME prints the digest of CONTINUUM_REGISTRY/NAME in IMAGES.
digest() {
  local ref
  ref=$(grep -F "$REGISTRY/$1@sha256:" "$IMAGES" | tail -1)
  [ -n "$ref" ] || die "$IMAGES has no $REGISTRY/$1 image: run make images"
  echo "${ref##*@}"
}

# json_list ITEM... prints the items as a JSON array of strings.
json_list() {
  if [ $# -eq 0 ]; then
    echo '[]'
    return
  fi
  printf '%s\n' "$@" | jq -R . | jq -cs .
}

# check_run compares the run this install makes with the deployed one. A
# deploy that cannot resume is reset with hack/down.sh, or refused with
# KEEP_RUN=1.
check_run() {
  ENGINE_IMG=$REGISTRY/continuum@$(digest continuum)
  local out old reason deployed want
  old=$(k -n "$NAMESPACE" get statefulset continuum-engine -o jsonpath='{.metadata.annotations.continuum\.blinklabs\.io/run}' 2>/dev/null) || old=""
  out=$(SEED=$SEED devup run -deployed "$old" -image "$ENGINE_IMG" -profiles "$PROFILES" -attacks "$ENGINE_ATTACKS")
  RUN_ID=$(printf '%s\n' "$out" | sed -n 1p)
  reason=$(printf '%s\n' "$out" | sed -n 2p)
  helm_ -n "$NAMESPACE" status "$RELEASE" >/dev/null 2>&1 || return 0
  deployed=$(helm_ -n "$NAMESPACE" get values "$RELEASE" -o json | jq -c '[.pools, (.genesis.overrides // [])]')
  want=$(jq -cn --argjson n "$POOLS" --argjson d "$DINGO_POOLS" --argjson o "$(json_list ${OVERRIDES[@]+"${OVERRIDES[@]}"})" \
    '[{cardanoNode: $n, dingo: $d}, $o]')
  if [ -z "$reason" ] && [ "$deployed" != "$want" ]; then
    reason="pools or genesis overrides changed: the genesis fixes them"
  fi
  [ -z "$reason" ] && return 0
  [ "$KEEP_RUN" = 1 ] && die "$reason; KEEP_RUN=1, so not resetting (make down, or unset KEEP_RUN)"
  log "$reason; resetting the network"
  CONTEXT=$CONTEXT NAMESPACE=$NAMESPACE RELEASE=$RELEASE HELM=$HELM K_BIN=$K CONTINUUM_STATE_DIR=$state "$root/hack/down.sh" >&2 ||
    die "reset failed; run make down"
}

# install applies the CRDs, which Helm installs but never upgrades, then
# installs or upgrades the release. It does not pass --wait: the pools and
# the engine become ready only after the genesis Job publishes.
install() {
  k apply -f "$chart/crds" >/dev/null
  local attacks=()
  [ -n "$ENGINE_ATTACKS" ] && IFS=, read -r -a attacks <<<"$ENGINE_ATTACKS"
  local args=(
    --set-string "images.registry=$REGISTRY"
    --set-string "images.operator.digest=$(digest continuum-operator)"
    --set-string "images.genesis.digest=$(digest continuum-genesis)"
    --set-string "images.engine.digest=$(digest continuum)"
    --set-string "seed.value=$SEED"
    --set-json "pools={\"cardanoNode\":$POOLS,\"dingo\":$DINGO_POOLS}"
    --set-json "genesis.overrides=$(json_list ${OVERRIDES[@]+"${OVERRIDES[@]}"})"
    --set-json "genesis.allowInconsistentExUnits=$ALLOW_INCONSISTENT"
    --set-string "engine.profiles=$PROFILES"
    --set-json "engine.wallets=$WALLETS"
    --set-json "engine.scale=$SCALE"
    --set-json "engine.pools=$ENGINE_POOLS"
    --set-json "engine.ui.port=$UI_PORT"
    --set-json "engine.attacks=$(json_list ${attacks[@]+"${attacks[@]}"})"
    --set-string "engine.runIdentity=$RUN_ID"
  )
  [ -n "$VALUES" ] && args+=(-f "$VALUES")
  helm_ upgrade --install "$RELEASE" "$chart" -n "$NAMESPACE" --create-namespace --timeout 5m "${args[@]}" >/dev/null ||
    die "helm upgrade --install failed"
}

pool_pods() {
  k -n "$NAMESPACE" get pods -l continuum.blinklabs.io/workload -o name 2>/dev/null
}

# wait_pools waits for the genesis Job and then for every pool pod.
wait_pools() {
  k -n "$NAMESPACE" wait --for=condition=Complete --timeout=10m job -l app=continuum-genesis >/dev/null ||
    die "the genesis Job did not complete (kubectl -n $NAMESPACE logs -l app=continuum-genesis)"
  local i
  for ((i = 0; i < 60; i++)); do
    [ "$(pool_pods | wc -l | tr -d ' ')" -ge "$total" ] && break
    sleep 5
  done
  [ "$(pool_pods | wc -l | tr -d ' ')" -ge "$total" ] || die "operator created only $(pool_pods | wc -l) of $total pools (check kubectl -n $NAMESPACE get continuumpools)"
  local pod
  for pod in $(pool_pods); do
    k -n "$NAMESPACE" wait --for=condition=Ready --timeout=6m "$pod" >/dev/null || die "$pod is not Ready"
  done
}

# cardano_pods lists the cardano-node pods, i.e. the pods whose image is not dingo's.
cardano_pods() {
  k -n "$NAMESPACE" get pods -l continuum.blinklabs.io/workload \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.containers[0].image}{"\n"}{end}' |
    awk '$2 !~ /dingo/ {print $1}'
}

# require_one_chain waits, bounded by fifteen minutes, until the pools have
# forged 2k blocks between them, then requires every tip to be within k of
# the others. A pool that never dials its peers forges a fork of its own.
require_one_chain() {
  local k_param=5 magic=42 want=10 i forged lo hi n tips pod out
  local deadline=$(($(date +%s) + 900))
  log "waiting for $want forged blocks"
  for ((i = 0; i < 90; i++)); do
    forged=0
    for pod in $(cardano_pods); do
      n=$(k -n "$NAMESPACE" logs "$pod" -c node 2>/dev/null | grep -c ForgedBlock || true)
      forged=$((forged + n))
    done
    [ "$forged" -ge "$want" ] && break
    [ "$(date +%s)" -gt "$deadline" ] && break
    sleep 10
  done
  [ "$forged" -ge "$want" ] || die "pools forged only $forged of $want blocks"
  tips=()
  for pod in $(cardano_pods); do
    out=$(k -n "$NAMESPACE" exec "$pod" -- cardano-cli query tip --testnet-magic "$magic" --socket-path /data/node.socket 2>/dev/null | jq -r .block) || continue
    tips+=("$out")
  done
  [ "${#tips[@]}" -eq "$POOLS" ] || die "${#tips[@]} of $POOLS cardano-node pools report a tip"
  lo=${tips[0]} hi=${tips[0]}
  for n in "${tips[@]}"; do
    ((n < lo)) && lo=$n
    ((n > hi)) && hi=$n
  done
  [ $((hi - lo)) -le "$k_param" ] || die "tips differ by $((hi - lo)) blocks, more than k=$k_param: the pools forked apart"
  log "one chain: tips $lo..$hi, $forged blocks forged"
}

# forward keeps a port-forward to the engine pod's loopback UI running after
# this script exits, restarting it when kubectl drops. The API has no
# authentication, so it listens on UI_ADDRESS, 127.0.0.1 unless asked.
forward() {
  mkdir -p "$state"
  if [ -f "$state/forward.pid" ] && kill -0 "$(cat "$state/forward.pid")" 2>/dev/null; then
    return
  fi
  nohup bash -c "for _ in \$(seq 1 100000); do '$K' --context '$CONTEXT' -n '$NAMESPACE' port-forward --address '$UI_ADDRESS' pod/continuum-engine-0 '$UI_PORT:$UI_PORT'; sleep 3; done" \
    >"$state/forward.log" 2>&1 </dev/null &
  echo $! >"$state/forward.pid"
  retry 20 1 "the UI port-forward" curl -fsS -m 2 "http://127.0.0.1:$UI_PORT/healthz"
}

main() {
  preflight
  check_run
  install
  wait_pools
  require_one_chain
  retry 60 5 "the engine pod to be Ready" k -n "$NAMESPACE" wait --for=condition=Ready --timeout=4s pod/continuum-engine-0
  forward
  log "UI: http://127.0.0.1:$UI_PORT (port-forward to $NAMESPACE/continuum-engine-0, listening on $UI_ADDRESS)"
  log "from another host: ssh -L $UI_PORT:127.0.0.1:$UI_PORT <this host>, or rerun with UI_ADDRESS=0.0.0.0 (the API is unauthenticated)"
}

main "$@"
