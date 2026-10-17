#!/usr/bin/env bash
#
# Tests for check-action-pins.sh: each case writes a one-step workflow to a
# temp dir and checks whether the checker accepts it, and the last case runs
# it on this repository's own workflows.
#
#   scripts/test-check-action-pins.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
check="$here/check-action-pins.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

sha="3d3c42e5aac5ba805825da76410c181273ba90b1"
failed=0

# expect NAME WANT USES — WANT is "pass" or "fail": whether the checker
# accepts a step whose uses: line is USES (the whole line, after the dash).
expect() {
  local name="$1" want="$2" uses="$3" got=pass
  printf 'jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - %s\n' "$uses" >"$work/ci.yml"
  "$check" "$work/ci.yml" >"$work/out" 2>&1 || got=fail
  if [ "$got" = "$want" ]; then
    printf 'ok    %s\n' "$name"
  else
    printf 'FAIL  %s: got %s, want %s\n' "$name" "$got" "$want"
    cat "$work/out"
    failed=$((failed + 1))
  fi
}

expect "a full SHA with the release in a comment" pass "uses: actions/checkout@$sha # v7.0.1"
expect "a full SHA in quotes" pass "uses: \"actions/checkout@$sha\""
expect "an action in a subdirectory, pinned" pass "uses: github/codeql-action/init@$sha # v4"
expect "a local action" pass "uses: ./.github/actions/setup"
expect "a docker image by digest" pass "uses: docker://alpine@sha256:$sha${sha:0:24}"
expect "a major tag" fail "uses: actions/checkout@v4"
expect "a full release tag" fail "uses: actions/checkout@v7.0.1"
expect "a branch" fail "uses: actions/checkout@main"
expect "a short SHA" fail "uses: actions/checkout@${sha:0:12}"
expect "no ref at all" fail "uses: actions/checkout"
expect "a tag with a SHA only in the comment" fail "uses: actions/checkout@v4 # $sha"
expect "a docker image by tag" fail "uses: docker://alpine:3"
expect "a tag in quotes" fail "uses: 'actions/checkout@v4'"
# uses: on its own line, after the step's name, not after the dash.
expect "a tag below the step name" fail "name: checkout
        uses: actions/checkout@v4"

if out="$("$check" 2>&1)"; then
  printf 'ok    the repository'"'"'s workflows are pinned\n'
else
  printf 'FAIL  the repository'"'"'s workflows are pinned\n%s\n' "$out"
  failed=$((failed + 1))
fi

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
