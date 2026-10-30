#!/usr/bin/env bash
# Checks a linux full-build binary after GoReleaser links it (a post build hook in .goreleaser.yml,
# so a failure stops the release before anything is published). The binary must not need
# libstdc++ or libgcc_s, or any GLIBCXX_ or CXXABI_ version, from the host: the GCC 15 cross
# toolchains' libstdc++ is newer than Ubuntu 22.04's and Debian 12's (v0.3.1 failed there with
# "version `GLIBCXX_3.4.32' not found"). It prints the highest GLIBC_ version the binary needs.
# Usage: check-linux-static.sh <os> <arch> <binary>; any os but linux is skipped.
set -euo pipefail

os=$1 arch=$2 bin=$3
[ "$os" = linux ] || exit 0

case "$arch" in
  amd64) triple=x86_64-unknown-linux-gnu ;;
  arm64) triple=aarch64-unknown-linux-gnu ;;
  *) echo "check-linux-static: unknown arch $arch" >&2; exit 1 ;;
esac
readelf=$triple-readelf

needed=$("$readelf" -d "$bin" | grep NEEDED || true)
versions=$("$readelf" --version-info "$bin")

fail=0
if echo "$needed" | grep -Eq 'libstdc\+\+|libgcc_s'; then
  echo "check-linux-static: $bin needs a dynamic libstdc++ or libgcc_s:" >&2
  echo "$needed" >&2
  fail=1
fi
if echo "$versions" | grep -Eq 'GLIBCXX_|CXXABI_|GCC_[0-9]'; then
  echo "check-linux-static: $bin requires symbol versions from libstdc++ or libgcc_s:" >&2
  echo "$versions" | grep -E 'GLIBCXX_|CXXABI_|GCC_[0-9]' >&2
  fail=1
fi
[ "$fail" = 0 ] || exit 1

glibc=$(echo "$versions" | grep -o 'GLIBC_[0-9.]*' | sort -u -t_ -k2 -V | tail -1)
echo "check-linux-static: $os/$arch ok, no libstdc++ dependency; highest ${glibc:-GLIBC_none} required"
