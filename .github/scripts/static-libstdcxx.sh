#!/usr/bin/env bash
# Prepares a directory holding only the cross toolchain's libstdc++.a, for a linux full build (a
# pre build hook in .goreleaser.yml). The linker finds -lstdc++ (DuckDB's cgo flags ask for it by
# name) in the first -L directory that has it, and with no libstdc++.so beside it takes the static
# archive. `-static-libstdc++` alone does not do it: it only covers the implicit libstdc++, not an
# explicit -lstdc++.
# Usage: static-libstdcxx.sh <os> <arch> <dir>; any os but linux is skipped.
set -euo pipefail

os=$1 arch=$2 dir=$3
[ "$os" = linux ] || exit 0

case "$arch" in
  amd64) triple=x86_64-unknown-linux-gnu ;;
  arm64) triple=aarch64-unknown-linux-gnu ;;
  *) echo "static-libstdcxx: unknown arch $arch" >&2; exit 1 ;;
esac

archive=$("$triple-g++" -print-file-name=libstdc++.a)
[ -f "$archive" ] || { echo "static-libstdcxx: $triple-g++ has no libstdc++.a" >&2; exit 1; }
mkdir -p "$dir"
ln -sf "$archive" "$dir/libstdc++.a"
