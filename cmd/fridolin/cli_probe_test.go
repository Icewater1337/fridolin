package main

import (
	"strings"
	"testing"
)

func TestClaudeVersionWarning(t *testing.T) {
	cases := []struct {
		version string
		warn    bool
	}{
		{testedClaudeVersion + " (Claude Code)", false},
		{"2.1.303 (Claude Code)", false},
		{"2.2.0 (Claude Code)", false},
		{"3.0.0", false},
		{"v2.1.302", false},
		{"2.1.301 (Claude Code)", true},
		{"2.0.999 (Claude Code)", true},
		{"1.0.128 (Claude Code)", true},
		{"2.1.0-beta.1 (Claude Code)", true},
		// A version it cannot read is no reason to warn.
		{"", false},
		{"Claude Code", false},
		{"2.1", false},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			w := claudeVersionWarning(claudeStatus{Path: "/usr/local/bin/claude", Found: true, Version: c.version})
			if (w != "") != c.warn {
				t.Errorf("warning %q, want one: %v", w, c.warn)
			}
			if c.warn && !strings.Contains(w, "older than "+testedClaudeVersion) {
				t.Errorf("warning %q does not name the tested release", w)
			}
		})
	}
	if w := claudeVersionWarning(claudeStatus{Path: "claude", Version: "1.0.0"}); w != "" {
		t.Errorf("a CLI that was not found: %q", w)
	}
}
