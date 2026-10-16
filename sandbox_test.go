package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSandboxFailsClosed: with the sandbox on, a run whose sandbox cannot be
// set up is refused before anything starts, the tmux viewer included. It used
// to fall back to an unsandboxed spawn that still skipped every permission
// prompt.
func TestSandboxFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(sp *SandboxProfiler, home string)
	}{
		{"sandbox-exec missing", func(sp *SandboxProfiler, home string) {
			sp.execPath = filepath.Join(home, "no-sandbox-exec")
		}},
		{"path that cannot be quoted", func(sp *SandboxProfiler, home string) {
			sp.extraDeny = append(sp.extraDeny, filepath.Join(home, "two\nlines"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, `, "viewer": true`)
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("WCB_FAKE_ARGS_LOG", argsLog)
			tmuxCalls := fakeTmux(t)
			sp := mgr.profiler
			sp.mu.Lock()
			sp.enabled = true
			tc.breakIt(sp, home)
			sp.mu.Unlock()

			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })

			res := d.all()[0].res
			if !errors.Is(res.Err, errSandboxUnavailable) {
				t.Fatalf("run result = %+v, want errSandboxUnavailable", res)
			}
			if strings.Contains(res.Err.Error(), home) {
				t.Errorf("the chat error names a local path: %v", res.Err)
			}
			if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
				t.Error("the CLI was started without the sandbox")
			}
			if _, err := os.Stat(tmuxCalls); !os.IsNotExist(err) {
				t.Error("a refused run started the tmux viewer")
			}
			if last := mgr.Get("a").Snapshot().Last; last == nil || last.Outcome != "error" {
				t.Errorf("last run = %+v, want outcome error", last)
			}

			// Control: with the sandbox off the same brain does start the
			// viewer, so the check above would have seen it.
			sp.mu.Lock()
			sp.enabled = false
			sp.mu.Unlock()
			if _, _, _, err := mgr.Submit(dm("again")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the second run", func() bool { return d.count() == 2 })
			if _, err := os.Stat(tmuxCalls); err != nil {
				t.Errorf("the unsandboxed run did not start the viewer: %v", err)
			}
		})
	}
}

// fakeTmux makes the activity viewer use a tmux that only logs its calls to
// the returned file and then fails, so the viewer never gets as far as
// opening a Terminal window.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	t.Setenv("WCB_FAKE_TMUX_LOG", calls)
	script := filepath.Join(dir, "tmux")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> \"$WCB_FAKE_TMUX_LOG\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmuxOnce.Do(func() {}) // keep findTmux from looking up the real one
	old := tmuxPath
	tmuxPath = script
	t.Cleanup(func() { tmuxPath = old })
	return calls
}

// TestSandboxConfigNeedsMacOS: the sandbox only exists on macOS, and brain
// runs refuse to start without it, so enabling it elsewhere is rejected when
// the config loads instead of failing every message.
func TestSandboxConfigNeedsMacOS(t *testing.T) {
	cases := []struct {
		goos    string
		enabled bool
		wantErr bool
	}{
		{"darwin", true, false},
		{"darwin", false, false},
		{"linux", true, true},
		{"linux", false, false},
		{"freebsd", true, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s enabled=%v", tc.goos, tc.enabled), func(t *testing.T) {
			err := SandboxConfig{Enabled: tc.enabled}.validate(tc.goos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "sandbox.enabled") {
				t.Errorf("error %q does not name the setting", err)
			}
		})
	}
}

// TestSandboxCommandPassesProfileInline: the profile travels as an argument,
// never through a file in the shared TMPDIR that another brain could swap.
func TestSandboxCommandPassesProfileInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sp := NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a"), "b": filepath.Join(home, "b")},
		&Config{DataDir: filepath.Join(home, "data")})
	sp.execPath = os.Args[0] // any file that exists

	bin, args, err := sp.Command("a", "/opt/claude", []string{"--verbose", "-p", "hi"})
	if runtime.GOOS != "darwin" {
		if err == nil {
			t.Fatalf("Command succeeded on %s, which has no sandbox-exec", runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"-p", mustProfile(t, sp, "a"), "/opt/claude"}, "--verbose", "-p", "hi")
	if bin != sp.execPath || !slices.Equal(args, want) {
		t.Errorf("Command = %s %q, want %s %q", bin, args, sp.execPath, want)
	}
}

// mustProfile is sp.Profile(brainID) for profiles that must build.
func mustProfile(t *testing.T, sp *SandboxProfiler, brainID string) string {
	t.Helper()
	profile, err := sp.Profile(brainID)
	if err != nil {
		t.Fatalf("Profile(%q): %v", brainID, err)
	}
	return profile
}

// TestSBPLString: paths go into the profile as raw UTF-8 with only backslash
// and double quote escaped. Go's %q wrote \u00a0 and \u200d escapes the
// profile reader does not decode, so those rules never matched.
func TestSBPLString(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"plain", "/Users/you/a", `"/Users/you/a"`, false},
		{"space", "/Users/you/My Brain", `"/Users/you/My Brain"`, false},
		{"no-break space", "/Users/you/My\u00a0Brain", "\"/Users/you/My\u00a0Brain\"", false},
		{"narrow no-break space", "/x/10.00\u202fAM.png", "\"/x/10.00\u202fAM.png\"", false},
		{"emoji with joiners", "/x/\U0001F468\u200d\U0001F469\u200d\U0001F467", "\"/x/\U0001F468\u200d\U0001F469\u200d\U0001F467\"", false},
		{"tab", "/x/a\tb", "\"/x/a\tb\"", false},
		{"double quote", `/x/say "hi"`, `"/x/say \"hi\""`, false},
		{"backslash", `/x/a\b`, `"/x/a\\b"`, false},
		{"NUL", "/x/a\x00b", "", true},
		{"newline", "/x/a\nb", "", true},
		{"carriage return", "/x/a\rb", "", true},
		{"invalid UTF-8", "/x/a\xffb", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sbplString(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("sbplString(%q) error = %v, want error %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("sbplString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestSandboxProfileRefusesUnquotablePaths: a path that cannot be written
// into the profile fails the whole profile instead of dropping its rule.
func TestSandboxProfileRefusesUnquotablePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]func(cfg *Config, dirs map[string]string){
		"other brain's folder": func(cfg *Config, dirs map[string]string) { dirs["b"] = home + "/b\nc" },
		"deny path":            func(cfg *Config, dirs map[string]string) { cfg.Sandbox.DenyPaths = []string{home + "/x\x00"} },
		"allow path":           func(cfg *Config, dirs map[string]string) { cfg.Sandbox.AllowPaths = []string{home + "/x\r"} },
		"data dir":             func(cfg *Config, dirs map[string]string) { cfg.DataDir = home + "/data\n" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{DataDir: filepath.Join(home, "data")}
			dirs := map[string]string{"a": filepath.Join(home, "a"), "b": filepath.Join(home, "b")}
			breakIt(cfg, dirs)
			if profile, err := NewSandboxProfiler(dirs, cfg).Profile("a"); err == nil {
				t.Errorf("Profile succeeded:\n%s", profile)
			}
		})
	}
}
