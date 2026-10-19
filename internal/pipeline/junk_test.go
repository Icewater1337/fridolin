package pipeline

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestIsJunkMessage(t *testing.T) {
	junk := []string{"ok", "OK", "thanks", "👍", "...", "?", "a", "🎉🎉"}
	for _, s := range junk {
		if !IsJunk(s) {
			t.Errorf("isJunkMessage(%q) = false, want true", s)
		}
	}

	real := []string{"what time is it", "ok but why", "book it for 3pm", "PR 42?", "привет", "你好", "שלום!", "ßß"}
	for _, s := range real {
		if IsJunk(s) {
			t.Errorf("isJunkMessage(%q) = true, want false", s)
		}
	}
}

// FuzzIsJunkMessage: untagged group messages the filter calls junk never
// get a run, not even in a brain that observes with runs (observeRuns),
// so it may only catch what carries nothing: a single character, no
// letter or digit in any script, or one of the short acknowledgements,
// which are ASCII once lowercased the way the filter compares them.
// Whitespace around a message and its ASCII letter case never change the
// verdict.
func FuzzIsJunkMessage(f *testing.F) {
	for _, s := range []string{"ok", "OK", "o\u212a", "than\u212a you", "thanks", "thank you", "👍", "...", "?", "a", "🎉🎉", "what time is it", "ok but why", "book it for 3pm", "PR 42?", "", " \n", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got := IsJunk(text)
		if padded := IsJunk(" \t" + text + "\n "); padded != got {
			t.Errorf("isJunkMessage(%q) = %v, but %v with whitespace around it", text, got, padded)
		}
		if swapped := swapASCIICase(text); IsJunk(swapped) != got {
			t.Errorf("isJunkMessage(%q) = %v, but not for %q", text, got, swapped)
		}
		trimmed := strings.TrimSpace(text)
		short := utf8.RuneCountInString(trimmed) <= 1
		content := strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
		if (short || !content) && !got {
			t.Errorf("isJunkMessage(%q) = false for a message without content", text)
		}
		// Lowercased like the filter does: strings.ToLower turns some
		// non-ASCII letters into ASCII ones, so "o\u212a" is "ok".
		lower := strings.ToLower(trimmed)
		if got && !short && content && (len(lower) > len("thank you") || !isASCII(lower)) {
			t.Errorf("isJunkMessage(%q) = true for a message with letters or digits", text)
		}
	})
}

// swapASCIICase turns ASCII lowercase letters into uppercase and back,
// leaving every other byte as it is.
func swapASCIICase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z':
			b[i] = c - 'a' + 'A'
		case 'A' <= c && c <= 'Z':
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
