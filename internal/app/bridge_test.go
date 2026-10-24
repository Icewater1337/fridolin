package app

import (
	"cmp"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	setenv(t, "HOME", t.TempDir())
	cfg, err := loadConfig(t, writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudePath != "claude" {
		t.Errorf("ClaudePath = %q", cfg.ClaudePath)
	}
	if cfg.MaxChunkSize != 3900 {
		t.Errorf("MaxChunkSize = %d", cfg.MaxChunkSize)
	}
	if len(cfg.Triggers) != 1 || cfg.Triggers[0].Text != "@claude" {
		t.Errorf("Triggers = %v", cfg.Triggers)
	}
}

// max_media_mb defaults to 100 and must stay within what WhatsApp can send.
func TestLoadConfigMaxMediaMB(t *testing.T) {
	t.Parallel()
	cases := []struct {
		body string
		want int // 0: rejected
	}{
		{`{}`, 100},
		{`{"max_media_mb": 0}`, 100},
		{`{"max_media_mb": 1}`, 1},
		{`{"max_media_mb": 2048}`, 2048},
		{`{"max_media_mb": -1}`, 0},
		{`{"max_media_mb": 2049}`, 0},
	}
	for _, c := range cases {
		t.Run(c.body, func(t *testing.T) {
			setenv(t, "HOME", t.TempDir())
			cfg, err := loadConfig(t, writeConfig(t, c.body))
			if c.want == 0 {
				if err == nil {
					t.Errorf("loaded with max_media_mb %d, want an error", cfg.MaxMediaMB)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxMediaMB != c.want || (int64(cfg.MaxMediaMB)<<20) != int64(c.want)<<20 {
				t.Errorf("max_media_mb %d (%d bytes), want %d", cfg.MaxMediaMB, (int64(cfg.MaxMediaMB) << 20), c.want)
			}
		})
	}
	if got := (int64(resolvedSettings(t, &config.File{}).MaxMediaMB) << 20); got != 100<<20 {
		t.Errorf("a default config allows %d bytes, want the default", got)
	}
}

func TestLoadConfigExpandsHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)
	cfg, err := loadConfig(t, writeConfig(t, `{
		"data_dir": "~/data",
		"brain_mode": true,
		"brain_dirs": [{"path": "~/brains/a", "approved": {"id": "a", "name": "A"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "data"); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
	if want := filepath.Join(home, "brains", "a"); cfg.BrainDirs[0].Path != want {
		t.Errorf("brain folder = %q, want %q", cfg.BrainDirs[0].Path, want)
	}
}

// mkdirMode creates dir with exactly mode, whatever the umask.
func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func TestNewBridgeMakesDataDirPrivate(t *testing.T) {
	// Serial: captureLog takes over the process's standard logger, which
	// every bridge writes to.
	// protectDataDir does not run on config loads, so starting the bridge
	// is what must call it.
	home := fsx.Canonical(t.TempDir())
	setenv(t, "HOME", home)
	data := filepath.Join(home, "data")
	mkdirMode(t, data, 0o755)
	cfgs, err := openConfigStore(t, writeConfig(t, `{"brain_mode": true, "data_dir": "`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	b, err := New(Options{Config: cfgs, NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if got := permOf(t, data); got != 0o700 {
		t.Errorf("data dir has mode %04o after start, want 0700", got)
	}
	if !logContains(logs, data+" was open to other users") {
		t.Error("the change to the data dir was not logged")
	}
}

func TestConfigWritesLeaveFolderModesAlone(t *testing.T) {
	// Serial: captureLog takes over the process's standard logger, which
	// every bridge writes to.
	// effective() runs on every load and on every dashboard save. Changing
	// modes there would act on a folder typed into Settings before any
	// restart, and its warnings would repeat on every write.
	home := t.TempDir()
	setenv(t, "HOME", home)
	data := filepath.Join(home, "data")
	shared := filepath.Join(home, "shared")
	mkdirMode(t, data, 0o755)
	mkdirMode(t, shared, 0o755)
	logs := captureLog(t)

	cs, err := openConfigStore(t, writeConfig(t, `{"data_dir": "`+data+`", "db_path": "`+shared+`/bridge.db"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"debug", "info"} {
		if _, err := cs.Update("", func(c *config.File) error { c.LogLevel = level; return nil }); err != nil {
			t.Fatal(err)
		}
	}

	if got := permOf(t, data); got != 0o755 {
		t.Errorf("a config write changed the data dir to %04o", got)
	}
	if logContains(logs, "open to other users") {
		t.Error("a config write logged a warning about folder modes")
	}
}

func TestLoadConfigAllowsNoBrains(t *testing.T) {
	t.Parallel()
	// A fresh install starts with no brains and adds them in the dashboard.
	setenv(t, "HOME", t.TempDir())
	if _, err := loadConfig(t, writeConfig(t, `{"brain_mode":true,"brain_dirs":[]}`)); err != nil {
		t.Fatalf("an empty brain list must load: %v", err)
	}
}

func TestLoadConfigMaxChunkSize(t *testing.T) {
	t.Parallel()
	// Below 100 the "[i/N] " numbering leaves too little room for text;
	// such a limit is refused at load time rather than quietly changed.
	cases := []struct {
		name, body string
		want       int // 0: the config must be refused
	}{
		{"unset means the default", `{}`, 3900},
		{"zero means the default", `{"max_chunk_size":0}`, 3900},
		{"the minimum is allowed", `{"max_chunk_size":100}`, 100},
		{"a larger limit is kept", `{"max_chunk_size":5000}`, 5000},
		{"just below the minimum", `{"max_chunk_size":99}`, 0},
		{"smaller than the numbering", `{"max_chunk_size":5}`, 0},
		{"negative", `{"max_chunk_size":-1}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, "HOME", t.TempDir())
			cfg, err := loadConfig(t, writeConfig(t, tc.body))
			if tc.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "max_chunk_size") {
					t.Fatalf("expected a max_chunk_size error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxChunkSize != tc.want {
				t.Errorf("MaxChunkSize = %d, want %d", cfg.MaxChunkSize, tc.want)
			}
		})
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	t.Parallel()
	setenv(t, "HOME", t.TempDir())
	example, err := os.ReadFile(filepath.Join(repoDir, "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, string(example))
	if runtime.GOOS != "darwin" {
		// The example turns the sandbox on, which only macOS can honour.
		if _, err := loadConfig(t, cfgPath); err == nil || !strings.Contains(err.Error(), "sandbox.enabled") {
			t.Fatalf("config.example.json on %s: err = %v, want the sandbox.enabled error", runtime.GOOS, err)
		}
		// The rest of it must load all the same.
		var cfg map[string]any
		if err := json.Unmarshal(example, &cfg); err != nil {
			t.Fatal(err)
		}
		sandbox, _ := cfg["sandbox"].(map[string]any)
		if sandbox == nil {
			t.Fatal("config.example.json has no sandbox object")
		}
		sandbox["enabled"] = false
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		cfgPath = writeConfig(t, string(data))
	}
	if _, err := loadConfig(t, cfgPath); err != nil {
		t.Fatalf("config.example.json does not load: %v", err)
	}
}

func TestLogDMAccessWarnsWhenAnyoneCanDM(t *testing.T) {
	// Serial: captureLog takes over the process's standard logger, which
	// every bridge writes to.
	yes, no := true, false
	open := &config.File{AllowAnyDM: &yes}
	unset := &config.File{}
	listed := &config.File{AllowedNumbers: []string{"15551234567"}, AllowAnyDM: &yes}
	closed := &config.File{AllowAnyDM: &no}
	tests := []struct {
		name       string
		prev, next *config.File
		want       string // "" means nothing is logged
	}{
		{"startup with explicit true and an empty list", nil, open, "WARNING: anyone"},
		{"startup with the flag unset", nil, unset, "nobody can send"},
		{"startup with numbers listed", nil, listed, ""},
		{"edit that opens DMs", closed, open, "WARNING: anyone"},
		{"edit that keeps DMs open", open, open, ""},
		{"edit that closes DMs", open, closed, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			defer log.SetOutput(log.Writer())
			log.SetOutput(&buf)
			logDMAccess(resolvedSettings(t, tc.prev), resolvedSettings(t, tc.next))
			got := buf.String()
			if tc.want == "" && got != "" {
				t.Errorf("logged %q, want nothing", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("logged %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// ── brain routing ─────────────────────────────────────────────────────────

func newTestManager(t *testing.T, cfg *config.Settings) *BrainManager {
	t.Helper()
	requireTestDataDir(t, cfg)
	mgr, err := newBrainManager(cfg, envOf(t), *optionsOf(t).waits)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestBrainRouting(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)

	cfg, err := loadConfig(t, writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/p", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/t", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{"team@g.us"}}}),
		brainFolder(t, home+"/o", manifest.Manifest{ID: "other", Routing: manifest.Routing{Default: true}}),
	)+`
	}`))
	if err != nil {
		t.Fatal(err)
	}

	mgr := newTestManager(t, cfg)

	if got := mgr.BrainFor("15551234567@s.whatsapp.net", false); got.ID() != "personal" {
		t.Errorf("DM routed to %q, want personal", got.ID())
	}
	if got := mgr.BrainFor("team@g.us", true); got.ID() != "team" {
		t.Errorf("team group routed to %q, want team", got.ID())
	}
	// A group no brain claims falls back to the default brain.
	if got := mgr.BrainFor("unknown@g.us", true); got.ID() != "other" {
		t.Errorf("unknown group routed to %q, want other", got.ID())
	}
}

// Route honours a brain named on the message before routing by chat, and a
// named brain that is gone is never swapped for the chat's owner.
func TestRouteHonoursTheNamedBrain(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)
	cfg, err := loadConfig(t, writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/p", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
		brainFolder(t, home+"/t", manifest.Manifest{ID: "team", Routing: manifest.Routing{Groups: []string{"team@g.us"}}}),
	)+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	const dm = "15551234567@s.whatsapp.net"
	for _, c := range []struct {
		name string
		m    brain.Inbound
		want string // "" = no brain
	}{
		{"by chat", brain.Inbound{Inbound: chat.Inbound{JID: dm}}, "personal"},
		{"named brain wins over the chat's owner", brain.Inbound{Inbound: chat.Inbound{JID: dm, Brain: "team"}}, "team"},
		{"named brain in a group it does not own", brain.Inbound{Inbound: chat.Inbound{JID: "other@g.us", Brain: "personal"}}, "personal"},
		{"named brain is gone", brain.Inbound{Inbound: chat.Inbound{JID: dm, Brain: "removed"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			if br := mgr.Route(c.m); br != nil {
				got = br.ID()
			}
			if got != c.want {
				t.Errorf("Route = %q, want %q", got, c.want)
			}
		})
	}
	if _, _, _, err := mgr.Submit(brain.Inbound{Inbound: chat.Inbound{JID: dm, Brain: "removed", Text: "x"}}); err == nil {
		t.Error("Submit to a removed brain must fail, not run in the DM brain")
	}
}

// A DM goes by its chat JID, its other JID and the sender's phone number,
// which WhatsApp may use instead of each other; a group goes by its JID
// only.
func TestChatJIDs(t *testing.T) {
	t.Parallel()
	const (
		pn    = "15551234567@s.whatsapp.net"
		lid   = "15559876543@lid"
		group = "120363000000000000@g.us"
	)
	for _, c := range []struct {
		name string
		m    brain.Inbound
		want string
	}{
		{"DM by number", brain.Inbound{Inbound: chat.Inbound{JID: pn, Sender: "15551234567"}}, pn},
		{"DM by LID", brain.Inbound{Inbound: chat.Inbound{JID: lid, Sender: "15551234567"}}, lid + "," + pn},
		{"DM without a sender", brain.Inbound{Inbound: chat.Inbound{JID: lid}}, lid},
		{"DM by number, its LID known", brain.Inbound{Inbound: chat.Inbound{JID: pn, ChatAlt: lid, Sender: "15551234567"}}, pn + "," + lid},
		{"group", brain.Inbound{Inbound: chat.Inbound{JID: group, Sender: "15551234567"}}, group},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(chatJIDs(c.m), ","); got != c.want {
				t.Errorf("chatJIDs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBrainRoutingFallsBackToFirstBrain(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)

	cfg, err := loadConfig(t, writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data"`+brainDirsJSON(t, brainFolder(t, home+"/o", manifest.Manifest{ID: "only"}))+`
	}`))
	if err != nil {
		t.Fatal(err)
	}

	mgr := newTestManager(t, cfg)
	// No brain sets handles_dms, so DMs land on the fallback.
	if got := mgr.BrainFor("15551234567@s.whatsapp.net", false); got.ID() != "only" {
		t.Errorf("DM routed to %q, want only", got.ID())
	}
}

func TestSandboxProfileIsolatesBrains(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)

	cfg := &config.File{DataDir: filepath.Join(home, "data")}
	sp := NewSandboxProfiler(map[string][]string{
		"personal": {"/opt/brains/personal"},
		"team":     {"/opt/brains/team"},
	}, resolvedSettings(t, cfg))

	profile := mustProfile(t, sp, "personal")

	if !strings.Contains(profile, `(deny file-read* (subpath "/opt/brains/team"))`) {
		t.Error("personal's profile does not deny the team work dir")
	}
	if strings.Contains(profile, `(deny file-read* (subpath "/opt/brains/personal"))`) {
		t.Error("personal's profile denies its own work dir")
	}
}

// TestSandboxProfileDeniesSecretsAndTheKeychains: the keychains used to stay
// readable for the Claude CLI's own login, and with them every item that
// trusts /usr/bin/security, gh's token among them. Brains log in with a
// token now, so the keychains are denied like the other secrets, after the
// allow_paths allows, which therefore cannot open them.
func TestSandboxProfileDeniesSecretsAndTheKeychains(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)

	cfg := &config.File{DataDir: filepath.Join(home, "data"), Sandbox: config.SandboxConfig{AllowPaths: []string{filepath.Join(home, "Library")}}}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, resolvedSettings(t, cfg)), "a")

	for _, secret := range []string{".ssh", ".aws", ".gnupg", "Library/Mail", ".git-credentials", "Library/Keychains"} {
		if !strings.Contains(profile, filepath.Join(home, secret)) {
			t.Errorf("profile does not deny %s", secret)
		}
	}
	lines := strings.Split(profile, "\n")
	allow := slices.Index(lines, `(allow file-read* (subpath "`+filepath.Join(home, "Library")+`"))`)
	deny := slices.Index(lines, `(deny file-read* (subpath "`+filepath.Join(home, "Library", "Keychains")+`"))`)
	if allow < 0 || deny < allow {
		t.Errorf("the keychain deny (line %d) does not follow the allow_paths allow (line %d)", deny, allow)
	}
}

// TestSandboxProfileKeepsCalendarTokensFromBrains: the bridge makes a
// brain's calendar calls, so no brain may read a calendar token or the
// OAuth client copy older versions wrote next to it, its own included,
// whether they are in the data dir or in sandbox.credentials_dir.
func TestSandboxProfileKeepsCalendarTokensFromBrains(t *testing.T) {
	t.Parallel()
	for _, creds := range []string{"", "creds"} {
		t.Run("credentials_dir "+creds, func(t *testing.T) {
			home := t.TempDir()
			setenv(t, "HOME", home)
			cfg := &config.File{DataDir: filepath.Join(home, "data")}
			if creds != "" {
				cfg.Sandbox.CredentialsDir = filepath.Join(home, creds)
			}
			profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}, "b": {"/tmp/b"}}, resolvedSettings(t, cfg)), "a")
			if strings.Contains(profile, "google_calendar_") {
				t.Errorf("the profile opens a calendar file:\n%s", profile)
			}
			dir := cmp.Or(cfg.Sandbox.CredentialsDir, cfg.DataDir)
			if !strings.Contains(profile, `(deny file-read* (subpath "`+dir+`"))`) {
				t.Errorf("the profile does not deny %s", dir)
			}
		})
	}
}

func TestSandboxProfileIncludesConfiguredDenies(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	setenv(t, "HOME", home)

	cfg := &config.File{
		DataDir: filepath.Join(home, "data"),
		Sandbox: config.SandboxConfig{DenyPaths: []string{"/private/clients"}},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, resolvedSettings(t, cfg)), "a")

	if !strings.Contains(profile, `(deny file-read* (subpath "/private/clients"))`) {
		t.Error("configured deny path missing from the profile")
	}
}

// ── identity map ──────────────────────────────────────────────────────────

// ── misc ──────────────────────────────────────────────────────────────────

// Some names are read outside Go: the dashboard labels the run outcomes
// and the bridge's backup and offers the queue and observe modes. Only
// this test keeps the two sides in step.
func TestNamesReadOutsideGo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file string
		want []string
	}{
		{"web/assets/components/brain.js", []string{"ok" + ": [", "error" + ": [", "interrupted" + ": [", "question" + ": ["}},
		{"web/assets/pages/settings.js", []string{"def: '" + config.QueueModeQueue + "'", "['" + config.QueueModeQueue + "', ", "['" + config.QueueModeInterrupt + "', "}},
		{"web/assets/lib/brain.js", []string{"export const BRIDGE_BACKUP_ID = '" + backup.BridgeID + "';", "[['" + manifest.ObserveHistory + "', ", "['" + manifest.ObserveRun + "', "}},
		{"web/assets/pages/brain.js", []string{"m.observe || '" + manifest.ObserveHistory + "'"}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(repoDir, c.file))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(string(data), w) {
					t.Errorf("%s does not have %s", c.file, w)
				}
			}
		})
	}
}

// The bridge's backup id is spelt out in lib/brain.js alone, which
// TestNamesReadOutsideGo holds to bridgeBackupID; the other modules
// import it from there, so none can be left behind when it changes.
func TestDashboardSpellsTheBridgesBackupIDOnce(t *testing.T) {
	t.Parallel()
	assets := filepath.Join(repoDir, "web", "assets")
	err := filepath.WalkDir(assets, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".js" || path == filepath.Join(assets, "lib", "brain.js") {
			return err
		}
		js, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(js), "'"+backup.BridgeID+"'") {
			rel, _ := filepath.Rel(repoDir, path)
			t.Errorf("%s spells out %q; import BRIDGE_BACKUP_ID from lib/brain.js", rel, backup.BridgeID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
