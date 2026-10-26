#!/usr/bin/env bash
#
# Fail when a GitHub workflow uses an action by tag or branch instead of a
# full commit SHA. A tag can be moved to other code after it was reviewed; a
# SHA cannot. Renovate moves the pins and the release comments next to
# them. A local action (./path) needs no pin, and a docker:// image needs a
# digest.
#
#   scripts/check-action-pins.sh [FILE...]   # default .github/workflows/*
#
# Exit status: 0 when every action is pinned, 1 when one is not.

set -euo pipefail

if [ "$#" -eq 0 ]; then
  cd "$(dirname "$0")/.."
  set -- .github/workflows/*.yml .github/workflows/*.yaml
fi

unpinned=0
for f in "$@"; do
  # A pattern that matched nothing stays as it is.
  [ -e "$f" ] || continue
  while IFS= read -r hit; do
    line="${hit%%:*}"
    # The value after "uses:", without a trailing comment or quotes.
    ref="$(printf '%s\n' "${hit#*:}" | sed -E \
      -e 's/^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*//' \
      -e 's/[[:space:]]+#.*$//' -e 's/[[:space:]]+$//' \
      -e "s/^[\"']//" -e "s/[\"']\$//")"
    case "$ref" in
      ./*) continue ;;
      docker://*@sha256:*) continue ;;
    esac
    if ! printf '%s\n' "$ref" | grep -Eq '^[^@[:space:]]+@[0-9a-f]{40}$'; then
      printf '%s:%s: %s is not pinned to a full commit SHA\n' "$f" "$line" "$ref" >&2
      unpinned=$((unpinned + 1))
    fi
  done < <(grep -nE '^[[:space:]]*(-[[:space:]]+)?uses:' "$f" || true)
done

if [ "$unpinned" -gt 0 ]; then
  printf '%d action(s) not pinned; pin each to the commit its release tag names, with the tag in a comment\n' "$unpinned" >&2
  exit 1
fi
