package main

import (
	"cmp"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

// ── junk filter ───────────────────────────────────────────────────────────

func TestIsJunkMessage(t *testing.T) {
	junk := []string{"ok", "OK", "thanks", "👍", "...", "?", "a", "🎉🎉"}
	for _, s := range junk {
		if !isJunkMessage(s) {
			t.Errorf("isJunkMessage(%q) = false, want true", s)
		}
	}

	real := []string{"what time is it", "ok but why", "book it for 3pm", "PR 42?", "привет", "你好", "שלום!", "ßß"}
	for _, s := range real {
		if isJunkMessage(s) {
			t.Errorf("isJunkMessage(%q) = true, want false", s)
		}
	}
}

// FuzzIsJunkMessage: untagged group messages the filter calls junk never
// get a run, not even in a brain that observes with runs (observeRuns),
// so it may only catch what carries nothing: a single character, no
// letter or digit in any script, or one of the short acknowledgements,
// which are ASCII once lowercased the way the filter compares them.
// Whitespace around a message and its ASCII letter case never change the
// verdict.
func FuzzIsJunkMessage(f *testing.F) {
	for _, s := range []string{"ok", "OK", "o\u212a", "than\u212a you", "thanks", "thank you", "👍", "...", "?", "a", "🎉🎉", "what time is it", "ok but why", "book it for 3pm", "PR 42?", "", " \n", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got := isJunkMessage(text)
		if padded := isJunkMessage(" \t" + text + "\n "); padded != got {
			t.Errorf("isJunkMessage(%q) = %v, but %v with whitespace around it", text, got, padded)
		}
		if swapped := swapASCIICase(text); isJunkMessage(swapped) != got {
			t.Errorf("isJunkMessage(%q) = %v, but not for %q", text, got, swapped)
		}
		trimmed := strings.TrimSpace(text)
		short := utf8.RuneCountInString(trimmed) <= 1
		content := strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
		if (short || !content) && !got {
			t.Errorf("isJunkMessage(%q) = false for a message without content", text)
		}
		// Lowercased like the filter does: strings.ToLower turns some
		// non-ASCII letters into ASCII ones, so "o\u212a" is "ok".
		lower := strings.ToLower(trimmed)
		if got && !short && content && (len(lower) > len("thank you") || !isASCII(lower)) {
			t.Errorf("isJunkMessage(%q) = true for a message with letters or digits", text)
		}
	})
}

// swapASCIICase turns ASCII lowercase letters into uppercase and back,
// leaving every other byte as it is.
func swapASCIICase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z':
			b[i] = c - 'a' + 'A'
		case 'A' <= c && c <= 'Z':
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// ── chunking ──────────────────────────────────────────────────────────────

func TestChunkMessageShortTextUntouched(t *testing.T) {
	chunks := ChunkMessage("hello", 100)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("got %v", chunks)
	}
}

func TestChunkMessageRespectsMaxSize(t *testing.T) {
	text := strings.Repeat("paragraph one is here.\n\n", 200)
	chunks := ChunkMessage(text, 500)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > 500 {
			t.Errorf("chunk %d has %d characters, over the 500 limit", i, n)
		}
		if !strings.HasPrefix(c, "[") {
			t.Errorf("chunk %d missing [n/total] prefix: %q", i, c[:20])
		}
	}
}

// ── config ────────────────────────────────────────────────────────────────

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := loadConfig(writeConfig(t, `{}`))
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
			t.Setenv("HOME", t.TempDir())
			cfg, err := loadConfig(writeConfig(t, c.body))
			if c.want == 0 {
				if err == nil {
					t.Errorf("loaded with max_media_mb %d, want an error", cfg.MaxMediaMB)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxMediaMB != c.want || cfg.MaxMediaBytes() != int64(c.want)<<20 {
				t.Errorf("max_media_mb %d (%d bytes), want %d", cfg.MaxMediaMB, cfg.MaxMediaBytes(), c.want)
			}
		})
	}
	if got := (&config.Config{}).MaxMediaBytes(); got != 100<<20 {
		t.Errorf("an unfilled config allows %d bytes, want the default", got)
	}
}

func TestLoadConfigExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := loadConfig(writeConfig(t, `{
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

func TestProtectDataDirMakesExistingDataDirPrivate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		relative bool // data_dir "data", started from HOME
		wantLog  bool
	}{
		{"world-readable", 0o755, false, true},
		{"group-readable", 0o750, false, true},
		{"writable by everyone", 0o777, false, true},
		{"already private", 0o700, false, false},
		// The log names the folder by its full path, not by the relative one.
		{"relative path", 0o755, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			data := filepath.Join(home, "data")
			mkdirMode(t, data, tc.mode)
			dataDir := data
			if tc.relative {
				t.Chdir(home)
				dataDir = "data"
			}
			logs := captureLog(t)

			cfg, err := loadConfig(writeConfig(t, `{"data_dir": "`+dataDir+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			protectDataDir(cfg)
			if got := permOf(t, data); got != 0o700 {
				t.Errorf("data dir has mode %04o, want 0700", got)
			}
			if got := logContains(logs, data+" was open to other users"); got != tc.wantLog {
				t.Errorf("logged the change = %v, want %v", got, tc.wantLog)
			}
		})
	}
}

func TestProtectDataDirLeavesHomeAlone(t *testing.T) {
	// Making the home folder 0700 would cut other users and services off
	// from all of it, so a data_dir there or above it only gets a warning.
	for _, tc := range []struct {
		name    string
		dataDir func(root, home string) string
		mode    os.FileMode
		warn    bool
	}{
		{"home folder", func(_, _ string) string { return "~" }, 0o755, true},
		{"home folder by path", func(_, home string) string { return home }, 0o755, true},
		{"parent of home", func(root, _ string) string { return root }, 0o755, true},
		{"private home folder", func(_, _ string) string { return "~" }, 0o700, false},
		// Relative paths depend on where the bridge was started; these
		// tests start it from HOME.
		{"home folder as relative path", func(_, _ string) string { return "." }, 0o755, true},
		{"parent of home as relative path", func(_, _ string) string { return ".." }, 0o755, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fsx.Canonical(t.TempDir())
			home := filepath.Join(root, "home")
			mkdirMode(t, home, tc.mode)
			if err := os.Chmod(root, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Chdir(home)
			cfg, err := loadConfig(writeConfig(t, `{"data_dir": "`+tc.dataDir(root, home)+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			logs := captureLog(t)

			protectDataDir(cfg)

			for _, dir := range []string{root, home} {
				if got := permOf(t, dir); got != tc.mode {
					t.Errorf("%s changed from %04o to %04o", dir, tc.mode, got)
				}
			}
			want := cfg.DataDir
			if !filepath.IsAbs(want) {
				want = filepath.Join(home, want)
			}
			if got := logContains(logs, "warning: data_dir "+want+" is your home folder or one above it"); got != tc.warn {
				t.Errorf("warned = %v, want %v", got, tc.warn)
			}
		})
	}
}

func TestProtectDataDirOnlyWarnsAboutOpenDBFolder(t *testing.T) {
	// A db_path outside the data dir may live in a folder shared on purpose.
	for _, tc := range []struct {
		name     string
		dataDir  string // relative to HOME; "link" is a symlink to HOME/data
		dbDir    string // relative to HOME; HOME/data is the data dir's folder
		mode     os.FileMode
		wantWarn bool
		advice   bool // the warning suggests chmod 700
	}{
		{"open to others", "data", "shared", 0o755, true, true},
		{"private", "data", "shared", 0o700, false, false},
		// Inside the data dir it is the bridge's own folder: no warning, and
		// only the data dir itself is made private.
		{"inside the data dir", "data", "data/db", 0o755, false, false},
		{"inside the data dir through a symlink", "link", "data/db", 0o755, false, false},
		// A 0700 home folder is what the data dir rule avoids, so the
		// warning does not suggest it.
		{"home folder", "data", ".", 0o755, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			mkdirMode(t, filepath.Join(home, "data"), 0o700)
			if err := os.Symlink(filepath.Join(home, "data"), filepath.Join(home, "link")); err != nil {
				t.Fatal(err)
			}
			dbDir := filepath.Join(home, tc.dbDir)
			if dbDir == home {
				if err := os.Chmod(home, tc.mode); err != nil {
					t.Fatal(err)
				}
			} else {
				mkdirMode(t, dbDir, tc.mode)
			}
			cfg, err := loadConfig(writeConfig(t, `{"data_dir": "`+home+`/`+tc.dataDir+`", "db_path": "`+dbDir+`/bridge.db"}`))
			if err != nil {
				t.Fatal(err)
			}
			logs := captureLog(t)

			protectDataDir(cfg)

			if got := permOf(t, dbDir); got != tc.mode {
				t.Errorf("db folder mode changed from %04o to %04o", tc.mode, got)
			}
			if got := logContains(logs, "warning: "+dbDir+", the folder of db_path, is open to other users"); got != tc.wantWarn {
				t.Errorf("warned = %v, want %v", got, tc.wantWarn)
			}
			if got := logContains(logs, "consider chmod 700"); got != tc.advice {
				t.Errorf("suggested chmod 700 = %v, want %v", got, tc.advice)
			}
		})
	}
}

func TestProtectDataDirLeavesOtherUsersFolderAlone(t *testing.T) {
	// A data dir that belongs to another user is theirs to set. Only root
	// could change it, and should not.
	t.Setenv("HOME", t.TempDir())
	var data string
	if os.Geteuid() == 0 {
		data = filepath.Join(t.TempDir(), "data")
		mkdirMode(t, data, 0o755)
		if err := os.Chown(data, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	} else {
		// Any system folder that root owns will do; nothing is written.
		for _, dir := range []string{"/usr", "/bin", "/etc"} {
			st, err := os.Stat(dir)
			if err != nil {
				continue
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() && st.Mode().Perm()&0o077 != 0 {
				data = dir
				break
			}
		}
		if data == "" {
			t.Skip("no folder of another user that others can open")
		}
	}
	before := permOf(t, data)
	logs := captureLog(t)

	protectDataDir(&config.Config{DataDir: data, DBPath: filepath.Join(data, "bridge.db")})

	if got := permOf(t, data); got != before {
		t.Errorf("data dir of another user changed from %04o to %04o", before, got)
	}
	if !logContains(logs, "warning: data_dir "+data+" belongs to another user") {
		t.Error("no warning that the data dir belongs to another user")
	}
	if logContains(logs, "could not make") {
		t.Error("tried to chmod a data dir of another user")
	}
}

func TestNewBridgeMakesDataDirPrivate(t *testing.T) {
	// protectDataDir does not run on config loads, so starting the bridge
	// is what must call it.
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	mkdirMode(t, data, 0o755)
	cfgs, err := openConfigStore(writeConfig(t, `{"brain_mode": true, "data_dir": "`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
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
	// effective() runs on every load and on every dashboard save. Changing
	// modes there would act on a folder typed into Settings before any
	// restart, and its warnings would repeat on every write.
	home := t.TempDir()
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	shared := filepath.Join(home, "shared")
	mkdirMode(t, data, 0o755)
	mkdirMode(t, shared, 0o755)
	logs := captureLog(t)

	cs, err := openConfigStore(writeConfig(t, `{"data_dir": "`+data+`", "db_path": "`+shared+`/bridge.db"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"debug", "info"} {
		if _, err := cs.Update("", func(c *config.Config) error { c.LogLevel = level; return nil }); err != nil {
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
	// A fresh install starts with no brains and adds them in the dashboard.
	t.Setenv("HOME", t.TempDir())
	if _, err := loadConfig(writeConfig(t, `{"brain_mode":true,"brain_dirs":[]}`)); err != nil {
		t.Fatalf("an empty brain list must load: %v", err)
	}
}

func TestLoadConfigMaxChunkSize(t *testing.T) {
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
			t.Setenv("HOME", t.TempDir())
			cfg, err := loadConfig(writeConfig(t, tc.body))
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
	t.Setenv("HOME", t.TempDir())
	example, err := os.ReadFile(filepath.Join(repoDir, "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, string(example))
	if runtime.GOOS != "darwin" {
		// The example turns the sandbox on, which only macOS can honour.
		if _, err := loadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), "sandbox.enabled") {
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
	if _, err := loadConfig(cfgPath); err != nil {
		t.Fatalf("config.example.json does not load: %v", err)
	}
}

func TestLogDMAccessWarnsWhenAnyoneCanDM(t *testing.T) {
	yes, no := true, false
	open := &config.Config{AllowAnyDM: &yes}
	unset := &config.Config{}
	listed := &config.Config{AllowedNumbers: []string{"15551234567"}, AllowAnyDM: &yes}
	closed := &config.Config{AllowAnyDM: &no}
	tests := []struct {
		name       string
		prev, next *config.Config
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
			logDMAccess(tc.prev, tc.next)
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

func newTestManager(t *testing.T, cfg *config.Config) *BrainManager {
	t.Helper()
	requireTestDataDir(t, cfg)
	mgr, err := NewBrainManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestBrainRouting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := loadConfig(writeConfig(t, `{
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := loadConfig(writeConfig(t, `{
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
		m    Inbound
		want string // "" = no brain
	}{
		{"by chat", Inbound{Inbound: chat.Inbound{JID: dm}}, "personal"},
		{"named brain wins over the chat's owner", Inbound{Inbound: chat.Inbound{JID: dm, Brain: "team"}}, "team"},
		{"named brain in a group it does not own", Inbound{Inbound: chat.Inbound{JID: "other@g.us", Brain: "personal"}}, "personal"},
		{"named brain is gone", Inbound{Inbound: chat.Inbound{JID: dm, Brain: "removed"}}, ""},
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
	if _, _, _, err := mgr.Submit(Inbound{Inbound: chat.Inbound{JID: dm, Brain: "removed", Text: "x"}}); err == nil {
		t.Error("Submit to a removed brain must fail, not run in the DM brain")
	}
}

// A DM goes by its chat JID, its other JID and the sender's phone number,
// which WhatsApp may use instead of each other; a group goes by its JID
// only.
func TestChatJIDs(t *testing.T) {
	const (
		pn    = "15551234567@s.whatsapp.net"
		lid   = "15559876543@lid"
		group = "120363000000000000@g.us"
	)
	for _, c := range []struct {
		name string
		m    Inbound
		want string
	}{
		{"DM by number", Inbound{Inbound: chat.Inbound{JID: pn, Sender: "15551234567"}}, pn},
		{"DM by LID", Inbound{Inbound: chat.Inbound{JID: lid, Sender: "15551234567"}}, lid + "," + pn},
		{"DM without a sender", Inbound{Inbound: chat.Inbound{JID: lid}}, lid},
		{"DM by number, its LID known", Inbound{Inbound: chat.Inbound{JID: pn, ChatAlt: lid, Sender: "15551234567"}}, pn + "," + lid},
		{"group", Inbound{Inbound: chat.Inbound{JID: group, Sender: "15551234567"}}, group},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(chatJIDs(c.m), ","); got != c.want {
				t.Errorf("chatJIDs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBrainRoutingFallsBackToFirstBrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := loadConfig(writeConfig(t, `{
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

// ── sandbox ───────────────────────────────────────────────────────────────

func TestSandboxProfileIsolatesBrains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &config.Config{DataDir: filepath.Join(home, "data")}
	sp := NewSandboxProfiler(map[string][]string{
		"personal": {"/opt/brains/personal"},
		"team":     {"/opt/brains/team"},
	}, cfg)

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
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &config.Config{DataDir: filepath.Join(home, "data"), Sandbox: config.SandboxConfig{AllowPaths: []string{filepath.Join(home, "Library")}}}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, cfg), "a")

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
	for _, creds := range []string{"", "creds"} {
		t.Run("credentials_dir "+creds, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			cfg := &config.Config{DataDir: filepath.Join(home, "data")}
			if creds != "" {
				cfg.Sandbox.CredentialsDir = filepath.Join(home, creds)
			}
			profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}, "b": {"/tmp/b"}}, cfg), "a")
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
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &config.Config{
		DataDir: filepath.Join(home, "data"),
		Sandbox: config.SandboxConfig{DenyPaths: []string{"/private/clients"}},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, cfg), "a")

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
	cases := []struct {
		file string
		want []string
	}{
		{"web/assets/components/brain.js", []string{outcomeOK + ": [", outcomeError + ": [", outcomeInterrupted + ": [", outcomeQuestion + ": ["}},
		{"web/assets/pages/settings.js", []string{"def: '" + config.QueueModeQueue + "'", "['" + config.QueueModeQueue + "', ", "['" + config.QueueModeInterrupt + "', "}},
		{"web/assets/lib/brain.js", []string{"export const BRIDGE_BACKUP_ID = '" + bridgeBackupID + "';", "[['" + manifest.ObserveHistory + "', ", "['" + manifest.ObserveRun + "', "}},
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
	assets := filepath.Join(repoDir, "web", "assets")
	err := filepath.WalkDir(assets, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".js" || path == filepath.Join(assets, "lib", "brain.js") {
			return err
		}
		js, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(js), "'"+bridgeBackupID+"'") {
			rel, _ := filepath.Rel(repoDir, path)
			t.Errorf("%s spells out %q; import BRIDGE_BACKUP_ID from lib/brain.js", rel, bridgeBackupID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
