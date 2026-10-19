package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/registry"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestChildGets(t *testing.T) {
	cases := []struct {
		name                   string
		claude, other, desktop bool // given to a Claude run, to any other program, to a sign-in
	}{
		{"HOME", true, true, true},
		{"PATH", true, true, true},
		{"LANG", true, true, true},
		{"LC_ALL", true, true, true},
		{"LC_CTYPE", true, true, true},
		{"HTTPS_PROXY", true, true, true},
		{"https_proxy", true, true, true},
		{"NODE_EXTRA_CA_CERTS", true, true, true},
		{"TMUX_TMPDIR", true, true, true},
		// What a launchd job that logs the CLI in with a token sets besides
		// HOME and PATH.
		{"CLAUDE_CODE_OAUTH_TOKEN", true, false, false},
		{"CLAUDE_CODE_ENTRYPOINT", true, false, false},
		{"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", true, false, false},
		{"ANTHROPIC_API_KEY", true, false, false},
		{"ANTHROPIC_BASE_URL", true, false, false},
		{"TRANSCRIBE_MODEL", true, false, false},
		{"CALENDAR_TZ", true, false, false},
		// The bridge sets these from its own config (dataEnv).
		{"BRIDGE_DATA_DIR", false, false, false},
		{"BRIDGE_DB_PATH", false, false, false},
		// Only for a sign-in that opens your browser.
		{"DISPLAY", false, false, true},
		{"WAYLAND_DISPLAY", false, false, true},
		{"XAUTHORITY", false, false, true},
		{"XDG_RUNTIME_DIR", false, false, true},
		{"DBUS_SESSION_BUS_ADDRESS", false, false, true},
		{"XDG_CURRENT_DESKTOP", false, false, true},
		{"KDE_SESSION_VERSION", false, false, true},
		{"XDG_DATA_DIRS", false, false, true},
		{"XDG_CONFIG_DIRS", false, false, true},
		{"BROWSER", false, false, true},
		// Not on the list, some of them close to a name that is.
		{"GROQ_API_KEY", false, false, false},
		{"SSH_AUTH_SOCK", false, false, false},
		{"AWS_SECRET_ACCESS_KEY", false, false, false},
		{"RCLONE_CONFIG_DRIVE_TOKEN", false, false, false},
		{"CLAUDECODE", false, false, false},
		{"CLAUDE_CODE", false, false, false},
		{"HOMEBREW_GITHUB_API_TOKEN", false, false, false},
		{"PATHEXT", false, false, false},
		{"LCX", false, false, false},
		{"DISPLAYX", false, false, false},
		{"XDG_CONFIG_HOME", false, false, false},
		{"home", false, false, false},
		{"", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := childGets(claudeChild, tc.name); got != tc.claude {
				t.Errorf("childGets(claudeChild, %q) = %v, want %v", tc.name, got, tc.claude)
			}
			if got := childGets(anyChild, tc.name); got != tc.other {
				t.Errorf("childGets(anyChild, %q) = %v, want %v", tc.name, got, tc.other)
			}
			if got := childGets(desktopChild, tc.name); got != tc.desktop {
				t.Errorf("childGets(desktopChild, %q) = %v, want %v", tc.name, got, tc.desktop)
			}
		})
	}
}

func TestChildEnv(t *testing.T) {
	onlyEnv(t, map[string]string{
		"HOME":                    "/Users/you",
		"PATH":                    "/usr/bin:/bin",
		"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-token",
		"BRIDGE_BRAIN_ID":         "from-the-bridge",
		"FRIDOLIN_TEST_SECRET":    "test-secret",
	})
	cases := []struct {
		name        string
		scope       childScope
		passthrough []string
		extra       []string
		want        []string
	}{
		{"claude run", claudeChild, nil, nil,
			[]string{"CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token", "HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"other program", anyChild, nil, nil,
			[]string{"HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"passthrough adds a variable", anyChild, []string{"FRIDOLIN_TEST_SECRET"}, nil,
			[]string{"FRIDOLIN_TEST_SECRET=test-secret", "HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"passthrough of an unset variable", anyChild, []string{"FRIDOLIN_TEST_UNSET"}, nil,
			[]string{"HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"passthrough names, not prefixes", anyChild, []string{"FRIDOLIN_TEST"}, nil,
			[]string{"HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"the bridge's own variables are added", anyChild, nil, []string{"RCLONE_CONFIG=/tmp/rclone.conf"},
			[]string{"HOME=/Users/you", "PATH=/usr/bin:/bin", "RCLONE_CONFIG=/tmp/rclone.conf"}},
		{"and replace a listed one", anyChild, nil, []string{"HOME=/tmp/plaud-home"},
			[]string{"HOME=/tmp/plaud-home", "PATH=/usr/bin:/bin"}},
		{"or an unlisted one", claudeChild, nil, []string{"BRIDGE_BRAIN_ID=a"},
			[]string{"BRIDGE_BRAIN_ID=a", "CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token", "HOME=/Users/you", "PATH=/usr/bin:/bin"}},
		{"or one passed through", claudeChild, []string{"BRIDGE_BRAIN_ID"}, []string{"BRIDGE_BRAIN_ID=a"},
			[]string{"BRIDGE_BRAIN_ID=a", "CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token", "HOME=/Users/you", "PATH=/usr/bin:/bin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := childEnv(tc.scope, tc.passthrough, tc.extra...)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("childEnv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBrainGetsOnlyTheAllowlistedEnvironment: a brain's run gets the
// variables on childEnvAllowlist, the Claude CLI's login among them, and
// the bridge's own, and nothing else of the bridge's environment.
func TestBrainGetsOnlyTheAllowlistedEnvironment(t *testing.T) {
	listed := map[string]string{
		// What a launchd job that logs the CLI in with a token sets, with
		// HOME (below).
		"PATH":                                 "/usr/bin:/bin",
		"CLAUDE_CODE_OAUTH_TOKEN":              "test-oauth-token",
		"CLAUDE_CODE_ENTRYPOINT":               "test-entrypoint",
		"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST": "1",

		"USER":                "you",
		"LOGNAME":             "you",
		"SHELL":               "/bin/zsh",
		"TMPDIR":              "/tmp/",
		"LANG":                "en_US.UTF-8",
		"LC_CTYPE":            "en_US.UTF-8",
		"TZ":                  "Europe/Zurich",
		"HTTPS_PROXY":         "http://proxy.example.com:3128",
		"no_proxy":            "localhost",
		"NODE_EXTRA_CA_CERTS": "/etc/ssl/extra.pem",
		"ANTHROPIC_API_KEY":   "test-api-key",
		"ANTHROPIC_BASE_URL":  "http://127.0.0.1:9",
		"CALENDAR_TZ":         "Europe/Zurich",
	}
	unlisted := map[string]string{
		"FRIDOLIN_TEST_SECRET":      "test-secret",
		"GROQ_API_KEY":              "test-groq-key",
		"SSH_AUTH_SOCK":             "/tmp/agent.sock",
		"AWS_SECRET_ACCESS_KEY":     "test-aws-key",
		"RCLONE_CONFIG_DRIVE_TOKEN": "test-rclone-token",
		"CLAUDECODE":                "1",
		"DISPLAY":                   ":0", // for a sign-in only
		// The bridge sets these itself, so the brain gets the bridge's.
		"BRIDGE_BRAIN_ID": "not-a",
		"BRIDGE_DATA_DIR": "/elsewhere",
		"BRIDGE_DB_PATH":  "/elsewhere/bridge.db",
		// Older versions set this for every run, naming the folder of the
		// calendar tokens; now no run gets it.
		"BRIDGE_CREDENTIALS_DIR": "/elsewhere/creds",
	}
	env := maps.Clone(listed)
	maps.Copy(env, unlisted)
	got, home := brainRunEnv(t, env, "")

	want := maps.Clone(listed)
	maps.Copy(want, map[string]string{
		"HOME":              home,
		"FRIDOLIN_BRAIN_ID": "a",
		"BRIDGE_BRAIN_ID":   "a",
		"BRIDGE_DATA_DIR":   filepath.Join(home, "data"),
		"BRIDGE_DB_PATH":    filepath.Join(home, "data", "bridge.db"),
		// Plaud's CLI stays quiet in every brain's run (plaudOptOut).
		"PLAUD_TELEMETRY_DISABLED": "1",
		"PLAUD_NO_UPDATE_NOTIFIER": "1",
		// The fake CLI's own (testChildEnv).
		"FRIDOLIN_FAKE_CLAUDE":  "1",
		"FRIDOLIN_FAKE_ENV_LOG": got["FRIDOLIN_FAKE_ENV_LOG"],
	})
	if !maps.Equal(got, want) {
		for _, k := range slices.Sorted(maps.Keys(want)) {
			if v, ok := got[k]; !ok || v != want[k] {
				t.Errorf("the brain got %s=%q (set: %v), want %q", k, v, ok, want[k])
			}
		}
		for _, k := range slices.Sorted(maps.Keys(got)) {
			if _, ok := want[k]; !ok {
				t.Errorf("the brain got %s, which is not on the list", k)
			}
		}
	}
}

// TestBrainGetsThePassthrough: child_env_passthrough hands a brain the
// variables it names, and only those.
func TestBrainGetsThePassthrough(t *testing.T) {
	env := map[string]string{"GROQ_API_KEY": "test-groq-key", "SSH_AUTH_SOCK": "/tmp/agent.sock", "FRIDOLIN_TEST_SECRET": "test-secret"}
	cases := []struct {
		name, config string
		want         []string // of env's variables
	}{
		{"none", "", nil},
		{"empty", `, "child_env_passthrough": []`, nil},
		{"one", `, "child_env_passthrough": ["GROQ_API_KEY"]`, []string{"GROQ_API_KEY"}},
		{"two, one unset", `, "child_env_passthrough": ["GROQ_API_KEY", "SSH_AUTH_SOCK", "FRIDOLIN_TEST_UNSET"]`, []string{"GROQ_API_KEY", "SSH_AUTH_SOCK"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := brainRunEnv(t, env, tc.config)
			for _, k := range slices.Sorted(maps.Keys(env)) {
				v, ok := got[k]
				if want := slices.Contains(tc.want, k); ok != want || (ok && v != env[k]) {
					t.Errorf("the brain got %s=%q (set: %v), want it set: %v", k, v, ok, want)
				}
			}
			if _, ok := got["FRIDOLIN_TEST_UNSET"]; ok {
				t.Error("the brain got FRIDOLIN_TEST_UNSET, which the bridge does not have")
			}
		})
	}
}

func TestChildEnvPassthroughNames(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"GROQ_API_KEY", true},
		{"https_proxy", true},
		{"_PRIVATE", true},
		{"X2", true},
		{"", false},
		{"2X", false},
		{"A=B", false},
		{"A B", false},
		{"A-B", false},
		{"AWS_*", false},
		{"A\x00B", false},
		{"ÄPFEL", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			names, _ := json.Marshal([]string{tc.name})
			_, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/data", "child_env_passthrough": `+string(names)+`}`))
			if tc.ok && err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "child_env_passthrough")) {
				t.Fatalf("Load = %v, want an error naming child_env_passthrough", err)
			}
		})
	}
}

// TestClaudeVersionCheckGetsABrainsEnvironment: the dashboard's
// `claude --version` runs claude_path outside the sandbox, and a wrapper
// script there may run what a brain can change, so it gets what a brain
// gets and no more.
func TestClaudeVersionCheckGetsABrainsEnvironment(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\necho \"9.9 ${CLAUDE_CODE_OAUTH_TOKEN:-unset} ${GROQ_API_KEY:-unset} ${FRIDOLIN_TEST_SECRET:-unset}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("GROQ_API_KEY", "test-groq-key")
	t.Setenv("FRIDOLIN_TEST_SECRET", "test-secret")
	if got, want := ClaudeInfo(script, []string{"GROQ_API_KEY"}).Version, "9.9 test-oauth-token test-groq-key unset"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
}

// onlyEnv makes env the whole environment for the rest of the test, as the
// service file would be.
func onlyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	// It clears the environment of the whole process, under the feet of any
	// test running beside it. t.Setenv makes Go refuse it in a parallel test.
	t.Setenv("HOME", os.Getenv("HOME"))
	saved := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
	})
	os.Clearenv()
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOnlyEnvRefusesParallelTests: onlyEnv swaps the environment of the
// whole process, which a parallel test shares with the tests beside it.
func TestOnlyEnvRefusesParallelTests(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("onlyEnv cleared the environment in a parallel test")
			}
		}()
		onlyEnv(t, map[string]string{"HOME": "/Users/you"})
	})
}

// brainRunEnv runs one message through folder brain a, with config added to
// config.json, while the bridge's environment is env, a temp HOME and what
// the fake CLI needs. It returns the environment the CLI got, and HOME.
func brainRunEnv(t *testing.T, env map[string]string, configValue string) (map[string]string, string) {
	t.Helper()
	home := fsx.Canonical(t.TempDir())
	envLog := filepath.Join(t.TempDir(), "brain.env")
	all := maps.Clone(env)
	maps.Copy(all, map[string]string{"HOME": home, "FRIDOLIN_FAKE_CLAUDE": "1", "FRIDOLIN_FAKE_ENV_LOG": envLog})
	onlyEnv(t, all)

	m := testManifest("a")
	m.Routing.HandlesDMs = true
	if _, err := registry.Scaffold(filepath.Join(home, "a"), &m, brainTemplates); err != nil {
		t.Fatal(err)
	}
	approved, err := json.Marshal(m.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+os.Args[0]+`",
		"brain_dirs": [{"path": "`+home+`/a", "approved": `+string(approved)+`}]`+configValue+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)
	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
	if err := d.all()[0].res.Err; err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n") {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	return got, home
}

// TestPrepareBrainState: before a sandboxed run the bridge creates the
// brain's inbox and state folders, which the profile pins, and points the
// run at them: its own Claude Code config dir (unless it shares yours),
// TMPDIR, the CLI's temp dir and the tools' caches. A state folder that is
// a symlink is refused, since the profile allows it by name, and so is an
// id that cannot name a folder.
func TestPrepareBrainState(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherit_claude_user_config=%v", inherit), func(t *testing.T) {
			data := fsx.Canonical(t.TempDir())
			env, err := prepareBrainState(data, data+"/media", "a", inherit)
			if err != nil {
				t.Fatal(err)
			}
			state := data + "/brains/a"
			want := []string{
				"TMPDIR=" + state + "/tmp", "CLAUDE_CODE_TMPDIR=" + state + "/tmp", "TMPPREFIX=" + state + "/tmp/zsh",
				"XDG_CACHE_HOME=" + state + "/cache", "npm_config_cache=" + state + "/cache/npm",
			}
			if !inherit {
				want = append(want, "CLAUDE_CONFIG_DIR="+state+"/claude")
			}
			if !slices.Equal(env, want) {
				t.Errorf("env = %q, want %q", env, want)
			}
			for _, dir := range []string{state + "/claude", state + "/codex", state + "/tmp", state + "/cache", data + "/media/a"} {
				if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
					t.Errorf("%s: %v, %v", dir, fi, err)
				}
			}
			// The folders the sandbox profile names are these.
			if st := brainStateOf(data, "a"); !slices.Equal(st.folders(), []string{state + "/claude", state + "/codex", state + "/tmp", state + "/cache"}) || st.dir != state {
				t.Errorf("brainStateOf = %+v", st)
			}
		})
	}
	t.Run("a state folder that is a symlink", func(t *testing.T) {
		data := fsx.Canonical(t.TempDir())
		state := data + "/brains/a"
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), state+"/tmp"); err != nil {
			t.Fatal(err)
		}
		if env, err := prepareBrainState(data, data+"/media", "a", false); err == nil {
			t.Errorf("prepareBrainState = %q, want it refused", env)
		}
	})
	for name, c := range map[string]struct{ data, id string }{
		"no data dir":             {"", "a"},
		"an id that is no folder": {"data", "../b"},
	} {
		t.Run(name, func(t *testing.T) {
			data := c.data
			if data != "" {
				data = filepath.Join(t.TempDir(), data)
			}
			if env, err := prepareBrainState(data, "", c.id, false); err == nil {
				t.Errorf("prepareBrainState = %q, want it refused", env)
			}
		})
	}
}
