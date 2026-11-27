#!/usr/bin/env bash
# drift.sh [REV]: for each model, list the code it maps that has changed
# between the commit it models and REV (default HEAD). Run it from models/.
#
# A model's README pins that commit on a line
#   Models cardano-continuum at `<sha>`.
# and names the code it models as internal/... or cmd/... paths (files with
# :line, or package directories). A listed file is a model to re-read against
# the code, then re-pin. A model with no pin is reported as such. Exits 1 when
# any model drifted or is unpinned.
set -euo pipefail
exec </dev/null

rev=${1:-HEAD}
top=$(git rev-parse --show-toplevel)
status=0
for d in specs/*/; do
  d=${d%/}
  [[ -f "$d/README.md" ]] || continue
  pin=$(sed -n -E 's/.*Models cardano-continuum at `([0-9a-f]{7,40})`.*/\1/p' "$d/README.md" | head -1)
  paths=$(cat "$d"/README.md "$d"/*.tla 2>/dev/null |
    grep -oE '(internal|cmd)/[A-Za-z0-9_./-]*[A-Za-z0-9_]' | sort -u || true)
  if [[ -z "$paths" ]]; then
    continue
  fi
  if [[ -z "$pin" ]]; then
    echo "$d: no 'Models cardano-continuum at \`<sha>\`' line in README.md"
    status=1
    continue
  fi
  # shellcheck disable=SC2086 # one pathspec per word
  moved=$(git -C "$top" diff --name-only "$pin" "$rev" -- $paths)
  if [[ -n "$moved" ]]; then
    echo "$d: code changed since $pin:"
    sed 's/^/  /' <<<"$moved"
    status=1
  fi
done
exit "$status"
