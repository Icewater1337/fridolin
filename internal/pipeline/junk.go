package pipeline

import (
	"strings"
	"unicode"
)

// IsJunk reports whether a message is too trivial to be worth a Claude
// invocation — a bare emoji, a single character, or a one-word acknowledgement.
// Only applied to untagged group chatter, never to a message addressed to the
// bridge, and such a message still goes into the history (see observeRuns).
func IsJunk(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len([]rune(trimmed)) <= 1 {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "ok", "ok!", "okay", "thanks", "thank you", "yes", "no", "yep", "nope", "lol", "haha", "hehe":
		return true
	}
	// No letters and no digits, in any script: emoji, punctuation, or similar.
	return strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0
}
