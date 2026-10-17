#!/usr/bin/env bash
#
# Tests for check-personal-data.sh. Each case runs in a throwaway repository
# with a throwaway denylist and HOME, so nothing here reads your denylist,
# your git config or this repository's history.
#
#   scripts/test-check-personal-data.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/check-personal-data.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Run git and the check as if outside any repository or hook.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY XDG_CONFIG_HOME
export HOME="$work/home" GIT_CONFIG_NOSYSTEM=1
mkdir -p "$HOME"

# A made-up denylist entry: no generic pattern matches it, so a hit can only
# come from the denylist, and this file never trips the check it tests.
secret="zebraquartz-not-a-real-name"
export PERSONAL_DENYLIST="$work/denylist"
printf '%s\n' "$secret" >"$PERSONAL_DENYLIST"

failed=0
out=""

# new_repo DIR — an empty repository whose identity the check accepts.
new_repo() {
  git init -q -b main "$1"
  git -C "$1" config user.name "Test"
  git -C "$1" config user.email "you@example.com"
  git -C "$1" config commit.gpgsign false
}

# commit DIR FILE TEXT — commit FILE with TEXT, printing the new commit.
commit() {
  printf '%s\n' "$3" >"$1/$2"
  git -C "$1" add "$2"
  git -C "$1" commit -q -m "Add $2"
  git -C "$1" rev-parse HEAD
}

# expect NAME STATUS DIR ARGS... — run the check in DIR with ARGS (and this
# function's stdin) and compare its exit status; the output stays in $out.
expect() {
  local name="$1" want="$2" dir="$3" got=0
  shift 3
  out="$(cd "$dir" && "$script" "$@" 2>&1)" || got=$?
  if [ "$got" = "$want" ]; then
    printf 'ok    %s\n' "$name"
  else
    printf 'FAIL  %s: exit %s, want %s\n%s\n' "$name" "$got" "$want" "$out"
    failed=$((failed + 1))
  fi
}

# expect_output NAME TEXT — the last check's output names TEXT.
expect_output() {
  case "$out" in
    *"$2"*) printf 'ok    %s\n' "$1" ;;
    *) printf 'FAIL  %s: no %s in\n%s\n' "$1" "$2" "$out"; failed=$((failed + 1)) ;;
  esac
}

# One repository: main is clean and checked out; "leaky" has a file with the
# denylisted string, committed before anyone pushed it.
repo="$work/repo"
new_repo "$repo"
base="$(commit "$repo" README "hello")"
git -C "$repo" switch -q -c leaky
leaky="$(commit "$repo" notes.txt "call $secret tomorrow")"
git -C "$repo" switch -q main
clean="$(commit "$repo" more.txt "nothing to see")"

# The remote already has leaky (rsha = lsha), so no new line is pushed: only
# the scan of the pushed commit's files can find the string.
expect "pre-push scans the pushed commit, not HEAD" 1 "$repo" --pre-push \
  <<<"refs/heads/leaky $leaky refs/heads/leaky $leaky"
expect_output "pre-push names the pushed commit and file" "$(git -C "$repo" rev-parse --short "$leaky"):notes.txt:1"

git -C "$repo" switch -q leaky
expect "pre-push of a clean branch passes while HEAD has the string" 0 "$repo" --pre-push \
  <<<"refs/heads/main $clean refs/heads/main $base"
expect "deleting a branch publishes nothing" 0 "$repo" --pre-push \
  <<<"(delete) 0000000000000000000000000000000000000000 refs/heads/old $base"
git -C "$repo" switch -q main

# leaky2 only adds a clean commit on top of leaky, so the range holds no new
# line with the string; its files still have it.
git -C "$repo" switch -q -c leaky2 leaky
commit "$repo" todo.txt "buy milk" >/dev/null
git -C "$repo" switch -q main
expect "--range scans the end of the range, not HEAD" 1 "$repo" --range "$leaky..leaky2"
expect_output "--range names the branch and file" "leaky2:notes.txt:1"
expect "--range of a clean branch passes" 0 "$repo" --range "$base..main"

expect "--all-history of a clean HEAD passes" 0 "$repo" --all-history
git -C "$repo" switch -q leaky
expect "--all-history finds the string at HEAD" 1 "$repo" --all-history
expect_output "--all-history names HEAD and the file" "HEAD:notes.txt:1"
git -C "$repo" switch -q main

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
