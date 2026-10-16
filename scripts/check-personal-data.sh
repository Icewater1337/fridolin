#!/usr/bin/env bash
#
# Refuse to publish personal data.
#
# Scans what is about to be committed or pushed for things that should never
# reach a public repository: real phone numbers and WhatsApp JIDs, email
# addresses, home-directory paths, API tokens, private keys, databases — plus
# a local denylist of your own strings (names, numbers, company names) that no
# generic pattern could know about.
#
#   scripts/check-personal-data.sh --staged        # pre-commit: the staged diff
#   scripts/check-personal-data.sh --pre-push      # pre-push: reads the hook's stdin
#   scripts/check-personal-data.sh --range A..B    # commits in a range
#   scripts/check-personal-data.sh --all-history   # every commit reachable from HEAD (CI)
#
# Commits are scanned line by line as they were added, so something added in
# one commit and deleted in the next is still caught: deleting it does not
# remove it from history.
#
# The denylist is read from the first of:
#   $PERSONAL_DENYLIST
#   ~/.config/whatsapp-claude-bridge/personal-denylist
#   .personal-denylist           (gitignored; never commit it)
# One entry per line, matched case-insensitively. Lines starting with # are
# comments. Entries made only of digits also match with spaces, dashes, dots
# and brackets removed, so "+1 555 123 4567" is caught by "15551234567".
#
# A line containing "personal-data:ignore" is exempt from the generic patterns
# (never from the denylist). Use it for documented placeholders only.
#
# Exit status: 0 clean, 1 findings, 2 usage or git error.

set -euo pipefail

die() { printf 'check-personal-data: %s\n' "$*" >&2; exit 2; }

mode="${1:-}"
range=""
case "$mode" in
  --staged|--pre-push|--all-history) ;;
  --range) range="${2:-}"; [ -n "$range" ] || die "--range needs A..B" ;;
  *) die "usage: $0 --staged | --pre-push | --range A..B | --all-history" ;;
esac

root="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
cd "$root"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
scan="$work/scan.txt"     # "location<TAB>text" per line to check
names="$work/names.txt"   # "location<TAB>path" for added files
: >"$scan"; : >"$names"

# ── collect ────────────────────────────────────────────────────────────────

# collect_log RANGE... — added lines, commit messages and identities.
# awk keeps the commit header (identities + message) and, inside diffs, only
# added lines and file names. Removed lines are not new exposure.
collect_log() {
  git log --no-color --no-ext-diff -p -U0 --no-renames \
      --format='%x01COMMIT %h%nAUTHOR %an <%ae>%nCOMMITTER %cn <%ce>%n%B' "$@" -- |
  awk -v OFS='\t' '
    /^\001COMMIT / { c = substr($0, 9); file = "(message)"; indiff = 0; next }
    /^diff --git / { indiff = 1; next }
    !indiff        { print c ":" file, $0; next }
    /^\+\+\+ b\//  { file = substr($0, 7); print c ":" file, "(file name) " file; next }
    /^\+\+\+ /     { next }
    /^\+/          { print c ":" file, substr($0, 2) }
  ' >>"$scan"
  git log --no-color --no-renames --diff-filter=A --name-only --format='%x01%h' "$@" -- |
  awk -v OFS='\t' '/^\001/ { c = substr($0, 2); next } NF { print c, $0 }' >>"$names"
}

collect_staged() {
  git diff --cached --no-color --no-ext-diff -U0 --no-renames |
  awk -v OFS='\t' '
    /^diff --git / { next }
    /^\+\+\+ b\//  { file = substr($0, 7); print "staged:" file, "(file name) " file; next }
    /^\+\+\+ /     { next }
    /^\+/          { print "staged:" file, substr($0, 2) }
  ' >>"$scan"
  git diff --cached --name-only --diff-filter=A | awk -v OFS='\t' 'NF { print "staged", $0 }' >>"$names"
}

case "$mode" in
  --staged) collect_staged ;;
  --all-history) collect_log HEAD ;;
  --range) collect_log "$range" ;;
  --pre-push)
    zero="0000000000000000000000000000000000000000"
    while read -r _lref lsha _rref rsha; do
      [ "$lsha" = "$zero" ] && continue            # branch deletion
      if [ "$rsha" = "$zero" ]; then
        collect_log "$lsha" --not --remotes        # new branch: whatever no remote has yet
      else
        collect_log "$rsha..$lsha"
      fi
    done
    ;;
esac

# Also scan the denylist and token patterns against the files as they exist at
# HEAD/index, which catches content that pre-dates this hook.
if [ "$mode" != "--staged" ]; then
  git grep -I -n -e '' HEAD -- . ':!go.sum' 2>/dev/null |
    awk -v OFS='\t' '{ i = index($0, ":"); rest = substr($0, i + 1); j = index(rest, ":"); k = index(substr(rest, j + 1), ":");
                       print "HEAD:" substr(rest, 1, j - 1) ":" substr(rest, j + 1, k - 1), substr(rest, j + k + 1) }' >>"$scan" || true
fi

findings=0
report() { printf '  %s  %s\n' "$1" "$2"; findings=$((findings + 1)); }

# mask TEXT — show just enough to locate the hit without republishing it in
# CI logs.
mask() { local s="$1"; printf '%s…(%d chars)' "${s:0:3}" "${#s}"; }

# ── generic patterns ───────────────────────────────────────────────────────

# One grep per pattern over the text column; line numbers map back to the
# location column. Lines marked personal-data:ignore and go.sum (checksums)
# are blanked for the generic patterns only.
cut -f1 "$scan" >"$work/loc.txt"
awk -F'\t' '{ t = $0; sub(/^[^\t]*\t/, "", t) }
             $1 ~ /go\.sum(:|$)/ || t ~ /personal-data:ignore/ { print ""; next }
             { print t }' "$scan" >"$work/text.txt"

# generic ERE [ALLOW-ERE] — prints "location<TAB>match" for matches not allowed.
generic() {
  { grep -noE -- "$1" "$work/text.txt" || true; } |
  ALLOW="${2:-}" awk -F: 'BEGIN { allow = ENVIRON["ALLOW"] } NR == FNR { loc[FNR] = $0; next }
       { n = $1; m = substr($0, length(n) + 2); if (allow != "" && m ~ allow) next; print loc[n] "\t" m }' \
      "$work/loc.txt" - | sort -u
}

show() {   # show TITLE < "location<TAB>match" lines
  local out; out="$(cat)"
  [ -z "$out" ] && return 0
  printf '\n%s:\n' "$1"
  while IFS=$'\t' read -r loc hit; do report "$loc" "$(mask "$hit")"; done <<<"$out"
}

# WhatsApp JIDs other than the documented placeholders.
show 'WhatsApp JIDs' < <(generic '[0-9]{6,}(-[0-9]+)?@(g\.us|s\.whatsapp\.net|lid)' '^(120363000000000000|15551234567|15559876543)@')
# Phone numbers: international format, or a long digit run in a JSON string.
show 'Phone numbers' < <(generic '(\+[1-9][0-9 ]{9,16}[0-9]|"[1-9][0-9]{10,14}")' '^"?\+?1[ -]?555[ -]?(123[ -]?4567|987[ -]?6543)"?$')
show 'Email addresses' < <(generic '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}' \
        '(@example\.(com|org|net)$|@users\.noreply\.github\.com$|^noreply@anthropic\.com$|^git@github\.com$|@(g\.us|s\.whatsapp\.net|lid)$)')
show 'Home-directory paths' < <(generic '/(Users|home)/[^/[:space:]"'"'"'`)]+' '^/(Users|home)/(you|user|USERNAME|runner|<[^>]*>|\$USER|\$\{USER\})$')
show 'Tokens and keys' < <(generic '(sk-ant-[A-Za-z0-9_-]{8,}|glpat-[A-Za-z0-9_.-]{8,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AIza[0-9A-Za-z_-]{30,}|ya29\.[0-9A-Za-z_-]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[0-9A-Za-z-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|"refresh_token" *: *"[^"]{8,})')

# ── blocked files ──────────────────────────────────────────────────────────

blocked="$(awk -F'\t' '$2 ~ /(\.db|\.db-wal|\.db-shm|\.sqlite3?|\.pem|\.key|\.p12|qrcode\.png|(^|\/)\.env|(^|\/)config\.json|personal-denylist|rclone\.conf|dashboard\.token|conversation-history\.jsonl|claude-stream\.log)$/' "$names" | sort -u)"
if [ -n "$blocked" ]; then
  printf '\nFiles that must never be committed:\n'
  while IFS=$'\t' read -r loc f; do report "$loc" "$f"; done <<<"$blocked"
fi

# Large added files: the repo is source code; anything big is probably data.
if [ "$mode" != "--staged" ]; then
  while IFS=$'\t' read -r c f; do
    size="$(git cat-file -s "$c:$f" 2>/dev/null || echo 0)"
    [ "$size" -gt 2000000 ] && report "$c" "$f is $size bytes (over 2 MB)"
  done <"$names"
fi

# ── personal denylist ──────────────────────────────────────────────────────

denylist=""
for cand in "${PERSONAL_DENYLIST:-}" "$HOME/.config/whatsapp-claude-bridge/personal-denylist" "$root/.personal-denylist"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then denylist="$cand"; break; fi
done
if git ls-files --error-unmatch .personal-denylist >/dev/null 2>&1; then
  printf '\n.personal-denylist is tracked by git — remove it from the index immediately.\n'
  findings=$((findings + 1))
fi

if [ -n "$denylist" ]; then
  # Normalised copy of every scanned line for digit-only entries.
  cut -f2- "$scan" | tr -d ' .()+-' >"$work/digits.txt"
  n=0; first=1
  while IFS= read -r entry || [ -n "$entry" ]; do
    case "$entry" in ''|'#'*) continue ;; esac
    n=$((n + 1))
    hits="$(cut -f2- "$scan" | grep -n -i -F -- "$entry" | cut -d: -f1 || true)"
    if printf '%s' "$entry" | grep -qE '^[0-9]{6,}$'; then
      hits="$hits"$'\n'"$(grep -n -F -- "$entry" "$work/digits.txt" | cut -d: -f1 || true)"
    fi
    for line in $(printf '%s\n' "$hits" | grep -E '^[0-9]+$' | sort -un); do
      [ "$first" = 1 ] && { printf '\nPersonal denylist (%s):\n' "$denylist"; first=0; }
      report "$(sed -n "${line}p" "$work/loc.txt")" "denylist entry #$n"
    done
  done <"$denylist"
else
  printf 'check-personal-data: no personal denylist found — only generic patterns were checked.\n' >&2
fi

if [ "$findings" -gt 0 ]; then
  printf '\n✗ %d possible personal-data finding(s). Nothing was published.\n' "$findings" >&2
  printf '  Fix the content (and rewrite history if it is already committed), or mark a\n' >&2
  printf '  documented placeholder with "personal-data:ignore".\n' >&2
  exit 1
fi
printf '✓ no personal data found (%s)\n' "${mode#--}"
