#!/usr/bin/env bash
#
# Writes THIRD_PARTY_NOTICES: the code a fridolin binary holds besides its
# own. That is every module the packages of a release build come from, with
# the licence each is under, where a release binary's source is (GPL-3.0
# and MPL-2.0 ask for that), then their licence texts, the Go standard
# library's included. Release archives ship it next to LICENSE
# (build-release.sh), and make check and CI fail while it is out of date.
#
#   scripts/third-party-notices.sh             # write THIRD_PARTY_NOTICES (make notices)
#   scripts/third-party-notices.sh --check     # fail unless it is current (make notices-check)
#   scripts/third-party-notices.sh --targets   # the platforms it covers
#
# A FILE after the option writes or checks that file instead. The modules
# are what `go list -deps` finds for each platform the release workflow
# builds, with cgo off as there; the texts come from the module cache, and
# go list fetches a module missing there. A licence it cannot name stops
# it, so that a new dependency's licence gets a look before a release ships
# it. GO names the go command (default: go).
#
# Exit status: 0 written or current, 1 out of date or a licence it cannot
# name, 2 usage or go failed.

set -euo pipefail

# The platforms of the build matrix in .github/workflows/release.yml;
# scripts/test-third-party-notices.sh fails when the two differ.
targets="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

usage="usage: $0 [--check] [FILE] | --targets"
die() { local code="$1"; shift; printf 'third-party-notices: %s\n' "$*" >&2; exit "$code"; }

check=0
case "${1:-}" in
  --check) check=1; shift ;;
  --targets) printf '%s\n' $targets; exit 0 ;;
  -h | --help) printf '%s\n' "$usage"; exit 0 ;;
  -*) die 2 "$usage" ;;
esac
[ $# -le 1 ] || die 2 "$usage"

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
file="${1:-$repo/THIRD_PARTY_NOTICES}"
go="${GO:-go}"
pkg=.
[ -d "$repo/cmd/fridolin" ] && pkg=./cmd/fridolin

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export LC_ALL=C # one sort order everywhere, or the check fails on another machine

# ── what the binary links ──────────────────────────────────────────────────

# One line per package from outside the main module: its module's path, its
# version (and what replaces it), the module's folder and the package's own.
# The standard library's packages have no module and print an empty line.
tmpl='{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}{{"\t"}}{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{"\t"}}{{$.Dir}}{{end}}{{end}}'
: >"$work/pkgs"
for t in $targets; do
  if ! (cd "$repo" && CGO_ENABLED=0 GOOS="${t%/*}" GOARCH="${t#*/}" "$go" list -deps -f "$tmpl" "$pkg") >>"$work/pkgs"; then
    die 2 "go list failed for $t"
  fi
done
# The standard library's licence is the Go distribution's LICENSE, at the
# top of GOROOT; Homebrew keeps it in the folder above.
goroot="$("$go" env GOROOT)" || die 2 "go env GOROOT failed"
std_licence=""
for f in "$goroot/LICENSE" "$goroot/../LICENSE"; do
  if [ -f "$f" ]; then std_licence="$f"; break; fi
done
[ -n "$std_licence" ] || die 2 "the Go standard library's licence is neither at $goroot/LICENSE nor in the folder above"

# licence_files DIR — the licence files right in DIR. A source file named
# so (license.go, licence_test.go) is code, not a licence text.
licence_files() {
  # A LICENSE-LOGO credits an image (modernc.org's modules have one) that
  # is no part of the binary.
  find "$1" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \) | sort |
    { grep -viE '\.(go|s|c|h|cc|cpp|m|py|js|mjs|ts|sh|json|ya?ml|toml|proto|html|css|tmpl|mod|sum)$|/LICEN[CS]E-LOGO$' || true; }
}

# A package's licence is the one nearest it: in its own folder, else in the
# first folder above it that has one, up to the module's. A module can so
# hold code under more than one licence. The NOTICE files next to a licence
# go with it, since Apache-2.0 asks for them to be passed on.
grep -v '^$' "$work/pkgs" | sort -u >"$work/pkgs.sorted"
: >"$work/files"
while IFS=$'\t' read -r mpath mversion mdir pdir; do
  d="$pdir"
  found=""
  while :; do
    found="$(licence_files "$d")"
    [ -n "$found" ] && break
    case "$d" in "$mdir"/*) d="$(dirname "$d")" ;; *) break ;; esac
  done
  if [ -z "$found" ]; then
    where="the module's folder"
    [ "$pdir" = "$mdir" ] || where="${pdir#"$mdir"/} or a folder above it"
    die 1 "no licence file for $mpath $mversion: none in $where"
  fi
  found="$found"$'\n'"$(find "$d" -maxdepth 1 -type f -iname 'NOTICE*' | sort)"
  printf '%s\n' "$found" | while IFS= read -r f; do
    if [ -n "$f" ]; then printf '%s\t%s\t%s\t%s\n' "$mpath" "$mversion" "$mdir" "${f#"$mdir"/}"; fi
  done
done <"$work/pkgs.sorted" >"$work/files"
sort -u -o "$work/files" "$work/files"

# ── naming the licences ────────────────────────────────────────────────────

# licence_of FILE — the SPDX id of the licence in FILE, from phrases each
# licence's text has, on its words with line breaks and spacing evened out;
# nothing when it is none of these. The copyleft ones come first, since
# their texts quote others. An LGPL or AGPL text is left unnamed on
# purpose: what it means for the binary needs a look first.
licence_of() {
  local t
  t="$(tr -s '[:space:]' ' ' <"$1")"
  case "$t" in
    *"GNU LESSER GENERAL PUBLIC LICENSE"* | *"GNU AFFERO GENERAL PUBLIC LICENSE"*) ;;
    *"GNU GENERAL PUBLIC LICENSE Version 3"*) echo GPL-3.0 ;;
    *"Mozilla Public License Version 2.0"* | *"Mozilla Public License, version 2.0"*) echo MPL-2.0 ;;
    *"Apache License"*"Version 2.0"*) echo Apache-2.0 ;;
    *"Permission is hereby granted, free of charge, to any person obtaining a copy"*) echo MIT ;;
    *"Permission to use, copy, modify, and/or distribute this software for any purpose"* | \
      *"Permission to use, copy, modify, and distribute this software for any purpose with or without fee"*) echo ISC ;;
    *"Redistribution and use in source and binary forms"*"Neither the name"*) echo BSD-3-Clause ;;
    *"Redistribution and use in source and binary forms"*"may be used to endorse or promote"*) echo BSD-3-Clause ;;
    *"Redistribution and use in source and binary forms"*) echo BSD-2-Clause ;;
    *"SQLite Is Public Domain"*) echo blessing ;;
    *"Permission is granted to anyone to use this software for any purpose, including commercial applications, and to alter it and redistribute it freely"*) echo Zlib ;;
  esac
}

# modules: path, version, folder and licences (a comma-separated list), one
# line each, in path order.
: >"$work/modules"
cut -f1-3 "$work/files" | sort -u | while IFS=$'\t' read -r mpath mversion mdir; do
  ids=""
  while IFS=$'\t' read -r _ _ _ rel; do
    # A NOTICE, or an account of a module's third-party parts such as
    # modernc.org/sqlite's LICENSE-3RD-PARTY.md, goes with the licences
    # without being one.
    case "${rel##*/}" in [Nn][Oo][Tt][Ii][Cc][Ee]* | LICENSE-3RD-PARTY*) continue ;; esac
    id="$(licence_of "$mdir/$rel")"
    [ -n "$id" ] || die 1 "cannot name the licence in $rel of $mpath $mversion; read it, and add it to licence_of in $0 once you know what it means for the binary"
    ids="$ids"$'\n'"$id"
  done < <(awk -F'\t' -v p="$mpath" -v v="$mversion" '$1 == p && $2 == v' "$work/files")
  printf '%s\t%s\t%s\t%s\n' "$mpath" "$mversion" "$mdir" "$(printf '%s\n' "$ids" | grep -v '^$' | sort -u | paste -sd, - | sed 's/,/, /g')"
done >"$work/modules"
std_id="$(licence_of "$std_licence")"
[ -n "$std_id" ] || die 1 "cannot name the licence of the Go standard library ($std_licence)"

# ── the file ───────────────────────────────────────────────────────────────

# para TEXT — TEXT as one paragraph wrapped at 72 columns.
para() { printf '%s\n' "$1" | fold -s -w 72 | sed 's/ *$//'; }

# join_and — the lines on stdin as "a, b and c".
join_and() {
  awk '{ a[NR] = $0 } END { for (i = 1; i <= NR; i++) printf "%s%s", a[i], (i == NR ? "" : (i == NR - 1 ? " and " : ", ")) }'
}

# list_of ID — the modules under licence ID, as "a, b and c".
list_of() {
  awk -F'\t' -v id="$1" '{ n = split($4, l, ", "); for (i = 1; i <= n; i++) if (l[i] == id) print $1 }' "$work/modules" | join_and
}

rule="=============================================================================="
{
  printf 'THIRD-PARTY NOTICES\n\n'
  para "Fridolin's own source code is under the MIT licence in LICENSE. A fridolin binary built from it also holds the Go standard library and the Go modules listed below, each under its own licence, whose full text follows the list."
  gpl="$(list_of GPL-3.0)" mpl="$(list_of MPL-2.0)"
  if [ -n "$gpl" ]; then
    printf '\n'
    para "$gpl is under GPL-3.0, so a binary as a whole may be distributed only under the terms of GPL-3.0, which include making its complete source code available.${mpl:+ The files of $mpl stay under MPL-2.0.}"
  elif [ -n "$mpl" ]; then
    printf '\n'
    para "The files of $mpl are under MPL-2.0, which asks whoever distributes a binary to make their source code available."
  fi
  if [ -n "$gpl$mpl" ]; then
    printf '\n'
    para "The Corresponding Source of a release binary is the source of that release's tag of Fridolin's repository, which the GitHub release page that holds the binary's archive also offers as \"Source code\", with the versions of the modules listed below: \`go mod download\` in that source fetches each of them from the Go module proxy (proxy.golang.org) or from the module's own repository."
  fi
  printf '\n'
  para "This file is generated by scripts/third-party-notices.sh (make notices) from what the release builds for $(printf '%s\n' $targets | join_and) link; make check fails while it is out of date. Do not edit it by hand."
  printf '\nModules\n-------\n\n'
  printf 'Go standard library (%s)\n' "$std_id"
  awk -F'\t' '{ printf "%s %s (%s)\n", $1, $2, $4 }' "$work/modules"

  # section TITLE FILE — a licence text under a heading. Its CRs go: git
  # keeps this file with LF line ends (.gitattributes), so a text with
  # CRLF ones copied as it is would never match the file checked out.
  section() {
    printf '\n\n%s\n%s\n%s\n\n' "$rule" "$1" "$rule"
    tr -d '\r' <"$2" >"$work/text"
    cat "$work/text"
    # A text without a final line break still ends its line.
    [ -z "$(tail -c 1 "$work/text")" ] || printf '\n'
  }
  section "Go standard library: LICENSE" "$std_licence"
  while IFS=$'\t' read -r mpath mversion mdir rel; do
    section "$mpath $mversion: $rel" "$mdir/$rel"
  done <"$work/files"
} >"$work/out"

if [ "$check" = 1 ]; then
  if [ -f "$file" ] && cmp -s "$work/out" "$file"; then
    exit 0
  fi
  printf '%s is out of date with the modules the binary links. Run make notices and commit it.\n\n' "${file#"$repo"/}" >&2
  if [ -f "$file" ]; then
    diff -u "$file" "$work/out" | head -n 60 >&2 || true
  else
    printf '(there is no %s)\n' "$file" >&2
  fi
  exit 1
fi
cp "$work/out" "$file"
