#!/usr/bin/env bash
# Records hashes.golden with cardano-cli, independently of this package.
set -euo pipefail
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
go test ./internal/genesis -run TestHashesGolden -dump "$out" >/dev/null || true
docker run --rm --name continuum-genesis-hash -v "$out/config:/c:ro" --entrypoint bash \
	ghcr.io/blinklabs-io/cardano-node:11.1.3-1 -c '
	echo "byron $(cardano-cli byron genesis print-genesis-hash --genesis-json /c/byron-genesis.json)"
	for e in shelley alonzo conway; do echo "$e $(cardano-cli hash genesis-file --genesis /c/$e-genesis.json)"; done'
