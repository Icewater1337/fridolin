package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

func TestConfigStoreUpdateWritesAtomicallyAndKeepsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, `{"data_dir":"~/data","allowed_numbers":["15551234567"]}`)
	cs, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "data"); cs.Get().DataDir != want {
		t.Fatalf("effective DataDir = %q", cs.Get().DataDir)
	}

	etag := cs.ETag()
	restart, err := cs.Update(etag, func(c *config.Config) error {
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
	if _, err := cs.Update(etag, func(c *config.Config) error { c.BrainModel = "x"; return nil }); !errors.Is(err, config.ErrConflict) {
		t.Errorf("stale etag: err = %v, want conflict", err)
	}
	// An invalid edit is rejected and changes nothing.
	if _, err := cs.Update("", func(c *config.Config) error { c.BrainEffort = "extreme"; return nil }); err == nil {
		t.Error("invalid effort must be rejected")
	}
	if cs.Get().BrainEffort != "" {
		t.Error("a rejected edit must not apply")
	}
}

// The configuration comes from config.json alone. These variables used to
// override it, and their generic names are easily set by other software: a
// DATA_DIR meant for something else moved the bridge's data, and every one
// of them locked its setting in the dashboard.
func TestEnvironmentDoesNotOverrideTheConfig(t *testing.T) {
	// $ELSEWHERE is a dir of the subtest's own, so a regression that
	// creates it is blamed on the one variable that did.
	vars := []struct{ name, value string }{
		{"ALLOWED_NUMBERS", "15559876543"},
		{"ALLOWED_GROUPS", "120363000000000001@g.us"},
		{"TRIGGERS", "@bot"},
		{"CLAUDE_PATH", "$ELSEWHERE/claude"},
		{"DATA_DIR", "$ELSEWHERE"},
		{"DB_PATH", "$ELSEWHERE/bridge.db"},
		{"MEDIA_DIR", "$ELSEWHERE/media"},
		{"MAX_CHUNK_SIZE", "4000"},
		{"MODEL", "from-env"},
		{"BRAIN_MODEL", "from-env"},
		{"BRAIN_EFFORT", "high"},
		{"DISALLOWED_TOOLS", "Bash"},
		{"DANGEROUSLY_SKIP_PERMISSIONS", "false"},
		{"HTTP_LISTEN", "127.0.0.1:9999"},
		{"LOG_LEVEL", "debug"},
		{"SANDBOX_ENABLED", "true"},
		{"OTP_GUARD", "false"},
		{"VIEWER", "true"},
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, `{
		"allowed_numbers": ["15551234567"],
		"allowed_groups": ["120363000000000000@g.us"],
		"triggers": ["@claude"],
		"data_dir": "`+home+`/data",
		"max_chunk_size": 3000,
		"model": "from-file",
		"brain_model": "from-file",
		"brain_effort": "low",
		"disallowed_tools": ["WebFetch"],
		"dangerously_skip_permissions": true,
		"log_level": "info",
		"sandbox": {"enabled": false},
		"otp_guard": true,
		"viewer": false
	}`)
	for _, v := range vars {
		clearEnv(t, v.name)
	}
	want := effectiveJSON(t, path)
	for _, v := range vars {
		t.Run(v.name, func(t *testing.T) {
			elsewhere := filepath.Join(t.TempDir(), "elsewhere")
			value := strings.ReplaceAll(v.value, "$ELSEWHERE", elsewhere)
			t.Setenv(v.name, value)
			if got := effectiveJSON(t, path); got != want {
				t.Errorf("%s=%s changed the effective config:\n got %s\nwant %s", v.name, value, got, want)
			}
			if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
				t.Errorf("%s=%s made the bridge create %s (%v)", v.name, value, elsewhere, err)
			}
		})
	}
}

// effectiveJSON loads the config at path and returns the effective
// configuration as JSON.
func effectiveJSON(t *testing.T, path string) string {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConfigDefaultsAndLoopbackOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := loadConfig(writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPListen != config.DefaultHTTPListen || !cfg.HTTPEnabled() {
		t.Errorf("http_listen default = %q", cfg.HTTPListen)
	}
	if cfg.Queue.Mode != "queue" || cfg.DebounceQuietMS() != config.DefaultQuietMS || !cfg.QueueNotify() || !cfg.DashboardRequiresToken() {
		t.Error("unexpected defaults")
	}
	if cfg.AllowsAnyDM() || cfg.AllowAnyDM == nil || *cfg.AllowAnyDM {
		t.Error("an unset allow_any_dm must mean false, and the effective config must say so")
	}
	if (&config.Config{}).AllowsAnyDM() {
		t.Error("a Config built without the defaults must not let anyone DM either")
	}
	cfg, err = loadConfig(writeConfig(t, `{"http_listen":"off"}`))
	if err != nil || cfg.HTTPEnabled() {
		t.Error(`"off" must disable the HTTP server`)
	}
	if _, err := loadConfig(writeConfig(t, `{"http_listen":"0.0.0.0:8765"}`)); err == nil {
		t.Error("a non-loopback http_listen must be rejected")
	}
}

func TestStarterConfigLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := ensureConfigFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
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
func TestConfigRefusesClassicMode(t *testing.T) {
	cases := []struct {
		name, config string
		env          string // BRAIN_MODE in the environment, "" for unset
		want         string // part of the error, "" when the config loads
		fix          string // part of the error that says what to change
	}{
		{"no brain_mode", `{}`, "", "", ""},
		{"brain_mode true", `{"brain_mode": true}`, "", "", ""},
		{"brain_mode null", `{"brain_mode": null}`, "", "", ""},
		{"brain_mode false", `{"brain_mode": false}`, "", `"brain_mode": false asks for classic mode, which was removed`, `Set "brain_mode" to true in config.json`},
		{"brain_mode not a bool", `{"brain_mode": "off"}`, "", "brain_mode: json: cannot unmarshal", ""},
		{"BRAIN_MODE=0", `{}`, "0", "BRAIN_MODE=0 asks for classic mode, which was removed", "Unset BRAIN_MODE"},
		{"BRAIN_MODE=false over brain_mode true", `{"brain_mode": true}`, "false", "BRAIN_MODE=false asks for classic mode", "Unset BRAIN_MODE"},
		{"BRAIN_MODE=1", `{}`, "1", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearEnv(t, "BRAIN_MODE")
			if c.env != "" {
				t.Setenv("BRAIN_MODE", c.env)
			}
			_, err := loadConfig(writeConfig(t, c.config))
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
	cs, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "debug"; return nil }); err != nil {
		t.Fatal(err)
	}
	if raw := readRawConfig(t, path); string(raw["brain_mode"]) != "true" {
		t.Errorf(`saved "brain_mode": %s, want true`, raw["brain_mode"])
	}
	if _, err := loadConfig(path); err != nil {
		t.Errorf("the saved config does not load: %v", err)
	}
}

// Inline brains were removed with `fridolin migrate`, which moved them into
// their folders. A config that still lists some is refused, naming a build
// that can still move them: running without them would leave their chats
// to the default brain. An empty or null list loads, see
// TestRetiredConfigKeys.
func TestConfigRefusesInlineBrains(t *testing.T) {
	cases := []struct {
		name, config string
		want         string // part of the error
	}{
		{"one inline brain", `{"brains": [{"name": "a", "work_dir": "~/a", "handles_dms": true}]}`, `lists brains inline under "brains", which this version no longer runs`},
		{"not a list", `{"brains": {"name": "a"}}`, "brains: json: cannot unmarshal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			_, err := openConfigStore(writeConfig(t, c.config))
			switch {
			case err == nil:
				t.Fatalf("loaded, want an error containing %q", c.want)
			case !strings.Contains(err.Error(), c.want):
				t.Errorf("error %q, want it to contain %q", err, c.want)
			}
			if strings.Contains(c.want, "inline") {
				for _, part := range []string{
					`run "fridolin migrate" with a build from before this change (commit ` + config.MigrateBuild + ")",
					"see docs/BRAINS.md#configs-with-inline-brains",
				} {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("error %q does not say %q", err, part)
					}
				}
			}
		})
	}
}

// Keys older versions wrote and this one has no setting for still load.
// Each is logged once, as retired rather than unknown, and the next save
// leaves it out. model is the one with a value to keep: brains fell back
// to it while brain_model was empty, so it becomes brain_model then, and
// they run with the same model as before.
func TestRetiredConfigKeys(t *testing.T) {
	cases := []struct {
		name, config string
		brainModel   string   // the effective brain_model
		logged       []string // the retired keys the log names, in order
		note         string   // part of the log line, if one is logged
	}{
		{"none", `{"brain_model": "opus"}`, "opus", nil, ""},
		{"model without brain_model", `{"model": "sonnet"}`, "sonnet", []string{"model"}, `brain_model is now "sonnet"`},
		{"model with an empty brain_model", `{"model": "sonnet", "brain_model": ""}`, "sonnet", []string{"model"}, `brain_model is now "sonnet"`},
		{"model and brain_model", `{"model": "sonnet", "brain_model": "opus"}`, "opus", []string{"model"}, `brains use brain_model "opus", so "sonnet" went unused`},
		{"empty model", `{"model": ""}`, "", []string{"model"}, "it was empty"},
		{"work_dir", `{"work_dir": "~/classic"}`, "", []string{"work_dir"}, "classic mode"},
		{"empty brains", `{"brains": []}`, "", []string{"brains"}, "brain_dirs"},
		{"null brains", `{"brains": null}`, "", []string{"brains"}, "brain_dirs"},
		{"all of them", `{"brains": [], "work_dir": "~", "model": "haiku"}`, "haiku", []string{"model", "work_dir", "brains"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := writeConfig(t, c.config)
			logs := captureLog(t)
			cs, err := openConfigStore(path)
			if err != nil {
				t.Fatalf("load: %v, want it to load", err)
			}
			if got := cs.Get().BrainModel; got != c.brainModel {
				t.Errorf("brain_model = %q, want %q", got, c.brainModel)
			}
			var logged []string
			for _, l := range logs() {
				if strings.Contains(l, "ignoring unknown settings") {
					t.Errorf("logged as unknown: %q", l)
				}
				if _, rest, ok := strings.Cut(l, "config "+path+`: "`); ok {
					key, _, _ := strings.Cut(rest, `"`)
					logged = append(logged, key)
					if !strings.Contains(l, "is retired and is dropped the next time the dashboard saves") {
						t.Errorf("log line %q does not say the key is dropped on the next save", l)
					}
					if c.note != "" && !strings.Contains(l, c.note) {
						t.Errorf("log line %q does not say %q", l, c.note)
					}
				}
			}
			if !slices.Equal(logged, c.logged) {
				t.Errorf("logged retired keys %q, want %q (log %q)", logged, c.logged, logs())
			}

			if _, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "debug"; return nil }); err != nil {
				t.Fatal(err)
			}
			raw := readRawConfig(t, path)
			for _, name := range config.RetiredKeyNames() {
				if v, ok := raw[name]; ok {
					t.Errorf("a save wrote %q: %s", name, v)
				}
			}
			var saved string
			_ = json.Unmarshal(raw["brain_model"], &saved)
			if saved != c.brainModel {
				t.Errorf("saved brain_model %q, want %q", saved, c.brainModel)
			}
			if got := cs.Get().BrainModel; got != c.brainModel {
				t.Errorf("brain_model after a save = %q, want %q", got, c.brainModel)
			}
		})
	}
}

// The live config has model and no brain_model, and its brains name no
// model of their own: they must keep running with that model.
func TestBrainsKeepTheRetiredModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := loadConfig(writeConfig(t, `{"model": "sonnet"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := settingsFor(cfg, manifest.Spec{Manifest: manifest.Manifest{ID: "a"}}).model; got != "sonnet" {
		t.Errorf("a brain without a model of its own runs %q, want sonnet", got)
	}
	if got := settingsFor(cfg, manifest.Spec{Manifest: manifest.Manifest{ID: "a", Model: "opus"}}).model; got != "opus" {
		t.Errorf("a brain with model opus runs %q, want opus", got)
	}
}

// The data dir from before the rename is still found when ~/.fridolin
// does not exist, and the log says so, until data_dir names it.
func TestLegacyDataDir(t *testing.T) {
	cases := []struct {
		name        string
		dirs        []string // made in the home folder
		config      string
		want        string // the data dir, relative to home
		wantWarning bool
	}{
		{"fresh install", nil, `{}`, ".fridolin", false},
		{"only the old one", []string{".whatsapp-claude-bridge"}, `{}`, ".whatsapp-claude-bridge", true},
		{"both", []string{".fridolin", ".whatsapp-claude-bridge"}, `{}`, ".fridolin", false},
		{"named in data_dir", []string{".whatsapp-claude-bridge"}, `{"data_dir": "~/.whatsapp-claude-bridge"}`, ".whatsapp-claude-bridge", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, d := range c.dirs {
				if err := os.Mkdir(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureLog(t)
			cs, err := openConfigStore(writeConfig(t, c.config))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := cs.Get().DataDir, filepath.Join(home, c.want); got != want {
				t.Errorf("data dir %s, want %s", got, want)
			}
			if got := logContains(logs, "has the name from before the rename"); got != c.wantWarning {
				t.Errorf("warned %v, want %v (log %q)", got, c.wantWarning, logs())
			}
		})
	}
}

func TestConfigStoreRejectsTooSmallChunkSize(t *testing.T) {
	// The dashboard asks for at least 100 too, but the server must not rely
	// on it: a smaller limit would leave no room for the numbering.
	t.Setenv("HOME", t.TempDir())
	cs, err := openConfigStore(writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *config.Config) error { c.MaxChunkSize = 50; return nil }); err == nil {
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
			cfg, err := loadConfig(writeConfig(t, tc.config))
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
	cs, err := openConfigStore(writeConfig(t, `{"allowed_numbers":["15551234567"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *config.Config) error { c.AllowedNumbers = []string{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if numberAllowed(cs.Get(), "15559876543") {
		t.Error("an empty list with allow_any_dm unset must refuse everyone")
	}
}

func TestDotEnvIsNotRead(t *testing.T) {
	// A .env sets nothing, next to config.json, where older versions read
	// settings from, or in the working directory. One next to the config is
	// named in the log, so settings that stopped applying do not go
	// unnoticed, but nothing in it is: it may hold secrets. Nor does it reach
	// the process environment, which every brain inherits.
	const secret = "sk-not-a-real-secret"
	tests := []struct {
		name        string
		beside, cwd string // .env contents next to config.json and in the working directory; "" means none
		dir         bool   // the .env next to config.json is a directory
		relative    bool   // load ./config.json from its folder
		warn        bool   // the log names the .env next to config.json
	}{
		{"no .env", "", "", false, false, false},
		{"next to the config", "MODEL=beside\nFRIDOLIN_TEST_DOTENV_ONLY=" + secret + "\n", "", false, false, true},
		{"next to a relative config path", "MODEL=beside\n", "", false, true, true},
		{"one older versions refused to parse", "MODEL=\"" + secret + "\n", "", false, false, true},
		{"a directory", "", "", true, false, true},
		{"only in the working directory", "", "MODEL=cwd\n", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearEnv(t, "MODEL", "FRIDOLIN_TEST_DOTENV_ONLY")
			path := writeConfig(t, `{"brain_model":"from-file"}`)
			dotenv := filepath.Join(filepath.Dir(path), ".env")
			var err error
			switch {
			case tc.dir:
				err = os.Mkdir(dotenv, 0o700)
			case tc.beside != "":
				err = os.WriteFile(dotenv, []byte(tc.beside), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			wd := t.TempDir()
			if tc.cwd != "" {
				if err := os.WriteFile(filepath.Join(wd, ".env"), []byte(tc.cwd), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(wd)
			if tc.relative {
				t.Chdir(filepath.Dir(path))
				path = "config.json"
			}
			logs := captureLog(t)
			cs, err := openConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cs.Get().BrainModel; got != "from-file" {
				t.Errorf("BrainModel = %q, want the config's from-file", got)
			}
			// An edit re-derives the effective config from the file alone.
			if _, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "debug"; return nil }); err != nil {
				t.Fatal(err)
			}
			if got := cs.Get().BrainModel; got != "from-file" {
				t.Errorf("BrainModel after an edit = %q, want the config's from-file", got)
			}
			if v, ok := os.LookupEnv("FRIDOLIN_TEST_DOTENV_ONLY"); ok {
				t.Errorf("a .env value reached the process environment: %q", v)
			}
			if v, ok := os.LookupEnv("MODEL"); ok {
				t.Errorf("a .env value reached the process environment: MODEL=%q", v)
			}
			got := strings.Join(logs(), "\n")
			if strings.Contains(got, secret) {
				t.Errorf("the log quotes the .env: %q", got)
			}
			resolved := filepath.Join(fsx.Canonical(filepath.Dir(dotenv)), ".env")
			n := strings.Count(got, dotenv+" is no longer read")
			if resolved != dotenv {
				n += strings.Count(got, resolved+" is no longer read")
			}
			switch {
			case tc.warn && n != 1:
				t.Errorf("log %q names %s %d times, want once", got, dotenv, n)
			case !tc.warn && strings.Contains(got, ".env"):
				t.Errorf("log %q mentions a .env, but there is none next to the config", got)
			}
		})
	}
}

// The store migrates in memory only: starting writes nothing, and the next
// save writes the current version. A file of a newer version stops the
// bridge before it can save it without the settings it does not know, and
// a dashboard save of one is refused.
func TestConfigVersionOnDisk(t *testing.T) {
	t.Run("a version 1 file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		const body = `{"model": "sonnet", "log_level": "info"}`
		path := writeConfig(t, body)
		cs, err := openConfigStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(path); string(data) != body {
			t.Errorf("starting rewrote the file:\n%s", data)
		}
		if v := cs.FileCopy().Version; v != config.Version {
			t.Errorf("the file layer is version %d, want %d", v, config.Version)
		}
		if _, err := cs.Update(cs.ETag(), func(c *config.Config) error { c.LogLevel = "debug"; return nil }); err != nil {
			t.Fatal(err)
		}
		raw := readRawConfig(t, path)
		if string(raw["version"]) != "2" || string(raw["brain_model"]) != `"sonnet"` || raw["model"] != nil {
			t.Errorf("saved version %s, brain_model %s, model %s; want 2, \"sonnet\" and no model", raw["version"], raw["brain_model"], raw["model"])
		}
		if _, err := openConfigStore(path); err != nil {
			t.Errorf("the saved file does not load: %v", err)
		}
	})

	t.Run("no file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		path := filepath.Join(t.TempDir(), "config.json")
		cs, err := openConfigStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cs.Update(cs.ETag(), func(c *config.Config) error { c.Version = 0; return nil }); err != nil {
			t.Fatal(err)
		}
		if v := string(readRawConfig(t, path)["version"]); v != "2" {
			t.Errorf("the first save wrote version %s, want 2", v)
		}
	})

	t.Run("a version 2 file with a retired key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		path := writeConfig(t, `{"version": 2, "model": "sonnet"}`)
		logs := captureLog(t)
		cs, err := openConfigStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := cs.Get().BrainModel; got != "" {
			t.Errorf("brain_model = %q, want none: model is no key of version 2", got)
		}
		if !slices.ContainsFunc(logs(), func(l string) bool { return strings.Contains(l, "ignoring unknown settings [model]") }) {
			t.Errorf("model not logged as unknown: %q", logs())
		}
	})

	t.Run("a newer file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		const body = `{"version": 3, "allowed_numbers": ["15551234567"], "a_newer_setting": true}`
		path := writeConfig(t, body)
		_, err := openConfigStore(path)
		if err == nil || !errors.Is(err, config.ErrTooNew) {
			t.Fatalf("err = %v, want errConfigTooNew", err)
		}
		for _, part := range []string{"version 3 is newer than the version 2", "Run that newer version again", "docs/CONFIGURATION.md#version"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("error %q does not say %q", err, part)
			}
		}
		if data, _ := os.ReadFile(path); string(data) != body {
			t.Errorf("the refused file changed:\n%s", data)
		}
	})

	t.Run("a newer dashboard save", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		cs, err := openConfigStore(writeConfig(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		whole, err := json.Marshal(cs.FileCopy())
		if err != nil {
			t.Fatal(err)
		}
		newer := strings.Replace(string(whole), `"version":2`, `"version":3`, 1)
		if _, err := config.DecodeReplacement([]byte(newer)); !errors.Is(err, config.ErrTooNew) {
			t.Errorf("a version 3 save: err = %v, want errConfigTooNew", err)
		}
		if _, err := config.DecodeReplacement(whole); err != nil {
			t.Errorf("a version 2 save: %v", err)
		}
	})
}
