#!/usr/bin/env bash
# Start a kind cluster and its plain-HTTP registry. Idempotent.
#
# The registry is published on 127.0.0.1:$PORT so the host (ko) pushes to
# localhost:$PORT, and joins the kind network as <cluster>-registry:5000.
# Each node maps localhost:$PORT to that name through certs.d/hosts.toml, so
# the same image reference works from the host and inside the cluster.
#
# The e2e suite and the long-lived developer cluster use different names and
# ports, so a test run's teardown never removes the developer's simulation.
#
# Environment: KIND (command, default "kind"), CONTINUUM_E2E_PORT (default
# 5001), CONTINUUM_KIND_NAME (default continuum-e2e), CONTINUUM_KIND_RESTART
# (registry restart policy, default "no"; the dev cluster uses unless-stopped
# so it survives a Docker restart).
set -euo pipefail

KIND=${KIND:-kind}
PORT=${CONTINUUM_E2E_PORT:-5001}
CLUSTER=${CONTINUUM_KIND_NAME:-continuum-e2e}
REGISTRY=${CLUSTER}-registry
RESTART=${CONTINUUM_KIND_RESTART:-no}
REGISTRY_IMAGE=registry:2.8.3
here=$(cd "$(dirname "$0")" && pwd)

if [ "$(docker inspect -f '{{.State.Running}}' "$REGISTRY" 2>/dev/null || true)" != true ]; then
  docker rm -f "$REGISTRY" >/dev/null 2>&1 || true
  docker run -d --restart="$RESTART" --name "$REGISTRY" -p "127.0.0.1:${PORT}:5000" "$REGISTRY_IMAGE" >/dev/null
fi

if ! $KIND get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  $KIND create cluster --name "$CLUSTER" --config "$here/kind.yaml" --wait 120s
fi

if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "$REGISTRY")" = null ]; then
  docker network connect kind "$REGISTRY"
fi

for node in $($KIND get nodes --name "$CLUSTER"); do
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/localhost:${PORT}"
  docker exec -i "$node" tee "/etc/containerd/certs.d/localhost:${PORT}/hosts.toml" >/dev/null <<TOML
server = "http://${REGISTRY}:5000"

[host."http://${REGISTRY}:5000"]
  capabilities = ["pull", "resolve"]
TOML
  # The operator and the nodes name the registry by its in-cluster address.
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/${REGISTRY}:5000"
  docker exec -i "$node" tee "/etc/containerd/certs.d/${REGISTRY}:5000/hosts.toml" >/dev/null <<TOML
server = "http://${REGISTRY}:5000"

[host."http://${REGISTRY}:5000"]
  capabilities = ["pull", "resolve"]
TOML
done
echo "cluster $CLUSTER up; registry localhost:${PORT}"
