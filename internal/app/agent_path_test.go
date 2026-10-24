package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// agentOnPATH puts an executable called name in a folder of its own and
// makes that folder the bridge's PATH for the test.
func agentOnPATH(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookups of a script need a .exe on Windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return bin
}

func TestResolveAgentBinary(t *testing.T) {
	// Serial: puts the agent CLI on the process's PATH, where the bridge
	// looks it up.
	claude := agentOnPATH(t, "claude")
	for _, tc := range []struct{ provider, path string }{{"claude", "claude"}, {"claude", ""}, {"", claude}} {
		got, err := resolveAgentBinary(tc.provider, tc.path)
		if err != nil || got != claude {
			t.Errorf("resolveAgentBinary(%q, %q) = %q, %v; want %s", tc.provider, tc.path, got, err, claude)
		}
	}
	_, err := resolveAgentBinary("codex", "codex")
	if err == nil {
		t.Fatal("a codex that is not on PATH resolved")
	}
	for _, want := range []string{`the codex CLI "codex" was not found on the bridge's PATH`, "codex_path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want %q in it", err, want)
		}
	}
	if _, err := resolveAgentBinary("claude", filepath.Join(t.TempDir(), "claude")); err == nil || !strings.Contains(err.Error(), "claude_path") {
		t.Errorf("a missing absolute claude_path: %v", err)
	}
}

// The live bug: a native install moved into the container kept no
// codex_path, and every run was refused with `bwrap policy for
// "personal": "codex" is not an absolute path`. The runner now hands the
// sandbox the CLI's absolute path, found on the bridge's PATH.
func TestRunnerHandsBwrapTheAgentsAbsolutePath(t *testing.T) {
	// Serial: puts the agent CLI on the process's PATH, where the bridge
	// looks it up.
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			agent := agentOnPATH(t, provider)
			data := t.TempDir()
			folder := filepath.Join(data, "brains-folders", "personal")
			if err := os.MkdirAll(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			sp := bwrapProfiler(t, config.SandboxConfig{}, map[string][]string{"personal": {folder}}, nil)
			sp.DataDir = data
			sp.State = func(id string) sandbox.StatePaths { return sandbox.StatePaths{Dir: filepath.Join(data, "brains", id)} }
			sp.bwrapPath = filepath.Join(data, "bwrap")
			if err := os.WriteFile(sp.bwrapPath, nil, 0o700); err != nil {
				t.Fatal(err)
			}
			r := &commandRunner{sandbox: sp, env: envOf(t)}
			var spawned claudecli.Spawn
			refused := errors.New("not started in a test")
			res := r.Run(context.Background(), brain.RunSpec{
				ID: "personal", Dir: folder, Prompt: "hi", BeforeStart: func() {},
				Settings: &brain.Settings{Provider: provider, ClaudePath: "claude", CodexPath: "codex", DataDir: data, MediaDir: filepath.Join(data, "media")},
			}, claudecli.Callbacks{
				Start: func(_ context.Context, s claudecli.Spawn) (*claudecli.Process, error) {
					spawned = s
					return nil, refused
				},
				Started: func(int) func() { return nil },
			})
			if !errors.Is(res.Err, refused) {
				t.Fatalf("run ended with %v, want it to reach the start", res.Err)
			}
			if spawned.Binary != sp.bwrapPath {
				t.Fatalf("started %s, want bubblewrap", spawned.Binary)
			}
			i := len(spawned.Args) - 1
			for i >= 0 && spawned.Args[i] != agent {
				i--
			}
			if i < 0 || spawned.Args[i-1] == "--ro-bind-try" {
				t.Errorf("bubblewrap does not run %s: %q", agent, spawned.Args)
			}
		})
	}
}

// A CLI that is not on the bridge's PATH ends the run before anything
// starts, with an error that names the setting.
func TestRunnerRefusesAMissingAgent(t *testing.T) {
	// Serial: puts the agent CLI on the process's PATH, where the bridge
	// looks it up.
	agentOnPATH(t, "claude")
	var started bool
	r := &commandRunner{env: envOf(t)}
	res := r.Run(context.Background(), brain.RunSpec{
		ID: "personal", Dir: t.TempDir(), Prompt: "hi", BeforeStart: func() { started = true },
		Settings: &brain.Settings{Provider: "codex", CodexPath: "codex"},
	}, claudecli.Callbacks{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "codex_path") || started {
		t.Errorf("run ended with %v (started %t), want the missing codex named", res.Err, started)
	}
}
