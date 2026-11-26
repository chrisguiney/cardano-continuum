#!/usr/bin/env bash
# Sample hack/podstats.sh every INTERVAL seconds, COUNT times, appending to
# OUT (a TSV: time, pod, mem_mib, cpu_m). Bounded by COUNT; reads no stdin.
set -euo pipefail
exec </dev/null
INTERVAL=${INTERVAL:-30}
COUNT=${COUNT:-60}
OUT=${OUT:?set OUT to the TSV to append to}
here=$(cd "$(dirname "$0")" && pwd)
for ((i = 0; i < COUNT; i++)); do
  "$here/podstats.sh" >>"$OUT" || true
  sleep "$INTERVAL"
done
