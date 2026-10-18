#!/usr/bin/env bash
#
# Runs one slice of the Go tests, so that CI can spread the slowest run, the
# race tests on macOS, over several jobs at once.
#
#   scripts/test-shard.sh I N [go test flags...]     # e.g. 2 3 -race -count=1
#
# The tests and fuzz targets are listed by name, sorted, and dealt out in
# turn, so each slice gets its share of every family of tests (the sandbox
# tests, which are slow on macOS, say) and every name lands in exactly one
# slice, a test added tomorrow included. 1 1 runs them all. Listing builds
# the test binary with the same flags, so the run that follows reuses it.
#
# Exit status: that of go test; 2 for wrong arguments.
set -euo pipefail

usage() {
	echo "usage: $0 I N [go test flags...]   (1 <= I <= N)" >&2
	exit 2
}
[ $# -ge 2 ] || usage
i="$1" n="$2"
shift 2
if ! [[ $i =~ ^[0-9]+$ && $n =~ ^[0-9]+$ ]] || ((i < 1 || i > n)); then
	usage
fi
go="${GO:-go}"

# The slices are of one package; a second one would go untested.
packages="$("$go" list ./...)"
if [ "$(printf '%s\n' "$packages" | wc -l)" -ne 1 ]; then
	printf '%s: the tests are in more than one package now; slice each:\n%s\n' "$0" "$packages" >&2
	exit 1
fi

names="$("$go" test "$@" -list '.*' . | grep -E '^(Test|Fuzz|Example)' | sort)"
[ -n "$names" ] || { echo "$0: go test -list found no tests" >&2; exit 1; }
slice="$(printf '%s\n' "$names" | awk -v i="$i" -v n="$n" '(NR - 1) % n == i - 1' | paste -sd '|' -)"
echo "slice $i of $n: $(printf '%s\n' "$slice" | tr '|' '\n' | wc -l | tr -d ' ') of $(printf '%s\n' "$names" | wc -l | tr -d ' ') tests and fuzz targets"
exec "$go" test "$@" -run "^($slice)\$" .
