#!/usr/bin/env bash
#
# Runs the Go tests that differ on one system from Linux, so macOS and
# Windows CI test what is theirs while Linux CI runs everything.
#
#   scripts/os-tests.sh [--goos GOOS] [--list] [go test flags...]
#
# GOOS defaults to the system it runs on. --list prints what would run, one
# line per package: its import path, then a tab and a -run expression when
# only some of its tests run. Without --list, go test runs them with the
# flags given (e.g. -race -count=1).
#
# A package is OS-specific for GOOS when:
#   - go list builds other files into it for GOOS than for Linux: all of
#     its tests run;
#   - or some of its test files carry a build constraint (//go:build other
#     than race alone, or a _darwin/_windows/_linux name): the tests in them
#     run;
#   - or .github/os-specific-packages.txt names it for GOOS.
#
# That file adds what go list cannot see, packages whose code differs by
# runtime.GOOS (Keychain, launchd, PATH lookup), one line each:
#
#   GOOS PACKAGE            all of its tests
#   GOOS PACKAGE REGEX      the tests in its tagged files, and those REGEX
#                           matches, instead of all (for a package too big
#                           to run whole)
#   GOOS PACKAGE -          none (its tests do not pass there yet)
#
# PACKAGE is relative to the module (internal/nodeagent). Packages run with
# a REGEX share one go test run, with all their expressions, so a test of
# the same name in another of them runs too.
#
# Exit status: 0 on success; 1 for a failed test or a bad list; 2 for usage.
set -euo pipefail

usage() {
	echo "usage: $0 [--goos GOOS] [--list] [go test flags...]" >&2
	exit 2
}
go="${GO:-go}"
goos="" list=0
while [ $# -gt 0 ]; do
	case "$1" in
	--goos) [ $# -ge 2 ] || usage; goos="$2"; shift 2 ;;
	--list) list=1; shift ;;
	*) break ;;
	esac
done
[ -n "$goos" ] || goos="$("$go" env GOOS)"
[[ $goos =~ ^[a-z0-9]+$ ]] || usage
root="$(cd "$(dirname "$0")/.." && pwd)"
manual="${OS_TESTS_LIST:-$root/.github/os-specific-packages.txt}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

module="$("$go" list -m)"
# Which files each package builds from, without cgo: the choice must not
# depend on whether a C compiler is around.
CGO_ENABLED=0 GOOS=linux "$go" list -f '{{.ImportPath}} {{join .GoFiles ","}}' ./... >"$tmp/linux"
CGO_ENABLED=0 GOOS="$goos" "$go" list -f '{{.ImportPath}}	{{.Dir}}	{{join .GoFiles ","}}	{{join .TestGoFiles " "}} {{join .XTestGoFiles " "}}' ./... >"$tmp/target"

# The list's lines for this system: "PACKAGE[ REGEX]", comments dropped.
sed -e 's/#.*//' "$manual" | awk -v goos="$goos" 'NF && $1 == goos { $1 = ""; sub(/^ /, ""); print }' >"$tmp/manual"
while read -r pkg _; do
	grep -q "^$module/$pkg	" "$tmp/target" || { echo "$0: $manual names $pkg for $goos, which go list does not know" >&2; exit 1; }
done <"$tmp/manual"

: >"$tmp/selected"
while IFS=$'\t' read -r pkg dir gofiles testfiles; do
	short="${pkg#"$module"/}"
	extra="$(awk -v p="$short" '$1 == p { print $2 }' "$tmp/manual")"
	[ "$extra" != - ] || continue
	listed="$(awk -v p="$short" '$1 == p { print "yes" }' "$tmp/manual")"
	if [ -n "$listed" ] && [ -z "$extra" ]; then
		printf '%s\n' "$pkg" >>"$tmp/selected"
		continue
	fi
	if [ -z "$extra" ] && [ "$(awk -v p="$pkg" '$1 == p { print $2 }' "$tmp/linux")" != "$gofiles" ]; then
		printf '%s\n' "$pkg" >>"$tmp/selected"
		continue
	fi
	names=""
	for f in $testfiles; do
		tagged=0
		case "$f" in
		*_darwin_test.go | *_windows_test.go | *_linux_test.go | *_darwin_*_test.go | *_windows_*_test.go | *_linux_*_test.go) tagged=1 ;;
		esac
		line="$(grep -m1 '^//go:build ' "$dir/$f" || true)"
		[ -z "$line" ] || [ "$line" = "//go:build race" ] || tagged=1
		[ "$tagged" = 1 ] || continue
		names="$names $(sed -n 's/^func \(Test[A-Za-z0-9_]*\)(t \*testing\.T).*/\1/p' "$dir/$f" | tr '\n' ' ')"
	done
	expr=""
	names="$(printf '%s\n' $names | grep . | LC_ALL=C sort -u | tr '\n' '|' | sed 's/|$//' || true)"
	[ -z "$names" ] || expr="^($names)\$"
	if [ -n "$extra" ]; then
		expr="${expr:+$expr|}$extra"
	fi
	[ -z "$expr" ] || printf '%s\t%s\n' "$pkg" "$expr" >>"$tmp/selected"
done <"$tmp/target"

if [ "$list" = 1 ]; then
	cat "$tmp/selected"
	exit 0
fi
[ -s "$tmp/selected" ] || { echo "$0: nothing to test for $goos" >&2; exit 1; }

status=0
whole="$(awk -F '\t' 'NF == 1 { print $1 }' "$tmp/selected")"
some="$(awk -F '\t' 'NF == 2 { print $1 }' "$tmp/selected")"
run="$(awk -F '\t' 'NF == 2 { printf "%s%s", sep, $2; sep = "|" }' "$tmp/selected")"
if [ -n "$whole" ]; then
	# shellcheck disable=SC2086 # one argument per package
	"$go" test "$@" $whole || status=1
fi
if [ -n "$some" ]; then
	# shellcheck disable=SC2086
	"$go" test "$@" -run "$run" $some || status=1
fi
exit "$status"
