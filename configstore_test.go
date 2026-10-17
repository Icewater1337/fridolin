package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStoreUpdateWritesAtomicallyAndKeepsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, `{"data_dir":"~/data","allowed_numbers":["15551234567"]}`)
	cs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "data"); cs.Get().DataDir != want {
		t.Fatalf("effective DataDir = %q", cs.Get().DataDir)
	}

	etag := cs.ETag()
	restart, err := cs.Update(etag, func(c *Config) error {
		c.AllowedNumbers = append(c.AllowedNumbers, "15559876543")
		c.LogLevel = "debug"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(restart) != 1 || restart[0] != "log_level" {
		t.Errorf("restart = %v, want [log_level]", restart)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"~/data"`) {
		t.Error("the file must keep ~ as written, not the expanded path")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", st.Mode().Perm())
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("the previous version should be kept as .bak")
	}
	if got := cs.Get().AllowedNumbers; len(got) != 2 {
		t.Errorf("effective config not updated: %v", got)
	}

	// A stale etag is a conflict and changes nothing.
	if _, err := cs.Update(etag, func(c *Config) error { c.Model = "x"; return nil }); !errors.Is(err, ErrConfigConflict) {
		t.Errorf("stale etag: err = %v, want conflict", err)
	}
	// An invalid edit is rejected and changes nothing.
	if _, err := cs.Update("", func(c *Config) error { c.BrainEffort = "extreme"; return nil }); err == nil {
		t.Error("invalid effort must be rejected")
	}
	if cs.Get().BrainEffort != "" {
		t.Error("a rejected edit must not apply")
	}
}

func TestConfigStoreReportsEnvLockedFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALLOWED_GROUPS", "120363000000000000@g.us")
	cs, err := OpenConfigStore(writeConfig(t, `{"allowed_groups":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cs.EnvLocked()["allowed_groups"] != "ALLOWED_GROUPS" {
		t.Errorf("env locked = %v", cs.EnvLocked())
	}
	// Saving never writes the environment's value into the file.
	if _, err := cs.Update("", func(c *Config) error { c.Model = "sonnet"; return nil }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cs.Path())
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if g, _ := raw["allowed_groups"].([]any); len(g) != 0 {
		t.Errorf("environment value leaked into the file: %v", raw["allowed_groups"])
	}
}

func TestConfigDefaultsAndLoopbackOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadConfig(writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPListen != defaultHTTPListen || !cfg.HTTPEnabled() {
		t.Errorf("http_listen default = %q", cfg.HTTPListen)
	}
	if cfg.Queue.Mode != "queue" || cfg.DebounceQuietMS() != defaultQuietMS || !cfg.QueueNotify() || !cfg.DashboardRequiresToken() {
		t.Error("unexpected defaults")
	}
	if cfg.AllowsAnyDM() || cfg.AllowAnyDM == nil || *cfg.AllowAnyDM {
		t.Error("an unset allow_any_dm must mean false, and the effective config must say so")
	}
	if (&Config{}).AllowsAnyDM() {
		t.Error("a Config built without the defaults must not let anyone DM either")
	}
	cfg, err = LoadConfig(writeConfig(t, `{"http_listen":"off"}`))
	if err != nil || cfg.HTTPEnabled() {
		t.Error(`"off" must disable the HTTP server`)
	}
	if _, err := LoadConfig(writeConfig(t, `{"http_listen":"0.0.0.0:8765"}`)); err == nil {
		t.Error("a non-loopback http_listen must be rejected")
	}
}

func TestStarterConfigLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := ensureConfigFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowsAnyDM() {
		t.Error("a fresh install must not answer strangers")
	}
	// An older bridge rolled back to runs classic mode without the key.
	if raw := readRawConfig(t, path); string(raw["brain_mode"]) != "true" {
		t.Errorf(`starter config has "brain_mode": %s, want true`, raw["brain_mode"])
	}
}

// readRawConfig returns the top-level keys of the config file at path, as
// they are written.
func readRawConfig(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// Classic mode was removed. A config or environment that asks for it is
// refused, with what to do instead; quietly running brains would leave the
// chats with no brain set up. Without the switch, or with it on, it loads.
// The bridge reads BRAIN_MODE in a .env to refuse it, so the log must not
// call it ignored.
func TestConfigRefusesClassicMode(t *testing.T) {
	cases := []struct {
		name, config string
		env          string // BRAIN_MODE in the environment, "" for unset
		dotenv       string // the .env next to the config, "" for none
		want         string // part of the error, "" when the config loads
		fix          string // part of the error that says what to change
	}{
		{"no brain_mode", `{}`, "", "", "", ""},
		{"brain_mode true", `{"brain_mode": true}`, "", "", "", ""},
		{"brain_mode null", `{"brain_mode": null}`, "", "", "", ""},
		{"brain_mode false", `{"brain_mode": false}`, "", "", `"brain_mode": false asks for classic mode, which was removed`, `Set "brain_mode" to true in config.json`},
		{"brain_mode not a bool", `{"brain_mode": "off"}`, "", "", "brain_mode: json: cannot unmarshal", ""},
		{"BRAIN_MODE=0", `{}`, "0", "", "BRAIN_MODE=0 asks for classic mode, which was removed", "Unset BRAIN_MODE"},
		{"BRAIN_MODE=false over brain_mode true", `{"brain_mode": true}`, "false", "", "BRAIN_MODE=false asks for classic mode", "Unset BRAIN_MODE"},
		{"BRAIN_MODE=0 in the .env", `{}`, "", "BRAIN_MODE=0\n", "BRAIN_MODE=0 asks for classic mode", "Unset BRAIN_MODE"},
		{"BRAIN_MODE=1", `{}`, "1", "", "", ""},
		{"BRAIN_MODE=1 in the .env", `{}`, "", "BRAIN_MODE=1\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearEnv(t, "BRAIN_MODE")
			if c.env != "" {
				t.Setenv("BRAIN_MODE", c.env)
			}
			path := writeConfig(t, c.config)
			if c.dotenv != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(c.dotenv), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureLog(t)
			_, err := LoadConfig(path)
			for _, l := range logs() {
				if strings.Contains(l, "ignoring") && strings.Contains(l, "BRAIN_MODE") {
					t.Errorf("log %q calls BRAIN_MODE ignored", l)
				}
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("load: %v, want it to load", err)
			case c.want == "":
			case err == nil:
				t.Fatalf("loaded, want an error containing %q", c.want)
			case !strings.Contains(err.Error(), c.want):
				t.Errorf("error %q, want it to contain %q", err, c.want)
			case !strings.Contains(err.Error(), c.fix):
				t.Errorf("error %q does not say %q", err, c.fix)
			case c.fix != "" && !strings.Contains(err.Error(), "add a brain in the dashboard"):
				t.Errorf("error %q does not say how to switch to a brain", err)
			}
		})
	}
}

// Every save writes "brain_mode": true, whatever the file had: a bridge
// rolled back to an older version runs classic mode without it. (Go code
// cannot set it to false; a dashboard save that tries is refused, see
// TestPutConfigRefusesClassicMode.)
func TestConfigSavesKeepBrainModeOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeConfig(t, `{"log_level": "info"}`)
	cs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *Config) error { c.LogLevel = "debug"; return nil }); err != nil {
		t.Fatal(err)
	}
	if raw := readRawConfig(t, path); string(raw["brain_mode"]) != "true" {
		t.Errorf(`saved "brain_mode": %s, want true`, raw["brain_mode"])
	}
	if _, err := LoadConfig(path); err != nil {
		t.Errorf("the saved config does not load: %v", err)
	}
}

func TestConfigStoreRejectsTooSmallChunkSize(t *testing.T) {
	// The dashboard asks for at least 100 too, but the server must not rely
	// on it: a smaller limit would leave no room for the numbering.
	t.Setenv("HOME", t.TempDir())
	cs, err := OpenConfigStore(writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *Config) error { c.MaxChunkSize = 50; return nil }); err == nil {
		t.Fatal("max_chunk_size 50 must be rejected")
	}
	if got := cs.Get().MaxChunkSize; got != 3900 {
		t.Errorf("a rejected edit must not apply: MaxChunkSize = %d", got)
	}
}

func TestDMAccessFollowsAllowAnyDM(t *testing.T) {
	// Allowed senders run Claude with all its permissions, so only an
	// explicit allow_any_dm: true may open an empty allowlist to everyone.
	const stranger = "15559876543"
	tests := []struct {
		name   string
		config string
		number string
		want   bool
	}{
		{"unset and empty list refuses a stranger", `{}`, stranger, false},
		{"hand-written config without the key refuses a stranger", `{"brain_mode":true,"dangerously_skip_permissions":true}`, stranger, false},
		{"false and empty list refuses a stranger", `{"allow_any_dm":false}`, stranger, false},
		{"explicit true and empty list lets anyone in", `{"allow_any_dm":true}`, stranger, true},
		{"listed number is allowed", `{"allowed_numbers":["15551234567"]}`, "15551234567", true},
		{"unlisted number is refused", `{"allowed_numbers":["15551234567"]}`, stranger, false},
		{"true does not widen a non-empty list", `{"allowed_numbers":["15551234567"],"allow_any_dm":true}`, stranger, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg, err := LoadConfig(writeConfig(t, tc.config))
			if err != nil {
				t.Fatal(err)
			}
			if got := numberAllowed(cfg, tc.number); got != tc.want {
				t.Errorf("numberAllowed(%s) = %v, want %v", tc.number, got, tc.want)
			}
		})
	}
}

func TestRemovingTheLastNumberDoesNotOpenDMs(t *testing.T) {
	// A config from before allow_any_dm existed: once the dashboard removes
	// its last number, the list is empty and the flag is still unset.
	t.Setenv("HOME", t.TempDir())
	cs, err := OpenConfigStore(writeConfig(t, `{"allowed_numbers":["15551234567"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *Config) error { c.AllowedNumbers = []string{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if numberAllowed(cs.Get(), "15559876543") {
		t.Error("an empty list with allow_any_dm unset must refuse everyone")
	}
}

func TestDotEnvIsReadOnlyNextToTheConfig(t *testing.T) {
	// The tests run in the repository and a bridge may be started from a
	// checkout: a .env lying in the working directory must not configure it.
	tests := []struct {
		name        string
		beside, cwd string // .env contents next to config.json and in the working directory; "" means none
		want        string // effective model
	}{
		{"next to the config", "MODEL=beside\n", "", "beside"},
		{"only in the working directory", "", "MODEL=cwd\n", ""},
		{"in both places", "MODEL=beside\n", "MODEL=cwd\n", "beside"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearEnv(t, "MODEL")
			path := writeConfig(t, `{}`)
			if tc.beside != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(tc.beside), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wd := t.TempDir()
			if tc.cwd != "" {
				if err := os.WriteFile(filepath.Join(wd, ".env"), []byte(tc.cwd), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(wd)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model != tc.want {
				t.Errorf("Model = %q, want %q", cfg.Model, tc.want)
			}
		})
	}
}

func TestDotEnvOverridesWithoutExporting(t *testing.T) {
	// The .env configures the bridge only. Exported into the process
	// environment, its values would reach every brain and tool the bridge
	// starts, and would outlive the test that loaded them.
	tests := []struct {
		name        string
		env, dotenv string // MODEL in the process environment and in the .env; "" means unset
		want        string // effective model
	}{
		{"config.json alone", "", "", "from-file"},
		{"process environment", "from-env", "", "from-env"},
		{".env", "", "from-dotenv", "from-dotenv"},
		{".env wins over the process environment", "from-env", "from-dotenv", "from-dotenv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearEnv(t, "MODEL", "FRIDOLIN_TEST_DOTENV_ONLY")
			if tc.env != "" {
				t.Setenv("MODEL", tc.env)
			}
			path := writeConfig(t, `{"model":"from-file"}`)
			dotenv := "FRIDOLIN_TEST_DOTENV_ONLY=secret\n"
			if tc.dotenv != "" {
				dotenv += "MODEL=" + tc.dotenv + "\n"
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(dotenv), 0o600); err != nil {
				t.Fatal(err)
			}
			cs, err := OpenConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cs.Get().Model; got != tc.want {
				t.Errorf("Model = %q, want %q", got, tc.want)
			}
			if v, ok := os.LookupEnv("FRIDOLIN_TEST_DOTENV_ONLY"); ok {
				t.Errorf("a .env value was exported into the process environment: %q", v)
			}
			if got := os.Getenv("MODEL"); got != tc.env {
				t.Errorf("process MODEL = %q, want it left at %q", got, tc.env)
			}
			// An edit re-derives the effective config from the same overrides.
			if _, err := cs.Update("", func(c *Config) error { c.LogLevel = "debug"; return nil }); err != nil {
				t.Fatal(err)
			}
			if got := cs.Get().Model; got != tc.want {
				t.Errorf("Model after an edit = %q, want %q", got, tc.want)
			}
			if locked := cs.EnvLocked()["model"] == "MODEL"; locked != (tc.want != "from-file") {
				t.Errorf("model locked = %v, env locked = %v", locked, cs.EnvLocked())
			}
		})
	}
}

func TestBadDotEnvIsAnError(t *testing.T) {
	// Ignoring a .env that does not parse would run the bridge without the
	// settings it meant to set; the error must not quote its contents.
	const secret = "sk-not-a-real-secret"
	tests := []struct {
		name string
		body string // "" makes .env a directory
	}{
		{"unterminated quote", "MODEL=\"" + secret + "\n"},
		{"invalid variable name", "MY-KEY=" + secret + "\n"},
		{"unreadable", ""},
	}
	for _, tc := range tests {
		for _, relative := range []bool{false, true} {
			name := tc.name
			if relative {
				name += ", relative config path"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				path := writeConfig(t, `{}`)
				dotenv := filepath.Join(filepath.Dir(path), ".env")
				var err error
				if tc.body == "" {
					err = os.Mkdir(dotenv, 0o700)
				} else {
					err = os.WriteFile(dotenv, []byte(tc.body), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if relative {
					// ./config.json, which the bridge uses when no --config is
					// given: the error must still say which folder's .env it is.
					t.Chdir(filepath.Dir(path))
					path = "config.json"
				}
				_, err = OpenConfigStore(path)
				if err == nil {
					t.Fatal("a .env that cannot be read must stop the config from loading")
				}
				resolved := filepath.Join(canonicalDir(filepath.Dir(dotenv)), ".env")
				if !strings.Contains(err.Error(), dotenv) && !strings.Contains(err.Error(), resolved) {
					t.Errorf("error %q does not name %s", err, dotenv)
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q quotes the .env contents", err)
				}
			})
		}
	}
}

func TestDotEnvLogsTheKeysItIgnores(t *testing.T) {
	// Only the override variables are read from the .env and nothing in it is
	// exported, so a key meant for the Claude CLI or a brain's tool reaches
	// nothing. The log names such keys, so that does not go unnoticed, but
	// never their values.
	const secret = "sk-not-a-real-secret"
	tests := []struct {
		name   string
		dotenv string // "" means no .env
		want   string // what the log says about the .env; "" means nothing
	}{
		{"no .env", "", ""},
		{"only override variables", "MODEL=from-dotenv\nLOG_LEVEL=debug\n", ""},
		{"a key for a brain's tool", "MODEL=from-dotenv\nGROQ_API_KEY=" + secret + "\n", "ignoring GROQ_API_KEY ("},
		{"several keys, sorted", "PATH=/opt/bin\nANTHROPIC_API_KEY=" + secret + "\nCONFIG_FILE=/elsewhere.json\n", "ignoring ANTHROPIC_API_KEY, CONFIG_FILE, PATH ("},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := writeConfig(t, `{}`)
			dotenv := filepath.Join(filepath.Dir(path), ".env")
			if tc.dotenv != "" {
				if err := os.WriteFile(dotenv, []byte(tc.dotenv), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var logs strings.Builder
			defer log.SetOutput(log.Writer())
			log.SetOutput(&logs)
			cs, err := OpenConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			// The .env is read once, so an edit does not log it again.
			if _, err := cs.Update("", func(c *Config) error { c.LogLevel = "debug"; return nil }); err != nil {
				t.Fatal(err)
			}
			got := logs.String()
			if strings.Contains(got, secret) {
				t.Errorf("the log quotes a .env value: %q", got)
			}
			if tc.want == "" {
				if strings.Contains(got, "ignoring") {
					t.Errorf("nothing is ignored, but the log says %q", got)
				}
				return
			}
			if n := strings.Count(got, tc.want); n != 1 {
				t.Errorf("log %q has %q %d times, want once", got, tc.want, n)
			}
			if !strings.Contains(got, ".env "+dotenv+": ") {
				t.Errorf("log %q does not name %s", got, dotenv)
			}
		})
	}
}
