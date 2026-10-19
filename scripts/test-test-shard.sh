#!/usr/bin/env bash
# Exercise package-aware sharding without building the bridge or running Go.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export SHARD_TEST_LOG="$tmp/selected"
export SHARD_TEST_CASE=normal
export GO="$tmp/go"

# Simulate go list/test, including names shared by packages, empty packages,
# build failures and failing tests. Selection uses the script's actual -run
# expression; the expected assignments below do not duplicate its algorithm.
cat > "$GO" <<'GO'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = list ]; then
  printf '%s\n' example.test/b example.test/empty example.test/a
  exit 0
fi
[ "$1" = test ] || exit 90
shift
mode="" pattern="" pkg="" race=no count=no
while [ $# -gt 0 ]; do
  case "$1" in
    -list|-run) mode="$1"; pattern="$2"; shift ;;
    -race) race=yes ;;
    -count=1) count=yes ;;
    example.test/*) pkg="$1" ;;
    *) exit 91 ;;
  esac
  shift
done
[ "$race/$count" = yes/yes ] || exit 92
case "$pkg" in
  example.test/a) names=$'TestShared\nTestSolo\nFuzzRoundTrip\nExample_value' ;;
  example.test/b) names=$'TestShared\nFuzzParse\nExample_value' ;;
  example.test/empty) names="" ;;
  *) exit 93 ;;
esac
if [ "$SHARD_TEST_CASE" = empty ]; then names=""; fi
if [ "$mode" = -list ]; then
  if [ "$SHARD_TEST_CASE" = compile ] && [ "$pkg" = example.test/b ]; then exit 42; fi
  if [ "$SHARD_TEST_CASE" = empty-compile ] && [ "$pkg" = example.test/empty ]; then exit 42; fi
  printf '%s\n' "$names"
  printf 'ok\t%s\t0.001s\n' "$pkg"
  exit 0
fi
[ "$mode" = -run ] || exit 94
failed=0
while IFS= read -r name; do
  [ -n "$name" ] || continue
  if [[ $name =~ $pattern ]]; then
    printf '%s %s\n' "$pkg" "$name" >> "$SHARD_TEST_LOG"
    if [ "$SHARD_TEST_CASE" = fail ] && [ "$pkg $name" = 'example.test/a TestShared' ]; then failed=1; fi
  fi
done <<< "$names"
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

run() { "$root/scripts/test-shard.sh" "$@" -race -count=1 > "$tmp/output" 2>&1; }
for n in 1 3 10; do
  : > "$SHARD_TEST_LOG"
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
echo 'ok    invalid slice arguments fail with usage status'

for failure in compile empty-compile empty fail; do
  export SHARD_TEST_CASE="$failure"
  : > "$SHARD_TEST_LOG"
  if run 1 1; then echo "FAIL  $failure was silently accepted"; exit 1; fi
  if [ "$failure" = fail ]; then
    grep -q '^example.test/b TestShared$' "$SHARD_TEST_LOG"
  fi
  echo "ok    $failure fails the slice"
done
echo 'all shard cases passed'
