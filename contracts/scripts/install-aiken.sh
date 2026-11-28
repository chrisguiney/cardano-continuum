#!/usr/bin/env bash
# Installs the Aiken compiler pinned in contracts/tools.lock into
# .tools/aiken/<version>-<target>/ at the repository root and prints the binary's path.
# Nothing is installed system-wide and an aiken on PATH is never used, so
# every build runs the same compiler. Delete .tools/aiken to uninstall.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK="$ROOT/contracts/tools.lock"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
lock() { sed -n "s/^$1=//p" "$LOCK"; }

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) target=aarch64-apple-darwin ;;
  Linux-x86_64) target=x86_64-unknown-linux-musl ;;
  Linux-aarch64 | Linux-arm64) target=aarch64-unknown-linux-musl ;;
  *) die "no pinned Aiken release for $(uname -s)-$(uname -m)" ;;
esac

version="$(lock aiken_version)"
want="$(lock "aiken_sha256_$target")"
[[ -n "$version" && -n "$want" ]] || die "contracts/tools.lock has no aiken_version or checksum for $target"

dir="$ROOT/.tools/aiken/$version-$target"
bin="$dir/aiken"
stamp="$dir/.sha256"
if [[ -x "$bin" && -f "$stamp" && "$(cat "$stamp")" == "$want" ]]; then
  echo "$bin"
  exit 0
fi

mkdir -p "$dir"
tmp="$dir/aiken.tar.gz.partial"
url="https://github.com/aiken-lang/aiken/releases/download/v$version/aiken-$target.tar.gz"
curl -fsSL -o "$tmp" "$url" >&2
got="$(sha256 "$tmp")"
if [[ "$got" != "$want" ]]; then
  rm -f "$tmp"
  die "checksum mismatch for $url (expected $want, got $got)"
fi
tar -xzf "$tmp" -C "$dir" --strip-components=1 "aiken-$target/aiken"
rm -f "$tmp"

ran="$("$bin" --version)"
[[ "$ran" == "aiken v$version"* ]] || die "installed binary reports '$ran', not v$version"
echo "$want" > "$stamp"
echo "$bin"
