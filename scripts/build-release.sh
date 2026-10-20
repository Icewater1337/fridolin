#!/usr/bin/env bash
#
# Builds one release archive, OUTDIR/fridolin_<version>_<os>_<arch>.tar.gz
# (.zip with fridolin.exe for Windows),
# holding the binary, the Python tools, README.md, LICENSE and
# THIRD_PARTY_NOTICES, the licences of the modules the binary links, which
# whoever passes the binary on must pass on with it. Unpacked,
# `./fridolin service install` installs the binary and the tools next to it.
# The release workflow runs it once per platform; it works locally too:
#
#   scripts/build-release.sh v1.2.3 darwin arm64 dist
#
# The binary is pure Go (cgo off), so any machine can build any target; the
# release workflow still builds each OS on its own runner to test it there.

set -euo pipefail

if [ $# -ne 4 ]; then
  echo "usage: $0 VERSION GOOS GOARCH OUTDIR" >&2
  exit 2
fi
version="$1" goos="$2" goarch="$3" out="$4"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pkg=./cmd/fridolin

name="fridolin_${version}_${goos}_${goarch}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/$name/tools" "$out"
exe=""
[ "$goos" = windows ] && exe=".exe"

( cd "$repo" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X github.com/Icewater1337/fridolin/internal/version.version=$version" -o "$stage/$name/fridolin$exe" "$pkg" )
# What `fridolin service install` copies from tools/: the helpers and their
# requirements, not their tests.
cp "$repo"/tools/*.py "$repo"/tools/requirements*.txt "$stage/$name/tools/"
cp "$repo/README.md" "$repo/LICENSE" "$repo/THIRD_PARTY_NOTICES" "$stage/$name/"

if [ "$goos" = windows ]; then
  out_abs="$(cd "$out" && pwd)"
  ( cd "$stage" && zip -qr "$out_abs/$name.zip" "$name" )
  echo "$out/$name.zip"
else
  tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
  echo "$out/$name.tar.gz"
fi
