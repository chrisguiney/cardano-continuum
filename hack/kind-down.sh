#!/usr/bin/env bash
# Remove everything kind-up.sh made: the cluster and the registry container.
set -euo pipefail

KIND=${KIND:-kind}
$KIND delete cluster --name continuum-e2e
docker rm -f continuum-e2e-registry >/dev/null 2>&1 || true
