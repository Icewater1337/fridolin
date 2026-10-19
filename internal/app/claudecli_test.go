package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// versionCLI writes a stand-in for the Claude CLI that answers --version
// with out, and returns its path.
func versionCLI(t *testing.T, path, out string) string {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "claude")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeInfoNoticesANewRelease(t *testing.T) {
	// The CLI updates itself while the bridge runs; the dashboard used to
	// show the version it saw first until the bridge restarted.
	cli := versionCLI(t, "", "2.1.0 (Claude Code)")
	if st := ClaudeInfo(cli, nil); !st.Found || st.Version != "2.1.0 (Claude Code)" {
		t.Fatalf("first look: %+v", st)
	}
	versionCLI(t, cli, "2.1.400 (Claude Code)")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(cli, later, later); err != nil {
		t.Fatal(err)
	}
	if st := ClaudeInfo(cli, nil); st.Version != "2.1.400 (Claude Code)" {
		t.Errorf("after the update: %+v, want 2.1.400", st)
	}

	t.Run("a symlink moved to a new release", func(t *testing.T) {
		dir := t.TempDir()
		oldCLI := versionCLI(t, filepath.Join(dir, "old"), "2.1.0 (Claude Code)")
		newCLI := versionCLI(t, filepath.Join(dir, "new"), "2.1.500 (Claude Code)")
		link := filepath.Join(dir, "claude")
		if err := os.Symlink(oldCLI, link); err != nil {
			t.Fatal(err)
		}
		if st := ClaudeInfo(link, nil); st.Version != "2.1.0 (Claude Code)" {
			t.Fatalf("first look: %+v", st)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(newCLI, link); err != nil {
			t.Fatal(err)
		}
		if st := ClaudeInfo(link, nil); st.Version != "2.1.500 (Claude Code)" {
			t.Errorf("after the update: %+v, want 2.1.500", st)
		}
	})

	t.Run("installed after the first look", func(t *testing.T) {
		cli := filepath.Join(t.TempDir(), "claude")
		if st := ClaudeInfo(cli, nil); st.Found {
			t.Fatalf("found before it exists: %+v", st)
		}
		versionCLI(t, cli, claudecli.TestedClaudeVersion+" (Claude Code)")
		if st := ClaudeInfo(cli, nil); !st.Found {
			t.Errorf("still not found once installed: %+v", st)
		}
	})
}

func TestStatusWarnsAboutAnOldClaude(t *testing.T) {
	for _, c := range []struct {
		version string
		warn    bool
	}{
		{"2.0.1 (Claude Code)", true},
		{claudecli.TestedClaudeVersion + " (Claude Code)", false},
	} {
		t.Run(c.version, func(t *testing.T) {
			cli := versionCLI(t, "", c.version)
			h := newHarness(t, `{"claude_path": "`+cli+`"}`)
			w := h.Do("GET", "/api/status", nil)
			var resp struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != 200 {
				t.Fatalf("status %d %v: %s", w.Code, err, w.Body.String())
			}
			got := strings.Contains(strings.Join(resp.Warnings, "\n"), "older than "+claudecli.TestedClaudeVersion)
			if got != c.warn {
				t.Errorf("warnings %q, want the old-CLI warning: %v", resp.Warnings, c.warn)
			}
		})
	}
}

func TestStartupLogNamesTheClaudeCLI(t *testing.T) {
	cases := []struct {
		name, version string
		missing       bool
		want          []string
	}{
		{"tested release", claudecli.TestedClaudeVersion + " (Claude Code)", false, []string{"Claude Code CLI: ", claudecli.TestedClaudeVersion}},
		{"older release", "2.0.1 (Claude Code)", false, []string{"Claude Code CLI: ", "WARNING: ", "older than " + claudecli.TestedClaudeVersion}},
		{"not installed", "", true, []string{"WARNING: the Claude Code CLI", "was not found"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli := filepath.Join(t.TempDir(), "claude")
			if !c.missing {
				versionCLI(t, cli, c.version)
			}
			logged := captureLog(t)
			LogClaudeCLI(cli, nil)
			out := strings.Join(logged(), "\n")
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("log %q, want %q in it", out, w)
				}
			}
			if c.name == "tested release" && strings.Contains(out, "WARNING") {
				t.Errorf("warns about the tested release: %q", out)
			}
		})
	}
}
