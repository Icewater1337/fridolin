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

# A branch already on the remote, rebased onto a main that moved meanwhile
# and pushed again. main's new commit stands for GitHub's merge commit: it
# is on the remote already, under another identity and with a message that
# names the owner, so it is not this push's to publish.
remote="$work/remote.git"
git init -q --bare "$remote"
clone="$work/clone"
new_repo "$clone"
git -C "$clone" remote add origin "$remote"
commit "$clone" README "hello" >/dev/null
git -C "$clone" push -q origin main
git -C "$clone" switch -q -c feature
old_tip="$(commit "$clone" feature.txt "a feature")"
git -C "$clone" push -q origin feature
git -C "$clone" switch -q main
commit "$clone" site.txt "the site" >/dev/null
GIT_COMMITTER_NAME="Merge Bot" GIT_COMMITTER_EMAIL=merge-bot@localhost \
  git -C "$clone" commit -q --allow-empty -m "Merge pull request #1 from $secret/site"
git -C "$clone" push -q origin main
git -C "$clone" switch -q feature
git -C "$clone" rebase -q main
new_tip="$(git -C "$clone" rev-parse HEAD)"
expect "pre-push after a rebase skips the commits a remote already has" 0 "$clone" --pre-push \
  <<<"refs/heads/feature $new_tip refs/heads/feature $old_tip"
leak_tip="$(commit "$clone" plan.txt "ask $secret")"
expect "pre-push after a rebase still scans the new commits" 1 "$clone" --pre-push \
  <<<"refs/heads/feature $leak_tip refs/heads/feature $old_tip"
expect_output "pre-push after a rebase names the new commit" "$(git -C "$clone" rev-parse --short "$leak_tip"):plan.txt"

expect "--all-history of a clean HEAD passes" 0 "$repo" --all-history
git -C "$repo" switch -q leaky
expect "--all-history finds the string at HEAD" 1 "$repo" --all-history
expect_output "--all-history names HEAD and the file" "HEAD:notes.txt:1"
git -C "$repo" switch -q main

# --staged, the pre-commit hook's mode: the change about to be committed.
# stage FILE TEXT — a new repository, $staged, with FILE holding TEXT
# staged in it.
cases=0
stage() {
  cases=$((cases + 1))
  staged="$work/staged-$cases"
  new_repo "$staged"
  printf '%s\n' "$2" >"$staged/$1"
  git -C "$staged" add "$1"
}
stage notes.txt "nothing to see"
expect "--staged of a clean change passes" 0 "$staged" --staged
stage notes.txt "call $secret tomorrow"
expect "--staged finds the denylisted string" 1 "$staged" --staged
expect_output "--staged names the staged file" "staged:notes.txt"
stage notes.txt "15551234567@s.whatsapp.net 120363000000000000@g.us you@example.com /Users/you"
expect "--staged passes the documented placeholders" 0 "$staged" --staged

# One case per generic pattern. Each value is written here with a # that
# keeps the pattern from matching this file, which the check scans too;
# the planted value is without it. The check must name the file and print
# the value masked: its first three characters and its length, never all
# of it, since CI logs are public.
for planted in \
  "a phone number|+49#1701234567" \
  "a phone number in a JSON string|\"49#1701234567\"" \
  "a WhatsApp JID|491701234567#@s.whatsapp.net" \
  "a group JID|120363123456789012#@g.us" \
  "a LID|123456789012345#@lid" \
  "a GitHub token|ghp#_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8" \
  "a fine-grained GitHub token|github#_pat_11ABCDEFG0123456789abcdefghij" \
  "an Anthropic key|sk#-ant-api03-AbCdEfGh12345678" \
  "an email address|jane.doe#@company.test" \
  "a macOS home path|/Use#rs/jane" \
  "a Linux home path|/ho#me/jane"; do
  name="${planted%%|*}"
  value="$(printf '%s' "${planted#*|}" | tr -d '#')"
  stage leak.txt "note: $value"
  expect "--staged catches $name" 1 "$staged" --staged
  expect_output "--staged names the file with $name" "staged:leak.txt"
  expect_output "--staged masks $name" "${value:0:3}…(${#value} chars)"
  case "$out" in
    *"$value"*) printf 'FAIL  --staged prints %s whole:\n%s\n' "$name" "$out"; failed=$((failed + 1)) ;;
    *) printf 'ok    --staged does not print %s whole\n' "$name" ;;
  esac
done

if [ "$failed" -gt 0 ]; then
  printf '\n%d case(s) failed\n' "$failed" >&2
  exit 1
fi
printf '\nall cases passed\n'
