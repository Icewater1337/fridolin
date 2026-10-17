#!/usr/bin/env bash
#
# Smoke test for a built binary: start it in dev mode (no WhatsApp) with a
# scratch HOME, config and data dir on a free port, check that it answers,
# then stop it with SIGTERM and check that it shuts down cleanly. It never
# reads your config or data dir and never talks to a running bridge.
#
#   scripts/smoke.sh [BINARY]      # default ./fridolin; `make smoke` builds it
#
# Exit status: 0 when every step passed, 1 when one failed, 2 on bad usage.

set -euo pipefail

bin="${1:-./fridolin}"
[ -x "$bin" ] || { printf 'smoke: no executable at %s (run make build)\n' "$bin" >&2; exit 2; }
bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"
command -v curl >/dev/null 2>&1 || { printf 'smoke: curl is needed\n' >&2; exit 2; }

work="$(mktemp -d)"
pid=""
cleanup() {
  if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

home="$work/home"
data="$home/.fridolin"
cfg="$data/config.json"
log="$work/bridge.log"
mkdir -p "$data"

pass() { printf 'ok    %s\n' "$1"; }
fail() {
  printf 'FAIL  %s\n' "$1"
  if [ -s "$log" ]; then printf -- '--- last lines of the bridge log:\n'; tail -n 40 "$log"; fi
  exit 1
}

# Run the binary with nothing from your environment: no config override, no
# data dir, no PATH entries beyond the usual ones. env execs it, so in the
# background $! is the bridge itself.
clean_env=(env -i PATH="/usr/bin:/bin:/usr/sbin:/sbin" HOME="$home" TMPDIR="$work")

# A port nothing listens on. curl exits 7 when the connection is refused.
port=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  candidate=$((20000 + RANDOM % 40000))
  rc=0
  curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$candidate/" || rc=$?
  if [ "$rc" = 7 ]; then port="$candidate"; break; fi
done
[ -n "$port" ] || fail "find a free port"
base="http://127.0.0.1:$port"

cat >"$cfg" <<EOF
{
  "data_dir": "$data",
  "http_listen": "127.0.0.1:$port",
  "allowed_numbers": [],
  "allow_any_dm": false,
  "sandbox": {"enabled": false}
}
EOF

version="$("${clean_env[@]}" "$bin" version)" || fail "fridolin version"
[ -n "$version" ] || fail "fridolin version prints a version"
pass "fridolin version ($version)"

"${clean_env[@]}" "$bin" check --config "$cfg" >"$work/check.txt" 2>&1 || { cat "$work/check.txt"; fail "fridolin check"; }
grep -q '^Config OK' "$work/check.txt" || { cat "$work/check.txt"; fail "fridolin check says Config OK"; }
pass "fridolin check"

"${clean_env[@]}" "$bin" --no-whatsapp --config "$cfg" >"$log" 2>&1 &
pid=$!
health=""
for _ in $(seq 1 100); do
  health="$(curl -s --max-time 2 "$base/health" || true)"
  [ "$health" = ok ] && break
  kill -0 "$pid" 2>/dev/null || fail "the bridge keeps running after start"
  sleep 0.2
done
[ "$health" = ok ] || fail "GET /health says ok"
pass "GET /health"

code="$(curl -s -o "$work/index.html" -w '%{http_code}' "$base/")"
if [ "$code" != 200 ] || ! grep -q '<html' "$work/index.html"; then fail "GET / serves the dashboard (status $code)"; fi
pass "GET / serves the dashboard"

code="$(curl -s -o /dev/null -w '%{http_code}' "$base/api/status")"
[ "$code" = 401 ] || fail "the API refuses a request without the token (status $code)"
pass "the API refuses a request without the token"

kill -TERM "$pid"
status=""
for _ in $(seq 1 100); do
  if ! kill -0 "$pid" 2>/dev/null; then
    status=0
    wait "$pid" || status=$?
    break
  fi
  sleep 0.2
done
[ -n "$status" ] || fail "the bridge stops within 20 s of SIGTERM"
pid=""
[ "$status" = 0 ] || fail "the bridge exits 0 on SIGTERM (exit $status)"
pass "clean shutdown on SIGTERM"

if grep -q -e 'panic:' -e 'DATA RACE' "$log"; then fail "no panic or data race in the log"; fi
pass "no panic or data race in the log"
printf '\nsmoke test passed\n'
