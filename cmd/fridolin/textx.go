package main

import (
	"strings"
	"unicode/utf8"
)

// Text helpers that know nothing of the bridge.

// truncate shortens s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// tail is truncate's counterpart for the end of s, for the last lines of
// a command's output: s without surrounding space, cut after an ellipsis
// to its last n bytes when longer, or a few fewer so that no rune is cut
// in two.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "…" + s[i:]
}
