// Package textx shortens text for logs, previews and error messages
// without cutting a UTF-8 rune in two. It knows nothing of the bridge.
package textx

import (
	"strings"
	"unicode/utf8"
)

// Truncate shortens s to at most n bytes without splitting a UTF-8 rune.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// Tail is Truncate's counterpart for the end of s, for the last lines of
// a command's output: s without surrounding space, cut after an ellipsis
// to its last n bytes when longer, or a few fewer so that no rune is cut
// in two.
func Tail(s string, n int) string {
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
