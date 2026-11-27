#!/usr/bin/env bash
# Checks the configurations of one model directory and says whether each one
# did what it is supposed to:
#   - a config whose name ends in Bug.cfg is EXPECTED TO FAIL (a mutation, a
#     known bug, or a reachability probe), and only on the property it names:
#     its single INVARIANT or PROPERTY entry, or the name on a
#     "\* EXPECT: Name" line. A deadlock counts only under "\* EXPECT: Deadlock";
#   - every other config must pass.
# A config names its module on a "\* SPEC: Name.tla" line; without one, the
# module whose name is the longest prefix of the config's name is used.
# A parse or evaluation error is a broken model, not a finding.
#
# Each verdict, the property it failed on and the distinct state count are
# compared with the directory's committed verdicts.tsv: a changed property, a
# passing config whose distinct count moved, a config with no row and a row
# with no config are all wrong. TLC_RECORD=1 writes the run into verdicts.tsv
# instead (only when every verdict was as expected), so the change shows up in
# review.
#
# TLC runs one at a time per user (a lock in TLC_LOCK, default
# $TMPDIR/continuum-tlc.lock; TLC_LOCK_WAIT seconds to wait for it, default
# 3600), each with a private java.io.tmpdir and -metadir, so concurrent runs
# cannot rewrite each other's extracted modules or delete each other's states.
# bin/_java caps the heap (TLC_HEAP, default 6g).
#
# The outcome is read from TLC's output with the patterns of
# internal/tla/outcome.go (Classify); keep the two in step.
#
# Usage: scripts/run-model.sh specs/<dir> [Config.cfg ...]
set -uo pipefail
exec </dev/null

dir="${1:?usage: scripts/run-model.sh specs/<model-directory> [Config.cfg ...]}"
shift
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root/$dir" || exit 2

if ! toolchain="$("$root/bin/_java" --check 2>&1)"; then
  echo "$dir: $toolchain" >&2
  exit 2
fi

lock="${TLC_LOCK:-${TMPDIR:-/tmp}/continuum-tlc.lock}"
lock_wait="${TLC_LOCK_WAIT:-3600}"
work=""
tlc_pid=""

release() {
  [[ -n "$tlc_pid" ]] && kill "$tlc_pid" 2>/dev/null
  [[ -n "$work" ]] && rm -rf "$work"
  if [[ "$(cat "$lock/pid" 2>/dev/null)" == "$$" ]]; then
    rm -rf "$lock"
  fi
}
trap release EXIT
trap 'exit 130' INT TERM

# mkdir is atomic, and macOS has no flock(1). A lock whose holder has exited
# is taken over; an empty pid file is a holder that has not written it yet.
waited=0
until mkdir "$lock" 2>/dev/null; do
  holder="$(cat "$lock/pid" 2>/dev/null || true)"
  if [[ -n "$holder" ]] && ! kill -0 "$holder" 2>/dev/null; then
    [[ "$(cat "$lock/pid" 2>/dev/null)" == "$holder" ]] && rm -rf "$lock"
    continue
  fi
  if (( waited >= lock_wait )); then
    echo "$dir: TLC lock $lock held by pid ${holder:-?}; gave up after ${lock_wait}s" >&2
    exit 2
  fi
  (( waited == 0 )) && echo "$dir: waiting for TLC lock $lock, held by pid ${holder:-?}" >&2
  sleep 1
  waited=$((waited + 1))
done
echo $$ >"$lock/pid"

work="$(mktemp -d "${TMPDIR:-/tmp}/tlc-run.XXXXXX")"
mkdir -p "$work/java"
out="$work/out"
rows="$work/rows"
: >"$rows"
echo "  $toolchain"

# props CFG: the cfg's INVARIANT and PROPERTY entries, one "inv Name" or
# "prop Name" per line.
props() {
  awk '{ sub(/\\\*.*/, "") }
    {
      for (i = 1; i <= NF; i++) {
        w = $i
        if (w ~ /^(SPECIFICATION|INIT|NEXT|CONSTANTS?|INVARIANTS?|PROPERTY|PROPERTIES|CHECK_DEADLOCK|SYMMETRY|VIEW|CONSTRAINTS?|ACTION_CONSTRAINTS?|ALIAS|POSTCONDITION)$/) { sec = w; continue }
        if (sec ~ /^INVARIANT/) print "inv", w
        else if (sec ~ /^PROPERT/) print "prop", w
      }
    }' "$1"
}

# classify EXIT: sets outcome (pass, fails, broken) and got, the violated
# name ("Deadlock" and "Temporal" stand for the two kinds TLC names none for).
classify() {
  got="$(sed -n -E 's/^Error: (Invariant|Action property|Property|Temporal property) ([^ ]+) (was|is) violated.*/\2/p' "$out" | head -1)"
  if [[ -n "$got" ]]; then
    outcome=fails
  elif grep -q '^Error: Deadlock reached' "$out"; then
    outcome=fails got=Deadlock
  elif grep -q '^Error: Temporal properties were violated' "$out"; then
    outcome=fails got=Temporal
  elif [[ "$1" -eq 0 ]] && grep -q '^Model checking completed. No error has been found' "$out"; then
    outcome=pass
  else
    outcome=broken
  fi
}

# module CFG: the .tla file the cfg checks.
module() {
  local name="${1%.cfg}" tla best=0 candidate base
  tla="$(sed -n 's/^\\\* *SPEC: *//p' "$1")"
  if [[ -z "$tla" ]]; then
    for candidate in *.tla; do
      base="${candidate%.tla}"
      if [[ "$name" == "$base"* && ${#base} -gt $best ]]; then
        tla="$candidate"
        best=${#base}
      fi
    done
  fi
  echo "${tla:-$name.tla}"
}

# judge CFG: sets verdict_ok (yes/no), property and why for one finished run.
judge() {
  local cfg="$1" expect n live
  expect="$(sed -n 's/^\\\* *EXPECT: *\([^ ]*\).*/\1/p' "$cfg" | head -1)"
  live="$(props "$cfg" | awk '$1 == "prop" { print $2 }')"
  property="-"
  verdict_ok=no
  if [[ "$got" == Temporal && $(grep -c . <<<"$live") -eq 1 ]]; then
    # TLC 2.19 does not name the temporal property it found violated; with
    # one PROPERTY entry the name is unambiguous.
    got="$live"
  fi

  if [[ "$cfg" != *Bug.cfg ]]; then
    case "$outcome" in
      pass) verdict_ok=yes ;;
      fails) why="failed on $got, expected pass" ;;
      *) why="TLC could not check it (parse or evaluation error)" ;;
    esac
    return
  fi

  if [[ -z "$expect" ]]; then
    n="$(props "$cfg" | grep -c .)"
    if (( n != 1 )); then
      why="names $n properties; a Bug config names one, or says which with a \\* EXPECT: line"
      return
    fi
    expect="$(props "$cfg" | awk '{ print $2 }')"
  fi

  case "$outcome" in
    pass) why="passed, expected failure on $expect" ;;
    broken) why="TLC could not check it (parse or evaluation error)" ;;
    *)
      if [[ "$got" == Temporal ]]; then
        why="TLC does not name the temporal property it found violated; list exactly one under PROPERTIES"
      elif [[ "$got" != "$expect" ]]; then
        why="failed on $got, expected $expect"
      else
        verdict_ok=yes
        property="$got"
      fi
      ;;
  esac
}

# baseline CFG FIELD: the verdicts.tsv value of FIELD (2 verdict, 3
# property, 4 distinct) for CFG, or nothing.
baseline() {
  [[ -f verdicts.tsv ]] || return 0
  awk -F '\t' -v c="$1" -v f="$2" '$1 == c { print $f; exit }' verdicts.tsv
}

if (( $# > 0 )); then
  cfgs=("$@")
else
  cfgs=(*.cfg)
fi

ok=0
wrong=0
for cfg in "${cfgs[@]}"; do
  name="${cfg%.cfg}"
  tla="$(module "$cfg")"
  start=$SECONDS
  JAVA_TOOL_OPTIONS="${JAVA_TOOL_OPTIONS:+$JAVA_TOOL_OPTIONS }-Djava.io.tmpdir=$work/java" \
    "$root/bin/tlc" -workers auto -metadir "$work/states-$name" -config "$cfg" "$tla" >"$out" 2>&1 &
  tlc_pid=$!
  wait "$tlc_pid"
  code=$?
  tlc_pid=""
  secs=$((SECONDS - start))
  rm -rf "$work/states-$name"

  classify "$code"
  judge "$cfg"
  distinct="$(sed -n -E 's/.* ([0-9,]+) distinct states found.*/\1/p' "$out" | tail -1 | tr -d ,)"
  depth="$(sed -n -E 's/^The depth of the complete state graph search is ([0-9]+).*/\1/p' "$out" | tail -1)"
  verdict=pass
  [[ "$cfg" == *Bug.cfg ]] && verdict=fails

  if [[ "$verdict_ok" != yes ]]; then
    wrong=$((wrong + 1))
    echo "  WRONG  $cfg: $why"
    grep -E 'violated|Deadlock|Error' "$out" | head -3 | sed 's/^/         /'
    continue
  fi

  if [[ "$verdict" == pass ]]; then
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$cfg" pass - "${distinct:--}" "${depth:--}" "$secs" >>"$rows"
    echo "  pass   $cfg (${distinct:-?} distinct, ${secs}s)"
  else
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$cfg" fails "$property" - - "$secs" >>"$rows"
    echo "  fails  $cfg on $property (as expected)"
  fi

  if [[ "${TLC_RECORD:-}" == 1 ]]; then
    ok=$((ok + 1))
    continue
  fi

  was="$(baseline "$cfg" 2)"
  if [[ -z "$was" ]]; then
    wrong=$((wrong + 1))
    echo "  NEW    $cfg: no row in verdicts.tsv (record it with TLC_RECORD=1)"
  elif [[ "$was" != "$verdict" || "$(baseline "$cfg" 3)" != "$property" ]]; then
    wrong=$((wrong + 1))
    echo "  CHANGED $cfg: $was $(baseline "$cfg" 3) -> $verdict $property"
  elif [[ "$verdict" == pass && "$(baseline "$cfg" 4)" != "${distinct:--}" ]]; then
    wrong=$((wrong + 1))
    echo "  CHANGED $cfg: distinct $(baseline "$cfg" 4) -> ${distinct:--} (a drop is a lost behaviour until shown otherwise)"
  else
    ok=$((ok + 1))
  fi
done

if (( $# == 0 )) && [[ -f verdicts.tsv && "${TLC_RECORD:-}" != 1 ]]; then
  while IFS=$'\t' read -r cfg _; do
    if [[ "$cfg" == *.cfg && ! -f "$cfg" ]]; then
      wrong=$((wrong + 1))
      echo "  GONE   $cfg: in verdicts.tsv but no such config"
    fi
  done <verdicts.tsv
fi

if [[ "${TLC_RECORD:-}" == 1 ]]; then
  if (( wrong > 0 )); then
    echo "$dir: not recording verdicts.tsv while $wrong verdicts are wrong"
  else
    {
      printf 'config\tverdict\tproperty\tdistinct\tdepth\tsecs\n'
      {
        if (( $# > 0 )) && [[ -f verdicts.tsv ]]; then
          awk -F '\t' 'NR == FNR { run[$1] = 1; next } FNR > 1 && !($1 in run)' "$rows" verdicts.tsv
        fi
        cat "$rows"
      } | sort
    } >"$work/verdicts.tsv"
    mv "$work/verdicts.tsv" verdicts.tsv
    echo "$dir: recorded verdicts.tsv"
  fi
fi

echo "$dir: $ok as expected, $wrong wrong"
[[ "$wrong" -eq 0 ]]
