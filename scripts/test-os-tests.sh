#!/usr/bin/env bash
#
# Tests for os-tests.sh's choice of packages and tests, on a small module
# it writes to a temp dir: go list only, nothing is built or run. CI's
# go-checks job checks the repository's own list (os-tests.sh --list).
#
#   scripts/test-os-tests.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/scripts/os-tests.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failed=0
if ! command -v go >/dev/null; then
	echo "skipped: no go on PATH"
	exit 0
fi

# A module old enough that no Go release fetches a newer toolchain for it.
mod="$tmp/mod"
mkdir -p "$mod"
printf 'module example.test/m\n\ngo 1.21\n' >"$mod/go.mod"
# file PATH CONTENT — a Go file of the fake module.
file() {
	mkdir -p "$(dirname "$mod/$1")"
	printf '%s\n' "$2" >"$mod/$1"
}
# plain: the same on every system.
file plain/a.go 'package plain'
file plain/a_test.go 'package plain
import "testing"
func TestPlain(t *testing.T) {}'
# code: a file of its own on macOS, so every test runs there.
file code/a.go 'package code'
file code/a_linux.go 'package code'
file code/a_darwin.go 'package code'
file code/a_windows.go 'package code'
file code/a_test.go 'package code
import "testing"
func TestCode(t *testing.T) {}'
# tagged: the same code, with tests in files for some systems.
file tagged/a.go 'package tagged'
file tagged/a_test.go 'package tagged
import "testing"
func TestEverywhere(t *testing.T) {}'
file tagged/unix_test.go '//go:build unix

package tagged
import "testing"
func TestUnixOne(t *testing.T) {}
func TestUnixTwo(t *testing.T) {}
func helper(t *testing.T) {}'
file tagged/mac_darwin_test.go 'package tagged
import "testing"
func TestMac(t *testing.T) {}'
file tagged/race_test.go '//go:build race

package tagged
import "testing"
func TestRaceOnly(t *testing.T) {}'
file tagged/main_test.go '//go:build unix

package tagged
import ("os"; "testing")
func TestMain(m *testing.M) { os.Exit(m.Run()) }'
# listed and big: same code, named in the list.
file listed/a.go 'package listed'
file listed/a_test.go 'package listed
import "testing"
func TestListed(t *testing.T) {}'
file big/a.go 'package big'
file big/a_linux.go 'package big'
file big/a_darwin.go 'package big'
file big/a_windows.go 'package big'
file big/a_test.go 'package big
import "testing"
func TestBigOS(t *testing.T) {}
func TestBigRest(t *testing.T) {}'
file big/b_darwin_test.go 'package big
import "testing"
func TestBigMac(t *testing.T) {}'

cat >"$tmp/list" <<'EOF'
# a comment
darwin listed           # all of it
darwin big ^TestBigOS$
windows code -
EOF

# expect NAME GOOS WANT — os-tests.sh --list for GOOS prints WANT.
expect() {
	local got
	if ! got="$(cd "$mod" && OS_TESTS_LIST="$tmp/list" GOTOOLCHAIN=local "$script" --goos "$2" --list 2>&1)"; then
		printf 'FAIL  %s: os-tests.sh failed\n%s\n' "$1" "$got"
		failed=$((failed + 1))
		return
	fi
	if [ "$got" = "$3" ]; then
		printf 'ok    %s\n' "$1"
	else
		printf 'FAIL  %s:\n got: %s\nwant: %s\n' "$1" "$got" "$3"
		failed=$((failed + 1))
	fi
}

m=example.test/m
expect "macOS: other files run whole, tagged test files' tests, the list's packages and expressions" darwin \
	"$m/big	^(TestBigMac)\$|^TestBigOS\$
$m/code
$m/listed
$m/tagged	^(TestMac|TestUnixOne|TestUnixTwo)\$"
expect "Windows: a package the list rules out stays out, unix tests do not run" windows "$m/big"
expect "Linux: only the tests of tagged files" linux \
	"$m/tagged	^(TestUnixOne|TestUnixTwo)\$"

# A package the list names that does not exist is an error, not a no-op.
printf 'darwin gone\n' >"$tmp/bad"
if (cd "$mod" && OS_TESTS_LIST="$tmp/bad" GOTOOLCHAIN=local "$script" --goos darwin --list) >"$tmp/out" 2>&1; then
	printf 'FAIL  a package that is gone: the script succeeded\n'
	failed=$((failed + 1))
elif grep -q 'names gone for darwin' "$tmp/out"; then
	printf 'ok    a package that is gone fails the list\n'
else
	printf 'FAIL  a package that is gone: unexpected output\n%s\n' "$(cat "$tmp/out")"
	failed=$((failed + 1))
fi

if [ "$failed" -gt 0 ]; then
	echo "$failed case(s) failed"
	exit 1
fi
echo "all cases passed"
