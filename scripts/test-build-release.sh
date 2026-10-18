#!/usr/bin/env bash
#
# Tests for build-release.sh: it builds an archive with a stand-in go
# command, which writes a placeholder binary, so nothing here needs Go or
# a C compiler, and checks what the archive holds.
#
#   scripts/test-build-release.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failed=0
ok() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2"; failed=$((failed + 1)); }

# The stand-in go: `go build ... -o OUT PKG` writes a placeholder to OUT.
mkdir -p "$work/bin"
cat >"$work/bin/go" <<'GO'
#!/usr/bin/env bash
[ "$1" = build ] || exit 2
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then printf '#!/bin/sh\necho placeholder\n' >"$2"; chmod +x "$2"; exit 0; fi
  shift
done
exit 2
GO
chmod +x "$work/bin/go"

name="fridolin_v0.0.0-test_linux_amd64"
if ! out="$(PATH="$work/bin:$PATH" "$here/build-release.sh" v0.0.0-test linux amd64 "$work/dist" 2>&1)"; then
  fail "builds an archive" "$out"
  exit 1
fi
archive="$work/dist/$name.tar.gz"
if [ "$out" = "$archive" ] && [ -f "$archive" ]; then ok "builds an archive and names it"; else fail "builds an archive and names it" "$out"; fi

list="$(tar -tzf "$archive" | sed 's#/$##' | LC_ALL=C sort)"
for entry in fridolin LICENSE THIRD_PARTY_NOTICES README.md tools/transcribe.py tools/requirements-transcription.txt; do
  if printf '%s\n' "$list" | grep -qxF "$name/$entry"; then
    ok "the archive holds $entry"
  else
    fail "the archive holds $entry" "$list"
  fi
done
if printf '%s\n' "$list" | grep -q "^$name/tools/tests"; then fail "the archive leaves out the tools' tests" "$list"; else ok "the archive leaves out the tools' tests"; fi

mkdir -p "$work/x"
tar -xzf "$archive" -C "$work/x"
for f in LICENSE THIRD_PARTY_NOTICES; do
  if cmp -s "$repo/$f" "$work/x/$name/$f"; then ok "$f is the checkout's"; else fail "$f is the checkout's"; fi
done

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
