#!/usr/bin/env bash
#
# Tests for the Makefile, read from what `make -n` would run, so nothing is
# built. Only build and install may write the binary: running the checks on
# a branch must not replace a build someone runs from the checkout, such as
# ./fridolin, which a service set up by an older setup.sh still starts on
# its next restart.
#
#   scripts/test-makefile.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# Run make as if from a shell, not with the flags of the make running this.
unset MAKEFLAGS MFLAGS MAKELEVEL

# A made-up name for the binary: a command that names it would write the
# build (BINARY is bin/fridolin by default).
bin="sentinel-not-the-binary"

failed=0

# expect NAME WANT TARGET PATTERN [VAR=VALUE...] — WANT is "has" or "lacks":
# whether a line of what make would run for TARGET (with the variables
# given) matches the extended regexp PATTERN.
expect() {
  local name="$1" want="$2" target="$3" pattern="$4" out got=lacks
  shift 4
  if ! out="$(make -C "$root" -n "$target" BINARY="$bin" "$@" 2>&1)"; then
    printf 'FAIL  %s: make -n %s failed\n%s\n' "$name" "$target" "$out"
    failed=$((failed + 1))
    return
  fi
  if printf '%s\n' "$out" | grep -Eq -- "$pattern"; then got=has; fi
  if [ "$got" = "$want" ]; then
    printf 'ok    %s\n' "$name"
  else
    printf 'FAIL  %s: make -n %s %s %s, want %s\n%s\n' "$name" "$target" "$got" "$pattern" "$want" "$out"
    failed=$((failed + 1))
  fi
}

# Without these the cases below would pass even if BINARY were ignored.
expect "build writes the binary" has build "-o \"$bin\"( |$)"
expect "install builds the binary it copies" has install "-o \"$bin\"( |$)"

# Every other target, as make help lists them; clean removes the binary.
targets="$(make -C "$root" -s help | awk '{ print $1 }')"
[ -n "$targets" ] || { printf 'FAIL  make help lists no targets\n'; exit 1; }
for target in $targets; do
  case "$target" in build | install | clean) continue ;; esac
  expect "$target leaves the binary alone" lacks "$target" "$bin"
done

expect "smoke builds with the race detector into its temp dir" has smoke 'build -race .*-o "\$tmp/fridolin"'

# fridolin version names the build: what it is given (setup.sh passes
# its own), else what git describe says of the checkout.
expect "build stamps the version it is given" has build '-X main\.version=v0\.0\.0-sentinel"' VERSION=v0.0.0-sentinel
described="$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)"
expect "build stamps git describe by default" has build "-X main\\.version=$described\""
# The linker skips an -X it has no variable for: smoke.sh checks that the
# stamp reaches the binary.
expect "smoke stamps the version it is given" has smoke 'build -race -ldflags "-X main\.version=v0\.0\.0-sentinel"' VERSION=v0.0.0-sentinel
expect "smoke has smoke.sh check the stamp" has smoke 'scripts/smoke\.sh "\$tmp/fridolin" "v0\.0\.0-sentinel"$' VERSION=v0.0.0-sentinel

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
