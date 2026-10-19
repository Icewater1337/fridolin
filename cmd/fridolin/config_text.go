package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sbplString quotes s as an SBPL string literal. Go's %q is not one: it
// writes a no-break space as \u00a0 and the joiner inside emoji as \u200d,
// which the profile reader does not decode, so the rule named a path that
// does not exist and never matched. Only backslash and double quote need
// escaping; everything else goes in as UTF-8. A NUL cannot be passed in an
// argument at all, and line breaks or invalid UTF-8 are refused rather than
// guessed at.
func sbplString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("sandbox path %q is not valid UTF-8", s)
	}
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", fmt.Errorf("sandbox path %q contains a NUL or a line break", s)
	}
	return `"` + escapeSBPL(s) + `"`, nil
}

var sbplEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// validEnvName reports whether name can be an environment variable's name
// in child_env_passthrough: letters, digits and underscores, not starting
// with a digit. "=" or a NUL byte would make the entry another variable or
// none at all.
func validEnvName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, c := range name {
		if c != '_' && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z') && !('0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// escapeSBPL escapes text inside a profile string, also used by diagnostic redaction.
func escapeSBPL(s string) string { return sbplEscaper.Replace(s) }
