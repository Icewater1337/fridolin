package claudecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeVersionWarning(t *testing.T) {
	cases := []struct {
		version string
		warn    bool
	}{
		{TestedClaudeVersion + " (Claude Code)", false},
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
			w := VersionWarning(Status{Path: "/usr/local/bin/claude", Found: true, Version: c.version})
			if (w != "") != c.warn {
				t.Errorf("warning %q, want one: %v", w, c.warn)
			}
			if c.warn && !strings.Contains(w, "older than "+TestedClaudeVersion) {
				t.Errorf("warning %q does not name the tested release", w)
			}
		})
	}
	if w := VersionWarning(Status{Path: "claude", Version: "1.0.0"}); w != "" {
		t.Errorf("a CLI that was not found: %q", w)
	}
}

// A `--version` run that fails once (a timeout on a cold start, say) is
// tried again after FailedProbeRetry, so an outdated CLI is still flagged;
// a version found is kept until the binary changes (#680).
func TestProbeRetriesAFailedVersionRun(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "claude")
	// The first run fails, every later one prints an old version.
	body := "#!/bin/sh\n" +
		"if [ -e '" + runs + "' ]; then echo run >> '" + runs + "'; echo '2.0.0 (Claude Code)'; exit 0; fi\n" +
		"echo run > '" + runs + "'\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	countRuns := func() int {
		b, err := os.ReadFile(runs)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "run")
	}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	probe := &Prober{now: func() time.Time { return now }}

	if st := probe.Info("claude", script, nil); st.Version != "" || !st.Found {
		t.Fatalf("first probe = %+v, want found without a version", st)
	}
	// Within the retry delay the failure stands, without running it again.
	now = now.Add(FailedProbeRetry / 2)
	if st := probe.Info("claude", script, nil); st.Version != "" {
		t.Fatalf("probe within the retry delay = %+v", st)
	}
	if n := countRuns(); n != 1 {
		t.Fatalf("the CLI ran %d times within the retry delay, want 1", n)
	}
	now = now.Add(FailedProbeRetry)
	st := probe.Info("claude", script, nil)
	if st.Version != "2.0.0 (Claude Code)" {
		t.Fatalf("probe after the retry delay = %+v, want the version", st)
	}
	if VersionWarning(st) == "" {
		t.Errorf("no warning for %q", st.Version)
	}
	// A version found stays until the binary changes.
	now = now.Add(24 * time.Hour)
	if st := probe.Info("claude", script, nil); st.Version != "2.0.0 (Claude Code)" {
		t.Fatalf("cached probe = %+v", st)
	}
	if n := countRuns(); n != 2 {
		t.Errorf("the CLI ran %d times, want 2", n)
	}
}
