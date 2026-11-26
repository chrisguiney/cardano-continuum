#!/usr/bin/env bash
# Remove everything kind-up.sh made for one cluster name: the cluster and its
# registry container. Environment: KIND, CONTINUUM_KIND_NAME (default
# continuum-e2e), matching kind-up.sh.
set -euo pipefail

KIND=${KIND:-kind}
CLUSTER=${CONTINUUM_KIND_NAME:-continuum-e2e}
$KIND delete cluster --name "$CLUSTER"
docker rm -f "${CLUSTER}-registry" >/dev/null 2>&1 || true
