#!/usr/bin/env bash
#
# Tests for third-party-notices.sh. A stand-in go command (GO) lists made-up
# modules in a temp dir, each with a licence file, so nothing here needs Go,
# the module cache or the network; make notices-check runs the script on the
# real build.
#
#   scripts/test-third-party-notices.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/third-party-notices.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failed=0
out=""

# The stand-in go: `go env GOROOT` names a made-up Go, and `go list` prints
# the lines in $work/list, plus those in $work/list.GOOS-GOARCH for the
# platform it is asked about, as the script's template would.
mkdir -p "$work/bin" "$work/goroot"
cat >"$work/bin/go" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  env) printf '%s\n' "$FAKE_GOROOT" ;;
  list)
    cat "$FAKE_WORK/list"
    if [ -f "$FAKE_WORK/list.$GOOS-$GOARCH" ]; then cat "$FAKE_WORK/list.$GOOS-$GOARCH"; fi
    ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$work/bin/go"
export GO="$work/bin/go" FAKE_GOROOT="$work/goroot" FAKE_WORK="$work"
printf 'Copyright 2009 The Go Authors.\n\nRedistribution and use in source and binary forms, with or without\nmodification, are permitted. Neither the name of Google LLC nor the names\nof its contributors may be used.\n' >"$work/goroot/LICENSE"

# module PATH FILE TEXT — a module folder holding FILE (a path in it) with
# TEXT; prints the folder.
module() {
  local dir="$work/mod/$1@v1.0.0"
  mkdir -p "$(dirname "$dir/$2")"
  printf '%s\n' "$3" >"$dir/$2"
  printf '%s\n' "$dir"
}

# pkg PATH [SUBDIR] — the list line of a package of module PATH, in SUBDIR
# of it.
pkg() {
  local dir="$work/mod/$1@v1.0.0"
  printf '%s\tv1.0.0\t%s\t%s\n' "$1" "$dir" "$dir${2:+/$2}"
}

# The phrases the script names each licence by, wrapped differently from
# the real texts: it must not depend on where the lines break.
module example.com/mit LICENSE "MIT License

Copyright (c) 2020 Someone

Permission is hereby granted, free of charge, to any person
obtaining a copy of this software, to deal in it without restriction." >/dev/null
module example.com/bsd3 LICENSE "Copyright (c) 2018 Someone. Redistribution and use in
source and binary forms are permitted. Neither the name of Someone nor the
names of its contributors may be used to endorse products." >/dev/null
module example.com/bsd2 LICENSE "Copyright (c) 2018 Someone. Redistribution and use in source and binary forms, with or without modification, are permitted." >/dev/null
apache="$(module example.com/apache LICENSE "                                 Apache License
                           Version 2.0, January 2004")"
printf 'Apache example\nCopyright 2024 The Apache example authors\n' >"$apache/NOTICE"
module example.com/mpl LICENSE "Mozilla Public License Version 2.0
==================================" >/dev/null
module example.com/gpl COPYING "                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007" >/dev/null
module example.com/isc LICENSE.txt "Copyright (c) 2025 Someone

Permission to use, copy, modify, and distribute this software for any purpose
with or without fee is hereby granted." >/dev/null
# A module whose subfolder holds code under a licence of its own.
module example.com/nested LICENSE "Permission is hereby granted, free of charge, to any person obtaining a copy" >/dev/null
module example.com/nested sub/LICENSE "Redistribution and use in source and binary forms. Neither the name of X." >/dev/null
mkdir -p "$work/mod/example.com/nested@v1.0.0/sub/inner"
module example.com/linuxonly LICENSE "Permission is hereby granted, free of charge, to any person obtaining a copy" >/dev/null
# Go code in a file named like a licence, beside the real one.
codefile="$(module example.com/codefile LICENSE "Permission is hereby granted, free of charge, to any person obtaining a copy")"
printf 'package codefile\n\n// License names no licence.\nconst License = "see LICENSE"\n' >"$codefile/license.go"
# A licence text with CRLF line ends, which git would turn into LF ones.
module example.com/crlf LICENSE.txt "$(printf 'Copyright (c) 2020 Someone\r\n\r\nPermission is hereby granted, free of charge, to any person obtaining a copy\r')" >/dev/null

{
  pkg example.com/mit
  pkg example.com/mit internal
  printf '\n' # a standard-library package
  pkg example.com/bsd3
  pkg example.com/bsd2
  pkg example.com/apache
  pkg example.com/mpl
  pkg example.com/gpl
  pkg example.com/isc
  pkg example.com/nested
  pkg example.com/nested sub/inner
  pkg example.com/codefile
  pkg example.com/crlf
} >"$work/list"
mkdir -p "$work/mod/example.com/mit@v1.0.0/internal"
pkg example.com/linuxonly >"$work/list.linux-arm64"

# run ARGS... — the script with ARGS; its status in $status, its output in $out.
status=0
run() {
  status=0
  out="$("$script" "$@" 2>&1)" || status=$?
}

ok() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2"; failed=$((failed + 1)); }

# expect_status NAME WANT — the last run exited with WANT.
expect_status() {
  if [ "$status" = "$2" ]; then ok "$1"; else fail "$1: exit $status, want $2" "$out"; fi
}

# expect_line NAME FILE LINE — FILE has LINE, whole.
expect_line() {
  if grep -qxF -- "$3" "$2"; then ok "$1"; else fail "$1: no line \"$3\" in $2"; fi
}

# expect_in NAME TEXT — the last run's output names TEXT.
expect_in() {
  case "$out" in
    *"$2"*) ok "$1" ;;
    *) fail "$1: no \"$2\" in" "$out" ;;
  esac
}

notices="$work/NOTICES"
run "$notices"
expect_status "writes the notices" 0
for line in \
  "Go standard library (BSD-3-Clause)" \
  "example.com/mit v1.0.0 (MIT)" \
  "example.com/bsd3 v1.0.0 (BSD-3-Clause)" \
  "example.com/bsd2 v1.0.0 (BSD-2-Clause)" \
  "example.com/apache v1.0.0 (Apache-2.0)" \
  "example.com/mpl v1.0.0 (MPL-2.0)" \
  "example.com/gpl v1.0.0 (GPL-3.0)" \
  "example.com/isc v1.0.0 (ISC)"; do
  expect_line "names the licence: $line" "$notices" "$line"
done
expect_line "a module only one platform links is listed" "$notices" "example.com/linuxonly v1.0.0 (MIT)"
expect_line "a package under a licence of its own adds it to its module's" "$notices" "example.com/nested v1.0.0 (BSD-3-Clause, MIT)"
expect_line "the nested licence's text is there" "$notices" "example.com/nested v1.0.0: sub/LICENSE"
expect_line "Apache's NOTICE is passed on" "$notices" "Copyright 2024 The Apache example authors"
expect_line "the GPL's text is there" "$notices" "                       Version 3, 29 June 2007"
expect_line "the MPL's text is there" "$notices" "Mozilla Public License Version 2.0"
expect_line "the Go standard library's text is there" "$notices" "Copyright 2009 The Go Authors."
expect_line "says the GPL module makes the binary GPL" "$notices" "example.com/gpl is under GPL-3.0, so a binary as a whole may be"
if grep -qF "The Corresponding Source of a release binary is the source of that" "$notices" &&
  grep -qF "go mod download" "$notices"; then
  ok "says where a release binary's source is"
else
  fail "says where a release binary's source is" "$(sed -n '1,30p' "$notices")"
fi
expect_line "a Go file named license.go is no licence" "$notices" "example.com/codefile v1.0.0 (MIT)"
if grep -qF "example.com/codefile v1.0.0: license.go" "$notices"; then
  fail "copies no Go file as a licence text"
else
  ok "copies no Go file as a licence text"
fi
expect_line "a CRLF licence is named" "$notices" "example.com/crlf v1.0.0 (MIT)"
if grep -q "$(printf '\r')" "$notices"; then
  fail "a CRLF licence text is copied with LF line ends" "$(grep -n "$(printf '\r')" "$notices" | cat -v)"
else
  ok "a CRLF licence text is copied with LF line ends"
fi
if [ "$(grep -c '^example\.com/mit v1\.0\.0 ' "$notices")" = 1 ]; then
  ok "a module is listed once, however many of its packages are linked"
else
  fail "a module is listed once, however many of its packages are linked" "$(grep '^example\.com/mit' "$notices")"
fi
if grep -qF "$work" "$notices"; then
  fail "names no folder of this computer" "$(grep -nF "$work" "$notices")"
else
  ok "names no folder of this computer"
fi

run --check "$notices"
expect_status "the notices it just wrote are current" 0
cp "$notices" "$work/before"

module example.com/added LICENSE "Permission is hereby granted, free of charge, to any person obtaining a copy" >/dev/null
cp "$work/list" "$work/list.saved"
pkg example.com/added >>"$work/list"
run --check "$notices"
expect_status "a module the binary now links makes them stale" 1
expect_in "says what to run" "Run make notices"
expect_in "shows the module that is missing" "+example.com/added v1.0.0 (MIT)"
if cmp -s "$notices" "$work/before"; then ok "--check writes nothing"; else fail "--check writes nothing"; fi

awk -F'\t' -v OFS='\t' '$1 == "example.com/mit" { $2 = "v1.1.0" } 1' "$work/list.saved" >"$work/list"
run --check "$notices"
expect_status "a module at another version makes them stale" 1
cp "$work/list.saved" "$work/list"
run --check "$notices"
expect_status "back as they were, they are current again" 0

mpl="$work/mod/example.com/mpl@v1.0.0/LICENSE"
cp "$mpl" "$work/mpl.saved"
printf 'changed\n' >>"$mpl"
run --check "$notices"
expect_status "a changed licence text makes them stale" 1
mv "$work/mpl.saved" "$mpl"

run --check "$work/none"
expect_status "no notices at all are stale" 1

# Licences it must not name: an LGPL, an AGPL, something else, or none.
i=0
for planted in \
  "an LGPL|GNU LESSER GENERAL PUBLIC LICENSE Version 3, 29 June 2007" \
  "an AGPL|GNU AFFERO GENERAL PUBLIC LICENSE Version 3, 19 November 2007" \
  "a licence it does not know|All rights reserved. Do not copy."; do
  i=$((i + 1))
  name="${planted%%|*}"
  module "example.com/odd$i" LICENSE "${planted#*|}" >/dev/null
  cp "$work/list.saved" "$work/list"
  pkg "example.com/odd$i" >>"$work/list"
  run "$work/odd"
  expect_status "stops at $name" 1
  expect_in "names the module with $name" "cannot name the licence in LICENSE of example.com/odd$i v1.0.0"
done
mkdir -p "$work/mod/example.com/bare@v1.0.0"
cp "$work/list.saved" "$work/list"
pkg example.com/bare >>"$work/list"
run "$work/odd"
expect_status "stops at a module without a licence file" 1
expect_in "names the module without one" "no licence file for example.com/bare v1.0.0"
mv "$work/list.saved" "$work/list"
if [ -e "$work/odd" ]; then fail "writes nothing when it stops"; else ok "writes nothing when it stops"; fi

# The platforms it covers are those the release workflow builds.
release="$here/../.github/workflows/release.yml"
want="$(sed -n 's/.*goos: *\([a-z0-9]*\), *goarch: *\([a-z0-9]*\).*/\1\/\2/p' "$release" | LC_ALL=C sort)"
got="$("$script" --targets | LC_ALL=C sort)"
if [ -n "$want" ] && [ "$got" = "$want" ]; then
  ok "covers the platforms release.yml builds"
else
  fail "covers the platforms release.yml builds" "it covers: $(printf '%s ' $got)
release.yml builds: $(printf '%s ' $want)"
fi

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
