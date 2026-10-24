package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

func TestCheckShowsTheClaudeVersion(t *testing.T) {
	cases := []struct {
		name, version string
		missing       bool
		want          string
	}{
		{"tested release", claudecli.TestedClaudeVersion + " (Claude Code)", false, "(" + claudecli.TestedClaudeVersion + " (Claude Code))"},
		{"older release", "2.0.1 (Claude Code)", false, "older than " + claudecli.TestedClaudeVersion},
		{"not installed", "", true, "not found, so brains cannot run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			// The test binary is the CLI (testutil's fake), through a link
			// of each case's own, which check caches the version by. It is
			// no new program, which macOS would scan before its first run.
			t.Setenv("FRIDOLIN_FAKE_CLAUDE", "1")
			t.Setenv("FRIDOLIN_FAKE_VERSION", c.version)
			cli := filepath.Join(home, "bin", "claude")
			if !c.missing {
				if err := os.MkdirAll(filepath.Dir(cli), 0o700); err != nil {
					t.Fatal(err)
				}
				self, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(self, cli); err != nil {
					t.Fatal(err)
				}
			}
			path := writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data", "claude_path": "`+cli+`",
				"child_env_passthrough": ["FRIDOLIN_FAKE_CLAUDE", "FRIDOLIN_FAKE_VERSION"]}`)
			out := captureStdout(t, func() { checkCmd([]string{"--config", path}) })
			line := lineWith(out, "claude:")
			if !strings.Contains(line, cli) || !strings.Contains(line, c.want) {
				t.Errorf("claude line %q, want the path and %q", line, c.want)
			}
			if c.name == "tested release" && strings.Contains(line, "older than") {
				t.Errorf("warns about the tested release: %q", line)
			}
		})
	}
}
