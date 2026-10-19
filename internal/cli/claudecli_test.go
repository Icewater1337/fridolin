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
			cli := filepath.Join(home, "bin", "claude")
			if !c.missing {
				if err := os.MkdirAll(filepath.Dir(cli), 0o700); err != nil {
					t.Fatal(err)
				}
				versionCLI(t, cli, c.version)
			}
			path := writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data", "claude_path": "`+cli+`"}`)
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
