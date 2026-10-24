#!/usr/bin/env bash
#
# Runs one slice of the Go tests, so CI can spread race tests over jobs.
#
#   scripts/test-shard.sh I N [go test flags...]     # e.g. 2 3 -race -count=1
#
# Package names and their tests, fuzz targets and examples are sorted, then
# dealt out in turn. Each (package, name) runs in exactly one slice, even
# when several packages use the same name. 1 1 runs them all. One go test
# -list over every package builds the test binaries in parallel, with the
# same flags, so execution reuses the build and packages without tests are
# still checked for compilation errors.
#
# The slice's packages then run side by side, SHARD_JOBS at a time (default:
# the number of CPUs, as go test ./... does), the ones with the most tests
# first. Each package's output is printed in one piece once it finishes.
#
# Exit status: 0 on success; 1 for a failed package or no tests; 2 for usage.
set -euo pipefail

usage() {
	echo "usage: $0 I N [go test flags...]   (1 <= I <= N; SHARD_JOBS >= 1)" >&2
	exit 2
}
[ $# -ge 2 ] || usage
i="$1" n="$2"
shift 2
if ! [[ $i =~ ^[1-9][0-9]*$ && $n =~ ^[1-9][0-9]*$ ]] || ((i > n)); then
	usage
fi
max_jobs="${SHARD_JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)}"
[[ $max_jobs =~ ^[1-9][0-9]*$ ]] || usage
go="${GO:-go}"

tmp="$(mktemp -d)"
# Stop the packages still running when the slice ends early (a signal, or
# set -e), go test first, and drop their output.
cleanup() {
	local pid
	for pid in $(jobs -p); do
		pkill -P "$pid" 2>/dev/null || true
		kill "$pid" 2>/dev/null || true
	done
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

packages="$("$go" list ./... | LC_ALL=C sort)"
[ -n "$packages" ] || { echo "$0: go list found no packages" >&2; exit 1; }
# A build failure must not be mistaken for a package that has no tests: it
# fails the whole slice. go test prints each package's names before its own
# ok line, in one piece, so the names that come before "ok <pkg>" are pkg's.
# shellcheck disable=SC2086 # one argument per package
if ! "$go" test "$@" -list '.*' $packages > "$tmp/listed"; then
	cat "$tmp/listed"
	echo "$0: go test -list failed" >&2
	exit 1
fi
awk -F '\t' '
	/^(Test|Fuzz|Example)[^[:space:]]*$/ { names[++k] = $0; next }
	/^(ok|\?) / { for (j = 1; j <= k; j++) print $2 "\t" names[j]; k = 0 }
' "$tmp/listed" | LC_ALL=C sort > "$tmp/names"
total="$(wc -l < "$tmp/names" | tr -d ' ')"
[ "$total" -gt 0 ] || { echo "$0: go test -list found no tests" >&2; exit 1; }

# Deal the sorted (package, name) pairs out in turn. Each package with a
# share of this slice gets a line: its names in the slice, its names in all,
# the package and the -run alternatives; the most names first.
awk -F '\t' -v i="$i" -v n="$n" '
	function flush() {
		# An empty selection must not turn into an unrestricted -run.
		if (chosen > 0) printf "%d\t%d\t%s\t%s\n", chosen, all, pkg, alts
	}
	$1 != pkg { flush(); pkg = $1; all = 0; chosen = 0; alts = "" }
	{ all++ }
	(NR - 1) % n == i - 1 { alts = alts (chosen++ ? "|" : "") $2 }
	END { flush() }
' "$tmp/names" | LC_ALL=C sort -t "$(printf '\t')" -k1,1nr -k3,3 > "$tmp/work"

# Starts work line k in the background. It writes the output to k.out and,
# once done, the exit status to k.status.
start() {
	local k="$1" chosen all pkg alts
	shift
	IFS=$'\t' read -r chosen all pkg alts <<< "$(sed -n "${k}p" "$tmp/work")"
	selected=$((selected + chosen))
	echo "$pkg: slice $i of $n runs $chosen of $all tests, fuzz targets and examples"
	printf '%s\n' "$pkg" > "$tmp/$k.pkg"
	(
		status=0
		"$go" test "$@" -run "^($alts)\$" "$pkg" > "$tmp/$k.out" 2>&1 < /dev/null || status=$?
		echo "$status" > "$tmp/$k.part"
		mv "$tmp/$k.part" "$tmp/$k.status"
	) &
}

count="$(wc -l < "$tmp/work" | tr -d ' ')"
selected=0 next=1 running="" busy=0 failed=""
while [ "$next" -le "$count" ] || [ -n "$running" ]; do
	still=""
	for k in $running; do
		if [ -e "$tmp/$k.status" ]; then
			cat "$tmp/$k.out"
			[ "$(cat "$tmp/$k.status")" -eq 0 ] || failed="$failed $(cat "$tmp/$k.pkg")"
			busy=$((busy - 1))
		else
			still="$still $k"
		fi
	done
	running="$still"
	while [ "$busy" -lt "$max_jobs" ] && [ "$next" -le "$count" ]; do
		start "$next" "$@"
		running="$running $next" busy=$((busy + 1)) next=$((next + 1))
	done
	[ -z "$running" ] || sleep 0.2
done
wait
echo "slice $i of $n: $selected of $total tests, fuzz targets and examples"
[ -z "$failed" ] || { echo "slice $i of $n failed in:$failed"; exit 1; }
