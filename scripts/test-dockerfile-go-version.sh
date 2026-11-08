#!/usr/bin/env bash
#
# Tests for dockerfile-go-version.sh, and holds the core image to a Go
# release with the standard library's security fixes: 1.26.8 lacked those
# of 1.26.9 and 1.26.10. Raise MIN_GO when a later release fixes more.
#
#   scripts/test-dockerfile-go-version.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
MIN_GO=1.26.10
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failed=0

ok() { printf 'ok    %s\n' "$1"; }
bad() { printf 'FAIL  %s\n' "$1"; failed=1; }

printf 'ARG GO_IMAGE=golang:1.26.8-bookworm\n' >"$work/Dockerfile"
[ "$("$here/dockerfile-go-version.sh" "$work/Dockerfile")" = 1.26.8 ] && ok "reads the tag" || bad "reads the tag"

printf 'ARG GO_IMAGE=golang:1.26-bookworm\n' >"$work/Dockerfile"
"$here/dockerfile-go-version.sh" "$work/Dockerfile" >/dev/null 2>&1 && bad "refuses a tag without a patch release" || ok "refuses a tag without a patch release"

got="$("$here/dockerfile-go-version.sh")"
if [ "$(printf '%s\n%s\n' "$MIN_GO" "$got" | sort -V | head -1)" = "$MIN_GO" ]; then
  ok "container/Dockerfile ships Go $got (>= $MIN_GO)"
else
  bad "container/Dockerfile ships Go $got, older than $MIN_GO"
fi

exit "$failed"
