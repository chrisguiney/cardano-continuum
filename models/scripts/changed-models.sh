#!/usr/bin/env bash
# changed-models.sh BASE: print, one per line, the model directories
# (specs/<dir>) whose verdicts a change since BASE can affect. Run it from
# models/; make models-changed passes each line to run-model.sh.
#
# Each spec is self-contained: one directory, extending only TLA+'s standard
# modules, so a change inside specs/<dir> can only change that directory's
# verdicts. A model also describes Go code: a change to a file or package its
# README or .tla names (internal/..., cmd/...) selects it too, so the model is
# re-run, and re-read, when the code it claims to model moves. A change to the runner (run-model.sh), the tool installer and its
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

code=$(
  cd "$(git rev-parse --show-toplevel)"
  git diff --name-only "$base" -- internal cmd
  git ls-files --others --exclude-standard -- internal cmd
)

# mapped DIR: every file the docs and specs in DIR name as code (a .go file
# with any :line suffix dropped) or as a package directory.
mapped() {
  cat "$1"/README.md "$1"/*.tla 2>/dev/null |
    grep -oE '(internal|cmd)/[A-Za-z0-9_./-]*[A-Za-z0-9_]' | sort -u || true
}

# covers PATH FILE: FILE is PATH, or lies below the package directory PATH.
covers() {
  [[ "$2" == "$1" || ( "$1" != *.go && "$2" == "$1"/* ) ]]
}

{
  # grep exits 1 when no spec changed; that is an empty selection.
  grep -oE '^specs/[^/]+/' <<<"$changed" || true
  if [[ -n "$code" ]]; then
    for d in specs/*/; do
      while read -r p; do
        [[ -z "$p" ]] && continue
        while read -r f; do
          if covers "$p" "$f"; then
            echo "$d"
            break 2
          fi
        done <<<"$code"
      done < <(mapped "$d")
    done
  fi
} | sort -u | while read -r d; do
  if [[ -d "$d" ]]; then
    echo "${d%/}"
  fi
done
