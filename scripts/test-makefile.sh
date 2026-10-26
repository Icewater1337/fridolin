#!/usr/bin/env bash
#
# Tests for the Makefile, read from what `make -n` would run, and from
# fuzz-short run with a fake go, so nothing is built. Only build and
# install may write the binary: running the checks on a branch must not
# replace a build someone runs from the checkout, such as ./fridolin,
# which a service set up by an older setup.sh still starts on its next
# restart.
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
# The main package is in cmd/fridolin, and the root has no Go files.
expect "build builds cmd/fridolin" has build "-o \"$bin\" \./cmd/fridolin$"
expect "smoke builds cmd/fridolin" has smoke ' \./cmd/fridolin; '

# fridolin version names the build: what it is given (setup.sh passes
# its own), else what git describe says of the checkout.
expect "build stamps the version it is given" has build '-X github.com/Icewater1337/fridolin/internal/version\.version=v0\.0\.0-sentinel"' VERSION=v0.0.0-sentinel
described="$(git -C "$root" describe --tags --always --dirty --abbrev=8 2>/dev/null || echo dev)"
expect "build stamps git describe by default" has build "-X github.com/Icewater1337/fridolin/internal/version\\.version=$described\""
# The linker skips an -X it has no variable for: smoke.sh checks that the
# stamp reaches the binary.
expect "smoke stamps the version it is given" has smoke 'build -race -ldflags "-X github.com/Icewater1337/fridolin/internal/version\.version=v0\.0\.0-sentinel"' VERSION=v0.0.0-sentinel
expect "smoke has smoke.sh check the stamp" has smoke 'scripts/smoke\.sh "\$tmp/fridolin" "v0\.0\.0-sentinel"$' VERSION=v0.0.0-sentinel

# fuzz-short runs, with a fake go in place of the real one: nothing is
# built or fuzzed.
fake="$(mktemp -d)"
trap 'rm -rf "$fake"' EXIT
cat >"$fake/go" <<'EOF'
#!/usr/bin/env bash
# go as fuzz-short calls it, for the packages a and b, which have fuzz
# targets, and none, which has not. FAKE is the folder the packages are
# in; the target FAKE_FAIL names fails.
case "$1 $2" in
"list ./...") printf 'example.com/m/%s\n' a b none ;;
"list -f") echo "$FAKE/${4##*/}" ;;
"test -list")
  case "$4" in
  */a) printf 'FuzzOne\nFuzzTwo\n' ;;
  */b) echo FuzzThree ;;
  esac
  printf 'ok  \t%s\t0.01s\n' "$4" ;;
"test -run")
  echo "$*" >>"$FAKE/calls"
  [ "$5" != "^${FAKE_FAIL:-}\$" ] ;;
*) echo "fake go: unexpected $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$fake/go"

# fuzz NAME [VAR=VALUE...] — runs fuzz-short with the fake go and its
# packages in $fake/NAME, outside GitHub Actions unless the variables say
# otherwise; NAME.out gets the output and NAME.status the exit status.
fuzz() {
  local name="$1" status=0
  shift
  mkdir "$fake/$name"
  env -u GITHUB_ACTIONS FAKE="$fake/$name" "$@" \
    make -C "$root" -s fuzz-short GO="$fake/go" FUZZTIME=3s >"$fake/$name.out" 2>&1 || status=$?
  echo "$status" >"$fake/$name.status"
}

# fuzz_case NAME WHAT COMMAND... — passes when COMMAND does; else shows
# what the run NAME printed, indented so that GitHub Actions reads no
# command in it.
fuzz_case() {
  local name="$1" what="$2"
  shift 2
  if "$@"; then
    printf 'ok    fuzz-short %s\n' "$what"
  else
    printf 'FAIL  fuzz-short %s\n' "$what"
    sed 's/^/    /' "$fake/$name.out"
    failed=$((failed + 1))
  fi
}
lacks() { ! grep -q -- "$1" "$2"; }

fuzz fail FAKE_FAIL=FuzzOne
fuzz_case fail "fails when a target fails" [ "$(cat "$fake/fail.status")" -ne 0 ]
fuzz_case fail "goes on past a target that fails" [ "$(grep -c . "$fake/fail/calls")" -eq 3 ]
fuzz_case fail "names the targets that failed" grep -qx 'fuzzing found a failing input for: FuzzOne' "$fake/fail.out"
fuzz_case fail "makes testdata in each package with fuzz targets" test -d "$fake/fail/a/testdata" -a -d "$fake/fail/b/testdata"
fuzz_case fail "leaves a package without fuzz targets alone" test ! -e "$fake/fail/none"
fuzz_case fail "fuzzes each target for FUZZTIME" [ "$(grep -c -- ' -fuzztime 3s ' "$fake/fail/calls")" -eq 3 ]
fuzz_case fail "keeps minimizing a failing input short" [ "$(grep -c -- ' -fuzzminimizetime 5s ' "$fake/fail/calls")" -eq 3 ]
fuzz_case fail "groups nothing outside GitHub Actions" lacks '^::' "$fake/fail.out"

fuzz actions FAKE_FAIL=FuzzOne GITHUB_ACTIONS=true
fuzz_case actions "groups each target in GitHub Actions" [ "$(grep -c '^::group::Fuzz' "$fake/actions.out")" -eq 3 ]
fuzz_case actions "makes the failing targets an error in GitHub Actions" \
  grep -qx '::error::fuzzing found a failing input for: FuzzOne' "$fake/actions.out"

fuzz pass
fuzz_case pass "passes when every target passes" [ "$(cat "$fake/pass.status")" -eq 0 ]

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
