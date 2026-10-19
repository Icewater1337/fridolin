#!/usr/bin/env bash
#
# Runs one slice of the Go tests, so CI can spread race tests over jobs.
#
#   scripts/test-shard.sh I N [go test flags...]     # e.g. 2 3 -race -count=1
#
# Package names and their tests, fuzz targets and examples are sorted, then
# dealt out in turn. Each (package, name) runs in exactly one slice, even
# when several packages use the same name. 1 1 runs them all. Listing builds
# each test binary with the same flags, so execution reuses it and packages
# without tests are still checked for compilation errors.
#
# Exit status: 0 on success; 1 for a failed package or no tests; 2 for usage.
set -euo pipefail

usage() {
	echo "usage: $0 I N [go test flags...]   (1 <= I <= N)" >&2
	exit 2
}
[ $# -ge 2 ] || usage
i="$1" n="$2"
shift 2
if ! [[ $i =~ ^[1-9][0-9]*$ && $n =~ ^[1-9][0-9]*$ ]] || ((i > n)); then
	usage
fi
go="${GO:-go}"
packages="$("$go" list ./... | LC_ALL=C sort)"
total=0 selected=0 failed=0
while IFS= read -r pkg; do
	[ -n "$pkg" ] || continue
	# Capture the command separately: a build failure must not be mistaken
	# for a package that has no tests, and must fail the whole slice.
	if ! listed="$("$go" test "$@" -list '.*' "$pkg")"; then
		exit 1
	fi
	names="$(printf '%s\n' "$listed" | awk '/^(Test|Fuzz|Example)/' | LC_ALL=C sort)"
	[ -n "$names" ] || continue
	count="$(printf '%s\n' "$names" | wc -l | tr -d ' ')"
	slice="$(printf '%s\n' "$names" | awk -v i="$i" -v n="$n" -v offset="$total" '(NR - 1 + offset) % n == i - 1' | paste -sd '|' -)"
	total=$((total + count))
	# An empty selection must not turn into an unrestricted -run expression.
	[ -n "$slice" ] || continue
	chosen="$(printf '%s\n' "$slice" | tr '|' '\n' | wc -l | tr -d ' ')"
	selected=$((selected + chosen))
	echo "$pkg: slice $i of $n runs $chosen of $count tests, fuzz targets and examples"
	if ! "$go" test "$@" -run "^($slice)\$" "$pkg"; then
		failed=1
	fi
done <<< "$packages"
[ "$total" -gt 0 ] || { echo "$0: go test -list found no tests" >&2; exit 1; }
echo "slice $i of $n: $selected of $total tests, fuzz targets and examples"
exit "$failed"
