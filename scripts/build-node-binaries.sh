#!/usr/bin/env bash
#
# Builds the node binaries a core updates its nodes to (docs/NODES.md,
# "Updates"): fridolin for each platform a node runs on, stamped with
# VERSION, stripped and gzip'd, with OUTDIR/manifest.json listing each
# one's platform, file, and the SHA-256 and size of the binary unpacked.
#
#   scripts/build-node-binaries.sh v1.2.3 dist/node-binaries
#
# The core image runs it at build time into /opt/fridolin/node-binaries;
# a native core finds them in node-binaries beside its binary
# (~/.local/lib/fridolin/node-binaries for the service) or in
# $FRIDOLIN_NODE_BINARIES: make node-binaries. VERSION must be the core's
# own (`fridolin version`), or the core does not offer them. PLATFORMS
# overrides the list ("darwin/arm64 linux/amd64").

set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 VERSION OUTDIR" >&2
  exit 2
fi
version="$1" out="$2"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
platforms="${PLATFORMS:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64}"
go="${GO:-go}"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

mkdir -p "$out"
out="$(cd "$out" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

entries=""
for p in $platforms; do
  goos="${p%/*}" goarch="${p#*/}"
  exe=""
  [ "$goos" = windows ] && exe=".exe"
  bin="$stage/fridolin-$goos-$goarch$exe"
  ( cd "$repo" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$go" build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/Icewater1337/fridolin/internal/version.version=$version" \
      -o "$bin" ./cmd/fridolin )
  file="fridolin-$goos-$goarch.gz"
  sum="$(sha256 "$bin")"
  size="$(wc -c <"$bin" | tr -d ' ')"
  gzip -9 -n -c "$bin" >"$out/$file"
  rm -f "$bin"
  entries="$entries${entries:+,}
    {\"os\": \"$goos\", \"arch\": \"$goarch\", \"file\": \"$file\", \"sha256\": \"$sum\", \"size\": $size}"
  echo "$out/$file ($size bytes unpacked)"
done

printf '{\n  "version": "%s",\n  "binaries": [%s\n  ]\n}\n' "$version" "$entries" >"$out/manifest.json"
echo "$out/manifest.json"
