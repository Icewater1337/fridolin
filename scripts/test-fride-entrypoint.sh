#!/usr/bin/env bash
#
# Tests for set-claude-token in the FrIDE image's entrypoint
# (container/fride/entrypoint.sh): what it stores of a token piped in, and
# what it refuses without storing it, in an empty temp HOME. Nothing else
# of the entrypoint runs.
#
#   scripts/test-fride-entrypoint.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/container/fride/entrypoint.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
stored="$tmp/fride-home/.config/fridolin-box/claude-token"
failed=0

# Made up, and split so that no line here looks like a real token.
prefix="sk-ant-"
token="${prefix}oat01-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfGhIjKlMnOpQrSt"

# set_token INPUT — runs set-claude-token with INPUT (printf %b) on stdin;
# leaves its exit status in $code and its stderr in $err.
set_token() {
	code=0
	err="$(printf '%b' "$1" | HOME="$tmp/fride-home" sh "$script" set-claude-token 2>&1 >/dev/null)" || code=$?
}

# stores NAME INPUT — INPUT is stored as the token, alone on its line.
stores() {
	rm -rf "$tmp/fride-home"
	set_token "$2"
	if [ "$code" -ne 0 ]; then
		printf 'FAIL  %s: exit %s: %s\n' "$1" "$code" "$err"
		failed=$((failed + 1))
	elif [ "$(cat "$stored")" != "$token" ] || [ "$(wc -l <"$stored")" -ne 1 ]; then
		printf 'FAIL  %s: stored %s bytes, not the token\n' "$1" "$(wc -c <"$stored")"
		failed=$((failed + 1))
	elif [ "$(stat -c %a "$stored" 2>/dev/null || stat -f %Lp "$stored")" != 600 ]; then
		printf 'FAIL  %s: the token file is not 0600\n' "$1"
		failed=$((failed + 1))
	else
		printf 'ok    %s\n' "$1"
	fi
}

# refuses NAME INPUT WANT — INPUT is refused with exit 2 and a message
# matching WANT, which never repeats it, and the token stored before stays.
refuses() {
	rm -rf "$tmp/fride-home"
	set_token "$token\n"
	set_token "$2"
	input="$(printf '%b' "$2" | tr -d '\r\n')"
	if [ "$code" -ne 2 ]; then
		printf 'FAIL  %s: exit %s, want 2\n' "$1" "$code"
		failed=$((failed + 1))
	elif ! grep -q -- "$3" <<<"$err"; then
		printf 'FAIL  %s: message %q lacks %q\n' "$1" "$err" "$3"
		failed=$((failed + 1))
	elif [ -n "$input" ] && grep -qF -- "$input" <<<"$err"; then
		printf 'FAIL  %s: the message repeats the input\n' "$1"
		failed=$((failed + 1))
	elif [ "$(cat "$stored")" != "$token" ]; then
		printf 'FAIL  %s: the stored token changed\n' "$1"
		failed=$((failed + 1))
	else
		printf 'ok    %s\n' "$1"
	fi
}

stores "a token" "$token\n"
stores "a token piped from Windows PowerShell (CRLF)" "$token\r\n"
stores "a token piped with a byte order mark" "\0357\0273\0277$token\r\n"
stores "a token without a newline" "$token"
stores "spaces around the token" "  $token \t\n"

refuses "the browser's sign-in code" "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGhIjKl#AbCdEfGhIjKlMnOpQrStUvWxY_a-AbCdEfGhIjKlMnO\r\n" "sign-in code from the browser"
refuses "an API key" "${prefix}api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789\n" "does not start with sk-ant-oat"
refuses "text around the token" "export CLAUDE_CODE_OAUTH_TOKEN=$token\n" "does not start with sk-ant-oat"
refuses "odd characters in the token" "${prefix}oat01-AbCdEf\$GhIj\n" "characters a token never has"
refuses "nothing" "\n" "no token on stdin"

if [ "$failed" -gt 0 ]; then
	echo "$failed case(s) failed"
	exit 1
fi
echo "all cases passed"
