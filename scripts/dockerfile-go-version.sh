#!/usr/bin/env bash
#
# Prints the Go release the core image builds the bridge with (the tag of
# GO_IMAGE in container/Dockerfile, such as 1.26.10), so govulncheck can
# check that release and not the one CI happens to have.
#
#   scripts/dockerfile-go-version.sh [DOCKERFILE]

set -euo pipefail

file="${1:-$(dirname "$0")/../container/Dockerfile}"
v="$(sed -n 's/^ARG GO_IMAGE=golang:\([0-9][0-9.]*\)-.*/\1/p' "$file")"
[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "no Go patch release in GO_IMAGE of $file" >&2; exit 1; }
echo "$v"
