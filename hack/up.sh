#!/usr/bin/env bash
# Deploy the whole system onto a developer's Kubernetes: operator, genesis,
# a ContinuumNetwork with POOLS cardano-node pools and DINGO_POOLS dingo
# pools, and the engine serving the live UI. Idempotent: every step skips
# work already done, so a second run only repairs what is missing. To change
# the pool count or the seed, run `make down` first: the genesis fixes both.
#
# Every wait is bounded and nothing reads stdin. kubectl always names
# $CONTEXT, so the current context is never changed.
#
# Environment (the Makefile passes these): CONTEXT, NAMESPACE, PROFILES,
# POOLS, DINGO_POOLS, WALLETS, SCALE, SEED (64 hex), CONTINUUM_REGISTRY,
# CONTINUUM_REGISTRY_INSECURE, KO, MIN_MEM_GIB, UI_PORT, UI_ADDRESS.
set -euo pipefail
exec </dev/null

CONTEXT=${CONTEXT:-docker-desktop}
NAMESPACE=${NAMESPACE:-continuum}
PROFILES=${PROFILES:-preprod}
POOLS=${POOLS:-3}
DINGO_POOLS=${DINGO_POOLS:-1}
WALLETS=${WALLETS:-100}
# SCALE multiplies every profile's activity rates (the engine's --scale).
SCALE=${SCALE:-200}
SEED=${SEED:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}
REGISTRY=${CONTINUUM_REGISTRY:-localhost:5005/continuum}
INSECURE=${CONTINUUM_REGISTRY_INSECURE:-true}
KO=${KO:-go run github.com/google/ko@v0.19.1}
MIN_MEM_GIB=${MIN_MEM_GIB:-48}
UI_PORT=${UI_PORT:-8088}
UI_ADDRESS=${UI_ADDRESS:-127.0.0.1}
# ENGINE_POOLS is the number of pool operators the engine simulates; the
# preprod and preview profiles have no pool profile, so it must stay 0 there.
ENGINE_POOLS=${ENGINE_POOLS:-0}
K=${K_BIN:-kubectl}
NODE_CONTAINER=${NODE_CONTAINER:-desktop-control-plane}
root=$(cd "$(dirname "$0")/.." && pwd)
state=${CONTINUUM_STATE_DIR:-${HOME}/.cache/continuum/$NAMESPACE}
total=$((POOLS + DINGO_POOLS))
# k and the Go helper; TOTAL pools are genesis pools 0..TOTAL-1.
k() { "$K" --context "$CONTEXT" "$@"; }
devup() { (cd "$root" && go run ./hack/devup "$@"); }
log() { printf '%s up: %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# bounded SECONDS CMD...: run CMD, killing it when the time is up (macOS has no timeout).
bounded() {
  local secs=$1
  shift
  perl -e 'alarm shift; exec @ARGV or die "exec: $!"' "$secs" "$@"
}

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
  k config view >/dev/null 2>&1 || die "kubectl cannot read its configuration"
  "$K" config get-contexts -o name | grep -qx "$CONTEXT" || die "no kubectl context named $CONTEXT"
  k version >/dev/null 2>&1 || die "context $CONTEXT does not answer"
  local node
  node=$(k get nodes -o jsonpath='{.items[0].status.allocatable.memory}')
  devup mem "$node" "$MIN_MEM_GIB" || die "cannot read the node's memory"
  local host=${REGISTRY%%/*}
  curl -fsS -m 5 "http://$host/v2/" >/dev/null || die "registry $host is not reachable"
  [ "$total" -ge 1 ] || die "need at least one pool"
  devup render -pools "$POOLS" -dingo "$DINGO_POOLS" >/dev/null || die "pool plan refused"
  [[ $SCALE =~ ^[0-9]+([.][0-9]+)?$ && $SCALE =~ [1-9] ]] || die "SCALE must be a positive number"
  [[ $SEED =~ ^[0-9a-f]{64}$ ]] || die "SEED must be 64 lowercase hex characters"
}

# load_node SRC NAME: make the node's containerd hold the dev-registry image
# SRC (a digest reference) as NAME. Docker Desktop's node routes every pull
# through a mirror that cannot reach a registry on the host, so the image is
# pulled by the host's Docker and imported by `ctr`, as `kind load` does.
# Pods then name the image by NAME, a tag unique to the digest, with the
# default IfNotPresent policy: containerd would not match a digest reference
# to an import.
load_node() {
  local src=$1 name=$2 tmp
  if docker exec "$NODE_CONTAINER" ctr -n k8s.io images ls -q 2>/dev/null | grep -qxF "$name"; then return; fi
  tmp=$(mktemp)
  docker pull -q "$src" >/dev/null && docker tag "$src" "$name" && docker save "$name" -o "$tmp" &&
    docker exec -i "$NODE_CONTAINER" ctr -n k8s.io images import - <"$tmp" >/dev/null || { rm -f "$tmp"; die "cannot load $src into $NODE_CONTAINER"; }
  rm -f "$tmp"
  docker exec "$NODE_CONTAINER" ctr -n k8s.io images ls -q | grep -qxF "$name" || die "$name is not in $NODE_CONTAINER after import"
}

# build NAME: ko-build ./cmd/NAME for the node's architecture, load it into
# the node and print the image name pods use.
build() {
  local arch flags=() ref name ref_hex
  arch=$(k get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
  [ "$INSECURE" = true ] && flags+=(--insecure-registry)
  ref=$(cd "$root" && KO_DOCKER_REPO=$REGISTRY bounded 300 $KO build --base-import-paths \
    --platform="linux/$arch" "${flags[@]}" "./cmd/$1" 2>/dev/null | tail -1)
  case $ref in *@sha256:*) ;; *) die "ko build $1 printed no digest reference" ;; esac
  name=${ref%@sha256:*}
  ref_hex=${ref##*@sha256:}
  name=$name:${ref_hex:0:12}
  load_node "$ref" "$name"
  echo "$name"
}

# prepull IMAGE...: pull images onto the node before the genesis fixes
# systemStart, since a node that starts more than the stability window after
# slot 0 has no ledger view and never forges.
prepull() {
  local i=0 img names=()
  for img in "$@"; do
    k -n "$NAMESPACE" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: prepull-$i}
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - {name: main, image: $img, command: [sleep, "3600"]}
EOF
    names+=("pod/prepull-$i")
    i=$((i + 1))
  done
  k -n "$NAMESPACE" wait --for=condition=Ready --timeout=8m "${names[@]}" >/dev/null || die "image pre-pull failed"
  k -n "$NAMESPACE" delete --wait=false "${names[@]}" >/dev/null
}

install_operator() {
  k apply -f "$root/config/crd/bases" >/dev/null
  k apply -f "$root/config/rbac/role.yaml" >/dev/null
  local crd
  for crd in continuumnetworks continuumpools; do
    k wait --for=condition=Established --timeout=60s "crd/$crd.continuum.blinklabs.io" >/dev/null
  done
  local img
  img=$(build continuum-operator)
  sed -e "s|image: continuum-operator|image: $img|" -e "s|namespace: NAMESPACE|namespace: $NAMESPACE|" \
    "$root/deploy/operator/operator.yaml" | k -n "$NAMESPACE" apply -f - >/dev/null
  k -n "$NAMESPACE" wait --for=condition=Available --timeout=3m deployment/continuum-operator >/dev/null
}

run_genesis() {
  k -n "$NAMESPACE" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata: {name: continuum-seed}
stringData: {seed: $SEED}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: continuum-genesis}
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 100Mi}}
EOF
  if k -n "$NAMESPACE" get job continuum-genesis >/dev/null 2>&1; then
    log "genesis job exists; keeping it"
    return
  fi
  local img
  img=$(build continuum-genesis)
  # -start-delay leaves time for the nodes to start; -pools is the pool count.
  awk -v img="$img" -v delay="${START_DELAY:-120s}" -v pools="$total" '
    /^          image: continuum-genesis$/ { print "          image: " img; next }
    /^            - -start-delay=60s$/ { print "            - -start-delay=" delay; next }
    { print }
    /^            - -out=\/genesis$/ { print "            - -pools=" pools }' \
    "$root/deploy/genesis/job.yaml" | k -n "$NAMESPACE" apply -f - >/dev/null
  k -n "$NAMESPACE" wait --for=condition=Complete --timeout=4m job/continuum-genesis >/dev/null || die "genesis job did not complete"
}

# publish_genesis copies the rendered config into the ConfigMap the network's
# genesisRef names and each pool's keys into a Secret, as the operator mounts them.
publish_genesis() {
  if k -n "$NAMESPACE" get configmap devnet-genesis >/dev/null 2>&1 &&
    k -n "$NAMESPACE" get secret "keys-$((total - 1))" >/dev/null 2>&1; then
    log "genesis already published"
    return
  fi
  k -n "$NAMESPACE" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: reader}
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: main
      image: busybox:1.37.0
      command: [sleep, "3600"]
      volumeMounts: [{name: genesis, mountPath: /genesis, readOnly: true}]
  volumes: [{name: genesis, persistentVolumeClaim: {claimName: continuum-genesis}}]
EOF
  k -n "$NAMESPACE" wait --for=condition=Ready --timeout=3m pod/reader >/dev/null
  local tmp f i
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  mkdir "$tmp/config"
  for f in config.json byron-genesis.json shelley-genesis.json alonzo-genesis.json conway-genesis.json; do
    k -n "$NAMESPACE" exec reader -- cat "/genesis/config/$f" >"$tmp/config/$f"
  done
  k -n "$NAMESPACE" create configmap devnet-genesis --from-file="$tmp/config" --dry-run=client -o yaml |
    k -n "$NAMESPACE" apply -f - >/dev/null
  for ((i = 0; i < total; i++)); do
    mkdir "$tmp/k$i"
    for f in kes.skey vrf.skey opcert.cert; do
      k -n "$NAMESPACE" exec reader -- cat "/genesis/pools/$i/$f" >"$tmp/k$i/$f"
    done
    k -n "$NAMESPACE" create secret generic "keys-$i" --from-file="$tmp/k$i" --dry-run=client -o yaml |
      k -n "$NAMESPACE" apply -f - >/dev/null
  done
  k -n "$NAMESPACE" delete pod reader --wait=false >/dev/null
}

system_start() {
  k -n "$NAMESPACE" get configmap devnet-genesis -o jsonpath='{.data.shelley-genesis\.json}' | jq -r .systemStart
}

pool_pods() {
  k -n "$NAMESPACE" get pods -l continuum.blinklabs.io/workload -o name 2>/dev/null
}

apply_network() {
  devup render -pools "$POOLS" -dingo "$DINGO_POOLS" | k -n "$NAMESPACE" apply -f - >/dev/null
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

# require_one_chain waits, bounded by the stability window plus two minutes
# after systemStart (and at least ten minutes from now), until the pools
# have forged 2k blocks between them, then requires every tip to be within k
# of the others. A pool that never dials its peers forges a fork of its own.
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

deploy_engine() {
  k -n "$NAMESPACE" apply -f "$root/deploy/engine/rbac.yaml" >/dev/null
  k -n "$NAMESPACE" create configmap continuum-engine --from-literal=node="$(devup node "$NAMESPACE")" \
    --dry-run=client -o yaml | k -n "$NAMESPACE" apply -f - >/dev/null
  local img args
  img=$(build continuum)
  args=$(printf '            - %s\n' "--profiles=$PROFILES" "--wallets=$WALLETS" "--scale=$SCALE" "--pools=$ENGINE_POOLS" \
    "--genesis-pools=$total" "--network=devnet" "--namespace=$NAMESPACE" "--serve=127.0.0.1:$UI_PORT")
  ARGS="$args" awk -v img="$img" '
    /^          image: continuum$/ { print "          image: " img; next }
    /^            - --wallets=20$/ { print ENVIRON["ARGS"]; next }
    /^      automountServiceAccountToken: false$/ { print "      serviceAccountName: continuum-engine"; print "      automountServiceAccountToken: true"; next }
    { print }' "$root/deploy/engine/statefulset.yaml" | k -n "$NAMESPACE" apply -f - >/dev/null
  retry 60 5 "the engine pod to be Ready" k -n "$NAMESPACE" wait --for=condition=Ready --timeout=4s pod/continuum-engine-0
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
  k create namespace "$NAMESPACE" --dry-run=client -o yaml | k apply -f - >/dev/null
  install_operator
  if ! k -n "$NAMESPACE" get job continuum-genesis >/dev/null 2>&1; then
    local node_img dingo_img imgs
    { read -r node_img; read -r dingo_img; } < <(devup images)
    imgs=("$node_img")
    [ "$DINGO_POOLS" -gt 0 ] && imgs+=("$dingo_img")
    prepull "${imgs[@]}"
  fi
  run_genesis
  publish_genesis
  apply_network
  require_one_chain
  deploy_engine
  forward
  log "UI: http://127.0.0.1:$UI_PORT (port-forward to $NAMESPACE/continuum-engine-0, listening on $UI_ADDRESS)"
  log "from another host: ssh -L $UI_PORT:127.0.0.1:$UI_PORT <this host>, or rerun with UI_ADDRESS=0.0.0.0 (the API is unauthenticated)"
}

main "$@"
