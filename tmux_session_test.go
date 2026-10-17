package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestViewerGetsTheAllowlistedEnvironment: a tmux server the viewer starts
// keeps the environment it was started with, and any brain can read that
// from it. So every tmux call, and the osascript that opens the Terminal
// window, gets what childEnv gives any program and the names in
// child_env_passthrough, not the bridge's whole environment.
func TestViewerGetsTheAllowlistedEnvironment(t *testing.T) {
	logs := t.TempDir()
	bin := t.TempDir()
	// Each call records its environment under its first argument.
	fake := "#!/bin/sh\nenv > \"" + logs + "/$(basename \"$0\").$1\"\n"
	for _, name := range []string{"tmux", "osascript"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Use up findTmux's lookup, so that it returns the fake, and start a
	// fresh one afterwards, so that a later test finds the real tmux again.
	tmuxOnce.Do(func() {})
	oldTmux, oldOsascript := tmuxPath, osascript
	tmuxPath, osascript = filepath.Join(bin, "tmux"), filepath.Join(bin, "osascript")
	t.Cleanup(func() { tmuxPath, osascript, tmuxOnce = oldTmux, oldOsascript, sync.Once{} })
	prev := startViewer
	startViewer = NewTmuxViewer
	t.Cleanup(func() { startViewer = prev })
	for k, v := range map[string]string{
		"TMUX_TMPDIR":             filepath.Join(logs, "tmux"),
		"GROQ_API_KEY":            "test-groq-key",
		"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-token",
		"FRIDOLIN_TEST_SECRET":    "test-secret",
	} {
		t.Setenv(k, v)
	}

	br := bareBrain(t)
	br.ensureViewer(&brainSettings{viewer: true, envPassthrough: []string{"GROQ_API_KEY"}})
	if br.tmuxViewer == nil {
		t.Fatal("no viewer started")
	}
	if !br.tmuxViewer.IsAlive() {
		t.Fatal("the viewer is not alive")
	}
	br.tmuxViewer.Close()

	calls := []string{"tmux.kill-session", "tmux.new-session", "tmux.rename-window", "tmux.has-session"}
	if runtime.GOOS == "darwin" {
		calls = append(calls, "osascript.-e")
	}
	for _, call := range calls {
		t.Run(call, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(logs, call))
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(string(data), "\n")
			for _, want := range []string{"HOME=" + os.Getenv("HOME"), "TMUX_TMPDIR=" + filepath.Join(logs, "tmux"), "GROQ_API_KEY=test-groq-key"} {
				if !slices.Contains(env, want) {
					t.Errorf("%s lacks %s", call, want)
				}
			}
			for _, kv := range env {
				for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "FRIDOLIN_TEST_SECRET"} {
					if strings.HasPrefix(kv, k+"=") {
						t.Errorf("%s got %s", call, kv)
					}
				}
			}
		})
	}
}
