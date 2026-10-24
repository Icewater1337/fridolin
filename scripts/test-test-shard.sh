#!/usr/bin/env bash
# Exercise package-aware sharding without building the bridge or running Go.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export SHARD_TEST_LOG="$tmp/selected"
export SHARD_TEST_DIR="$tmp/state"
export SHARD_TEST_CASE=normal
export GO="$tmp/go"
mkdir -p "$SHARD_TEST_DIR/running"

# Simulate go list/test, including names shared by packages, empty packages,
# build failures and failing tests. -list takes every package at once, as go
# test does, and prints each one's names before its own ok line; -run takes
# one. Selection uses the script's actual -run expression; the expected
# assignments below do not duplicate its algorithm. Each run notes how many
# runs are going at once (most), and prints its output in two parts.
cat > "$GO" <<'GO'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = list ]; then
  printf '%s\n' example.test/b example.test/empty example.test/a
  exit 0
fi
[ "$1" = test ] || exit 90
shift
mode="" pattern="" pkgs="" race=no count=no
while [ $# -gt 0 ]; do
  case "$1" in
    -list|-run) mode="$1"; pattern="$2"; shift ;;
    -race) race=yes ;;
    -count=1) count=yes ;;
    example.test/*) pkgs="$pkgs $1" ;;
    *) exit 91 ;;
  esac
  shift
done
[ "$race/$count" = yes/yes ] || exit 92
names() {
  if [ "$SHARD_TEST_CASE" = empty ]; then return; fi
  case "$1" in
    example.test/a) printf '%s\n' TestShared TestSolo FuzzRoundTrip Example_value ;;
    example.test/b) printf '%s\n' TestShared FuzzParse Example_value ;;
    example.test/empty) ;;
    *) exit 93 ;;
  esac
}
if [ "$mode" = -list ]; then
  status=0
  for pkg in $pkgs; do
    if [ "$SHARD_TEST_CASE/$pkg" = compile/example.test/b ] || [ "$SHARD_TEST_CASE/$pkg" = empty-compile/example.test/empty ]; then
      echo "$pkg: syntax error" >&2
      printf 'FAIL\t%s [build failed]\n' "$pkg"
      status=1
      continue
    fi
    if [ "$pkg" = example.test/empty ]; then
      printf '?   \t%s\t[no test files]\n' "$pkg"
      continue
    fi
    names "$pkg"
    printf 'ok  \t%s\t0.001s\n' "$pkg"
  done
  exit "$status"
fi
[ "$mode" = -run ] || exit 94
set -- $pkgs
[ $# -eq 1 ] || exit 95
pkg="$1" mark="${1##*/}"
touch "$SHARD_TEST_DIR/running/$mark"
now="$(ls "$SHARD_TEST_DIR/running" | wc -l | tr -d ' ')"
echo "$now" >> "$SHARD_TEST_DIR/at-once"
echo "begin $pkg"
if [ "$SHARD_TEST_CASE" = together ]; then
  # Each package waits for the other to start: only runs side by side end.
  other=a
  [ "$mark" = b ] || other=b
  touch "$SHARD_TEST_DIR/started-$mark"
  for _ in $(seq 50); do
    [ ! -e "$SHARD_TEST_DIR/started-$other" ] || break
    sleep 0.1
  done
  [ -e "$SHARD_TEST_DIR/started-$other" ] || { echo "$pkg ran alone"; exit 1; }
else
  sleep 0.2
fi
failed=0
while IFS= read -r name; do
  [ -n "$name" ] || continue
  if [[ $name =~ $pattern ]]; then
    printf '%s %s\n' "$pkg" "$name" >> "$SHARD_TEST_LOG"
    if [ "$SHARD_TEST_CASE" = fail ] && [ "$pkg $name" = 'example.test/a TestShared' ]; then failed=1; fi
  fi
done <<< "$(names "$pkg")"
rm "$SHARD_TEST_DIR/running/$mark"
echo "end $pkg"
if [ "$failed" = 1 ]; then printf 'FAIL\t%s\t0.3s\n' "$pkg"; else printf 'ok  \t%s\t0.3s\n' "$pkg"; fi
exit "$failed"
GO
chmod +x "$GO"

cat > "$tmp/expected" <<'EXPECTED'
example.test/a Example_value
example.test/a FuzzRoundTrip
example.test/a TestShared
example.test/a TestSolo
example.test/b Example_value
example.test/b FuzzParse
example.test/b TestShared
EXPECTED

reset() { : > "$SHARD_TEST_LOG"; : > "$SHARD_TEST_DIR/at-once"; rm -f "$SHARD_TEST_DIR"/started-*; }
run() { "$root/scripts/test-shard.sh" "$@" -race -count=1 > "$tmp/output" 2>&1; }
for n in 1 3 10; do
  reset
  for ((i=1; i<=n; i++)); do
    run "$i" "$n" || { cat "$tmp/output"; exit 1; }
  done
  LC_ALL=C sort "$SHARD_TEST_LOG" > "$tmp/actual"
  diff -u "$tmp/expected" "$tmp/actual"
  echo "ok    $n slices run every package target exactly once"
done

for bad in '0 3' '4 3' 'x 3' '01 3'; do
  read -r i n <<< "$bad"
  status=0
  run "$i" "$n" || status=$?
  [ "$status" -eq 2 ] || { echo "FAIL  invalid arguments: $bad"; exit 1; }
done
for bad in 0 x 2x; do
  status=0
  SHARD_JOBS="$bad" run 1 1 || status=$?
  [ "$status" -eq 2 ] || { echo "FAIL  invalid SHARD_JOBS: $bad"; exit 1; }
done
echo 'ok    invalid slice arguments and SHARD_JOBS fail with usage status'

# Both packages of the slice run at the same time: each waits for the other.
reset
SHARD_TEST_CASE=together SHARD_JOBS=2 run 1 1 || { cat "$tmp/output"; echo 'FAIL  packages ran one at a time'; exit 1; }
echo 'ok    a slice runs its packages side by side'

# SHARD_JOBS bounds them, and the package with more names starts first.
reset
SHARD_JOBS=1 run 1 1 || { cat "$tmp/output"; exit 1; }
[ "$(LC_ALL=C sort -n "$SHARD_TEST_DIR/at-once" | tail -1)" = 1 ] || { echo 'FAIL  SHARD_JOBS=1 ran packages at once'; exit 1; }
grep -o '^example.test/[a-z]*: slice' "$tmp/output" | paste -sd ' ' - | grep -qx 'example.test/a: slice example.test/b: slice' ||
  { cat "$tmp/output"; echo 'FAIL  the package with the most names did not start first'; exit 1; }
echo 'ok    SHARD_JOBS bounds the packages at once, the most names first'

# Each package's output stays in one piece, even when they run at once.
reset
SHARD_JOBS=2 run 1 1 || { cat "$tmp/output"; exit 1; }
awk '
  /^begin / { if (open != "") bad = 1; open = $2 }
  /^end /   { if (open != $2) bad = 1; open = "" }
  END { exit bad }
' "$tmp/output" || { cat "$tmp/output"; echo 'FAIL  outputs of packages interleaved'; exit 1; }
[ "$(grep -c '^begin ' "$tmp/output")" = 2 ] || { cat "$tmp/output"; echo 'FAIL  a package output is missing'; exit 1; }
echo 'ok    each package output stays in one piece'

for failure in compile empty-compile empty fail; do
  export SHARD_TEST_CASE="$failure"
  reset
  if run 1 1; then echo "FAIL  $failure was silently accepted"; exit 1; fi
  case "$failure" in
    *compile)
      # The build failure is shown, and nothing runs.
      grep -q 'syntax error' "$tmp/output" || { cat "$tmp/output"; echo "FAIL  $failure: no build error shown"; exit 1; }
      [ ! -s "$SHARD_TEST_LOG" ] || { echo "FAIL  $failure: tests ran after a build failure"; exit 1; }
      ;;
    fail)
      # The other package still runs, and the end names the failed one.
      grep -q '^example.test/b TestShared$' "$SHARD_TEST_LOG"
      grep -q 'failed in: example.test/a$' "$tmp/output" || { cat "$tmp/output"; echo 'FAIL  the failed package is not named'; exit 1; }
      ;;
  esac
  echo "ok    $failure fails the slice"
done
echo 'all shard cases passed'
