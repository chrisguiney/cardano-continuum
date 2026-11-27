#!/usr/bin/env bash
# changed-models.sh BASE: print, one per line, the model directories
# (specs/<dir>) whose verdicts a change since BASE can affect. Run it from
# models/; make models-changed passes each line to run-model.sh.
#
# Each spec is self-contained: one directory, extending only TLA+'s standard
# modules, so a change inside specs/<dir> can only change that directory's
# verdicts. A change to the runner (run-model.sh), the tool installer and its
# jar pins (install-tla.sh) or the tool wrappers (bin/) can change every
# verdict, so it selects them all. A change elsewhere, including this script
# and the READMEs, selects nothing.
#
# The comparison is BASE against the working tree, so committed, staged,
# unstaged and untracked changes all count. A deleted spec has nothing left
# to check and is not printed.
set -euo pipefail
exec </dev/null

base=${1:?usage: changed-models.sh BASE}

changed=$(
  git diff --name-only --relative "$base" -- .
  git ls-files --others --exclude-standard -- .
)

if grep -qE '^(scripts/run-model\.sh|scripts/install-tla\.sh|bin/)' <<<"$changed"; then
  for d in specs/*/; do
    echo "${d%/}"
  done
  exit 0
fi

# grep exits 1 when no spec changed; that is an empty selection, not an error.
{ grep -oE '^specs/[^/]+/' <<<"$changed" || true; } | sort -u | while read -r d; do
  if [[ -d "$d" ]]; then
    echo "${d%/}"
  fi
done
