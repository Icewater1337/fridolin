package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// ── triggers ──────────────────────────────────────────────────────────────

func TestMatchesTrigger(t *testing.T) {
	triggers := []string{"@claude", "hey bot"}

	cases := []struct {
		text string
		want bool
	}{
		{"@claude what's up", true},
		{"@CLAUDE what's up", true},    // case-insensitive
		{"ask @claude about it", true}, // anywhere in the message
		{"hey bot, status?", true},
		{"HEY BOT", true},
		{"just chatting", false},
		{"", false},
		{"claude is a name", false}, // must match the full trigger
	}

	for _, c := range cases {
		if got := TriggersFromStrings(triggers).Matches(c.text); got != c.want {
			t.Errorf("matchesTrigger(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestMatchesTriggerEmptyTriggerIgnored(t *testing.T) {
	// An empty trigger would otherwise match every message.
	if TriggersFromStrings([]string{""}).Matches("anything at all") {
		t.Error("an empty trigger must not match")
	}
}

func TestStripTriggers(t *testing.T) {
	triggers := []string{"@claude", "hey bot"}

	cases := []struct{ in, want string }{
		{"@claude hello", "hello"},
		{"@CLAUDE hello", "hello"},
		{"hello @claude", "hello"},
		{"@claude hello @claude again", "hello  again"},
		{"hey bot do the thing", "do the thing"},
		{"nothing to strip", "nothing to strip"},
		{"@claude", ""},
	}

	for _, c := range cases {
		if got := TriggersFromStrings(triggers).Strip(c.in); got != c.want {
			t.Errorf("stripTriggers(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripTriggersTerminates(t *testing.T) {
	// Repeated occurrences must all be removed without the strip loop spinning.
	done := make(chan string, 1)
	go func() { done <- TriggersFromStrings([]string{"a"}).Strip("aaaa") }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stripTriggers did not terminate")
	}
}

// ── junk filter ───────────────────────────────────────────────────────────

func TestIsJunkMessage(t *testing.T) {
	junk := []string{"ok", "OK", "thanks", "👍", "...", "?", "a", "🎉🎉"}
	for _, s := range junk {
		if !isJunkMessage(s) {
			t.Errorf("isJunkMessage(%q) = false, want true", s)
		}
	}

	real := []string{"what time is it", "ok but why", "book it for 3pm", "PR 42?"}
	for _, s := range real {
		if isJunkMessage(s) {
			t.Errorf("isJunkMessage(%q) = true, want false", s)
		}
	}
}

// ── polls ─────────────────────────────────────────────────────────────────

func TestExtractPolls(t *testing.T) {
	text := `Sure, let's vote.

[POLL]
Question: When should we meet?
- Monday 2pm
- Tuesday 3pm
[/POLL]

Let me know.`

	polls, remaining := extractPolls(text)
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	p := polls[0]
	if p.Question != "When should we meet?" {
		t.Errorf("question = %q", p.Question)
	}
	if len(p.Options) != 2 || p.Options[0] != "Monday 2pm" {
		t.Errorf("options = %v", p.Options)
	}
	if p.MaxSelectable != 1 {
		t.Errorf("MaxSelectable = %d, want 1", p.MaxSelectable)
	}
	if strings.Contains(remaining, "POLL") {
		t.Errorf("poll block left in remaining text: %q", remaining)
	}
	if !strings.Contains(remaining, "Let me know.") {
		t.Errorf("surrounding text lost: %q", remaining)
	}
}

func TestExtractPollsMaxSelectable(t *testing.T) {
	polls, _ := extractPolls("[POLL max=2]\nQuestion: Which?\n- A\n- B\n- C\n[/POLL]")
	if len(polls) != 1 || polls[0].MaxSelectable != 2 {
		t.Fatalf("got %+v", polls)
	}
}

func TestExtractPollsClampsMaxToOptionCount(t *testing.T) {
	// WhatsApp rejects a poll whose max exceeds the number of options.
	polls, _ := extractPolls("[POLL max=9]\nQuestion: Which?\n- A\n- B\n[/POLL]")
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	if polls[0].MaxSelectable != 2 {
		t.Errorf("MaxSelectable = %d, want 2", polls[0].MaxSelectable)
	}
}

func TestExtractPollsTruncatesToWhatsAppLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[POLL]\nQuestion: Pick\n")
	for i := 0; i < 20; i++ {
		sb.WriteString("- option\n")
	}
	sb.WriteString("[/POLL]")

	polls, _ := extractPolls(sb.String())
	if len(polls) != 1 {
		t.Fatalf("got %d polls, want 1", len(polls))
	}
	if len(polls[0].Options) != maxPollOptions {
		t.Errorf("options = %d, want %d", len(polls[0].Options), maxPollOptions)
	}
}

func TestExtractPollsRejectsIncomplete(t *testing.T) {
	// One option is not a poll; neither is a block with no question.
	for _, text := range []string{
		"[POLL]\nQuestion: Only one?\n- A\n[/POLL]",
		"[POLL]\n- A\n- B\n[/POLL]",
		"[POLL]\nQuestion: unterminated\n- A\n- B",
	} {
		if polls, _ := extractPolls(text); len(polls) != 0 {
			t.Errorf("extractPolls(%q) returned %d polls, want 0", text, len(polls))
		}
	}
}

func TestExtractPollsNoPoll(t *testing.T) {
	polls, remaining := extractPolls("just a normal reply")
	if len(polls) != 0 {
		t.Errorf("got %d polls, want 0", len(polls))
	}
	if remaining != "just a normal reply" {
		t.Errorf("remaining = %q", remaining)
	}
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
	cfg, err := LoadConfig(writeConfig(t, `{}`))
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
			cfg, err := LoadConfig(writeConfig(t, c.body))
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
	if got := (&Config{}).MaxMediaBytes(); got != 100<<20 {
		t.Errorf("an unfilled config allows %d bytes, want the default", got)
	}
}

func TestLoadConfigExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := LoadConfig(writeConfig(t, `{
		"data_dir": "~/data",
		"brain_mode": true,
		"brains": [{"name": "a", "work_dir": "~/brains/a"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "data"); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
	if want := filepath.Join(home, "brains", "a"); cfg.Brains[0].WorkDir != want {
		t.Errorf("brain WorkDir = %q, want %q", cfg.Brains[0].WorkDir, want)
	}
	if want := filepath.Join(home, "brains", "a", "brain-prompt.md"); cfg.Brains[0].PromptFile != want {
		t.Errorf("brain PromptFile = %q, want %q", cfg.Brains[0].PromptFile, want)
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
			home := canonicalDir(t.TempDir())
			t.Setenv("HOME", home)
			data := filepath.Join(home, "data")
			mkdirMode(t, data, tc.mode)
			dataDir := data
			if tc.relative {
				t.Chdir(home)
				dataDir = "data"
			}
			logs := captureLog(t)

			cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+dataDir+`"}`))
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
			root := canonicalDir(t.TempDir())
			home := filepath.Join(root, "home")
			mkdirMode(t, home, tc.mode)
			if err := os.Chmod(root, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Chdir(home)
			cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+tc.dataDir(root, home)+`"}`))
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
			cfg, err := LoadConfig(writeConfig(t, `{"data_dir": "`+home+`/`+tc.dataDir+`", "db_path": "`+dbDir+`/bridge.db"}`))
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

	protectDataDir(&Config{DataDir: data, DBPath: filepath.Join(data, "bridge.db")})

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
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	mkdirMode(t, data, 0o755)
	cfgs, err := OpenConfigStore(writeConfig(t, `{"brain_mode": true, "data_dir": "`+data+`"}`))
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

	cs, err := OpenConfigStore(writeConfig(t, `{"data_dir": "`+data+`", "db_path": "`+shared+`/bridge.db"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"debug", "info"} {
		if _, err := cs.Update("", func(c *Config) error { c.LogLevel = level; return nil }); err != nil {
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

func TestLoadConfigRejectsBadBrains(t *testing.T) {
	cases := map[string]string{
		"duplicate name":  `{"brain_mode":true,"brains":[{"name":"a","work_dir":"/tmp/a"},{"name":"a","work_dir":"/tmp/b"}]}`,
		"two dm brains":   `{"brain_mode":true,"brains":[{"name":"a","work_dir":"/tmp/a","handles_dms":true},{"name":"b","work_dir":"/tmp/b","handles_dms":true}]}`,
		"shared group":    `{"brain_mode":true,"brains":[{"name":"a","work_dir":"/tmp/a","groups":["g@g.us"]},{"name":"b","work_dir":"/tmp/b","groups":["g@g.us"]}]}`,
		"missing workdir": `{"brain_mode":true,"brains":[{"name":"a"}]}`,
		"name with slash": `{"brain_mode":true,"brains":[{"name":"a/b","work_dir":"/tmp/a"}]}`,
	}
	for name, body := range cases {
		t.Setenv("HOME", t.TempDir())
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestLoadConfigAllowsNoBrains(t *testing.T) {
	// A fresh install starts with no brains and adds them in the dashboard.
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadConfig(writeConfig(t, `{"brain_mode":true,"brains":[]}`)); err != nil {
		t.Fatalf("an empty brain list must load: %v", err)
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALLOWED_NUMBERS", "111, 222 ,333")
	t.Setenv("TRIGGERS", "@bot")
	cfg, err := LoadConfig(writeConfig(t, `{"allowed_numbers":["999"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedNumbers) != 3 || cfg.AllowedNumbers[2] != "333" {
		t.Errorf("AllowedNumbers = %v", cfg.AllowedNumbers)
	}
	if len(cfg.Triggers) != 1 || cfg.Triggers[0].Text != "@bot" {
		t.Errorf("Triggers = %v", cfg.Triggers)
	}
}

func TestLoadConfigMaxChunkSize(t *testing.T) {
	// Below 100 the "[i/N] " numbering leaves too little room for text;
	// such a limit is refused at load time rather than quietly changed.
	cases := []struct {
		name, body, env string
		want            int // 0: the config must be refused
	}{
		{"unset means the default", `{}`, "", 3900},
		{"zero means the default", `{"max_chunk_size":0}`, "", 3900},
		{"the minimum is allowed", `{"max_chunk_size":100}`, "", 100},
		{"a larger limit is kept", `{"max_chunk_size":5000}`, "", 5000},
		{"just below the minimum", `{"max_chunk_size":99}`, "", 0},
		{"smaller than the numbering", `{"max_chunk_size":5}`, "", 0},
		{"negative", `{"max_chunk_size":-1}`, "", 0},
		{"too small from the environment", `{}`, "50", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if tc.env != "" {
				t.Setenv("MAX_CHUNK_SIZE", tc.env)
			}
			cfg, err := LoadConfig(writeConfig(t, tc.body))
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
	// Loaded from a copy: the .env next to the config is read too, and next
	// to the example lies whatever .env a developer keeps in the checkout.
	t.Setenv("HOME", t.TempDir())
	example, err := os.ReadFile(filepath.Join(repoDir, "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, string(example))
	if runtime.GOOS != "darwin" {
		// The example turns the sandbox on, which only macOS can honour.
		if _, err := LoadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), "sandbox.enabled") {
			t.Fatalf("config.example.json on %s: err = %v, want the sandbox.enabled error", runtime.GOOS, err)
		}
		t.Setenv("SANDBOX_ENABLED", "false")
	}
	if _, err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("config.example.json does not load: %v", err)
	}
}

func TestLogDMAccessWarnsWhenAnyoneCanDM(t *testing.T) {
	yes, no := true, false
	open := &Config{AllowAnyDM: &yes}
	unset := &Config{}
	listed := &Config{AllowedNumbers: []string{"15551234567"}, AllowAnyDM: &yes}
	closed := &Config{AllowAnyDM: &no}
	tests := []struct {
		name       string
		prev, next *Config
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

func newTestManager(t *testing.T, cfg *Config) *BrainManager {
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

	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"brains": [
			{"name": "personal", "work_dir": "`+home+`/p", "handles_dms": true},
			{"name": "team",     "work_dir": "`+home+`/t", "groups": ["team@g.us"]},
			{"name": "other",    "work_dir": "`+home+`/o", "default": true}
		]
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
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"brains": [
			{"name": "personal", "work_dir": "`+home+`/p", "handles_dms": true},
			{"name": "team",     "work_dir": "`+home+`/t", "groups": ["team@g.us"]}
		]
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
		{"by chat", Inbound{JID: dm}, "personal"},
		{"named brain wins over the chat's owner", Inbound{JID: dm, Brain: "team"}, "team"},
		{"named brain in a group it does not own", Inbound{JID: "other@g.us", Brain: "personal"}, "personal"},
		{"named brain is gone", Inbound{JID: dm, Brain: "removed"}, ""},
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
	if _, _, _, err := mgr.Submit(Inbound{JID: dm, Brain: "removed", Text: "x"}); err == nil {
		t.Error("Submit to a removed brain must fail, not run in the DM brain")
	}
}

// A DM goes by its chat JID and the sender's phone number, which WhatsApp
// may use instead of each other; a group goes by its JID only.
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
		{"DM by number", Inbound{JID: pn, Sender: "15551234567"}, pn},
		{"DM by LID", Inbound{JID: lid, Sender: "15551234567"}, lid + "," + pn},
		{"DM without a sender", Inbound{JID: lid}, lid},
		{"group", Inbound{JID: group, Sender: "15551234567"}, group},
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

	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"brains": [{"name": "only", "work_dir": "`+home+`/o"}]
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

func TestNewBrainManagerCreatesWorkDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"brains": [{"name": "a", "work_dir": "`+home+`/brains/a"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	newTestManager(t, cfg)

	if fi, err := os.Stat(filepath.Join(home, "brains", "a")); err != nil || !fi.IsDir() {
		t.Errorf("work dir was not created: %v", err)
	}
}

// ── sandbox ───────────────────────────────────────────────────────────────

func TestSandboxProfileIsolatesBrains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{DataDir: filepath.Join(home, "data")}
	sp := NewSandboxProfiler(map[string]string{
		"personal": "/opt/brains/personal",
		"team":     "/opt/brains/team",
	}, cfg)

	profile := mustProfile(t, sp, "personal")

	if !strings.Contains(profile, `(deny file-read* (subpath "/opt/brains/team"))`) {
		t.Error("personal's profile does not deny the team work dir")
	}
	if strings.Contains(profile, `(deny file-read* (subpath "/opt/brains/personal"))`) {
		t.Error("personal's profile denies its own work dir")
	}
}

func TestSandboxProfileDeniesSecretsAndAllowsKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sp := NewSandboxProfiler(map[string]string{"a": "/tmp/a"}, &Config{DataDir: filepath.Join(home, "data")})
	profile := mustProfile(t, sp, "a")

	for _, secret := range []string{".ssh", ".aws", ".gnupg", "Library/Mail", ".git-credentials"} {
		if !strings.Contains(profile, filepath.Join(home, secret)) {
			t.Errorf("profile does not deny %s", secret)
		}
	}
	// Denying the keychain breaks the Claude CLI's own authentication.
	if strings.Contains(profile, "Library/Keychains") {
		t.Error("profile denies Library/Keychains, which breaks the Claude CLI")
	}
}

func TestSandboxProfileScopesCalendarCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, "data")

	sp := NewSandboxProfiler(map[string]string{"a": "/tmp/a", "b": "/tmp/b"}, &Config{DataDir: dataDir})
	profile := mustProfile(t, sp, "a")

	if !strings.Contains(profile, filepath.Join(dataDir, "google_calendar_credentials_a.json")) {
		t.Error("brain a cannot read its own calendar credentials")
	}
	if strings.Contains(profile, "google_calendar_credentials_b.json") {
		t.Error("brain a can read brain b's calendar credentials")
	}
}

func TestSandboxProfileIncludesConfiguredDenies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{
		DataDir: filepath.Join(home, "data"),
		Sandbox: SandboxConfig{DenyPaths: []string{"/private/clients"}},
	}
	profile := mustProfile(t, NewSandboxProfiler(map[string]string{"a": "/tmp/a"}, cfg), "a")

	if !strings.Contains(profile, `(deny file-read* (subpath "/private/clients"))`) {
		t.Error("configured deny path missing from the profile")
	}
}

// ── OTP guard ─────────────────────────────────────────────────────────────

func TestBareCodeRe(t *testing.T) {
	for _, s := range []string{"123456", " 483920 ", "1234", "12345678"} {
		if !bareCodeRe.MatchString(s) {
			t.Errorf("bareCodeRe should match %q", s)
		}
	}
	for _, s := range []string{"123", "123456789", "code 123456", "12 34 56", ""} {
		if bareCodeRe.MatchString(s) {
			t.Errorf("bareCodeRe should not match %q", s)
		}
	}
}

func TestRecentOutboundLooksLikeOTPRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	tail := func() []string {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		return tailLines(f, 256*1024)
	}

	write := func(entries ...historyEntry) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, e := range entries {
			b, _ := json.Marshal(e)
			f.Write(append(b, '\n'))
		}
	}

	write(historyEntry{Type: "out", From: "personal", Text: "🔐 Please send the code"})
	if !recentOutboundLooksLikeOTPRequest(tail(), "personal") {
		t.Error("should detect the OTP request")
	}

	// An inbound message quoting the marker must not arm the guard.
	write(historyEntry{Type: "in", From: "15551234567", Text: "what is an OTP?"})
	if recentOutboundLooksLikeOTPRequest(tail(), "personal") {
		t.Error("inbound message must not arm the guard")
	}

	// Another brain's outbound message must not arm this brain's guard.
	write(historyEntry{Type: "out", From: "team", Text: "🔐 send the code"})
	if recentOutboundLooksLikeOTPRequest(tail(), "personal") {
		t.Error("another brain's message must not arm the guard")
	}

	write(historyEntry{Type: "out", From: "personal", Text: "here is your summary"})
	if recentOutboundLooksLikeOTPRequest(tail(), "personal") {
		t.Error("ordinary message must not arm the guard")
	}

	if recentOutboundLooksLikeOTPRequest(nil, "personal") {
		t.Error("a missing history file must not arm the guard")
	}
}

// ── identity map ──────────────────────────────────────────────────────────

func TestParseIdentityMap(t *testing.T) {
	ids := parseIdentityMap([]byte(`# People

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
| 15551234567     | Alex   | work  |
| +15559876543    | Sam    |       |
`))
	if ids["15551234567"] != "Alex" {
		t.Errorf("got %q for Alex", ids["15551234567"])
	}
	// A leading + must be normalised away so it matches the JID form.
	if ids["15559876543"] != "Sam" {
		t.Errorf("got %q for Sam", ids["15559876543"])
	}
	if _, ok := ids["WhatsApp Number"]; ok {
		t.Error("header row was parsed as an entry")
	}
	if len(parseIdentityMap(nil)) != 0 {
		t.Error("a missing map should yield an empty map, not an error")
	}
}

func TestNormalizeJID(t *testing.T) {
	cases := map[string]string{
		"15551234567@s.whatsapp.net": "15551234567",
		"+15551234567":               "15551234567",
		"120363000000000000@g.us":    "120363000000000000",
		"15551234567":                "15551234567",
	}
	for in, want := range cases {
		if got := normalizeJID(in); got != want {
			t.Errorf("normalizeJID(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── misc ──────────────────────────────────────────────────────────────────

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// Cutting mid-rune would emit invalid UTF-8 into the activity log.
	got := truncate("héllo wörld", 2)
	if !strings.ContainsRune(got, '…') {
		t.Errorf("expected an ellipsis, got %q", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Errorf("truncate produced invalid UTF-8: %q", got)
		}
	}
	if truncate("short", 100) != "short" {
		t.Error("truncate altered a string under the limit")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:8765", "localhost:8765", "[::1]:8765"}
	for _, a := range loopback {
		if !isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q) = false, want true", a)
		}
	}
	exposed := []string{"0.0.0.0:8765", ":8765", "192.168.1.5:8765", "example.com:80", "8765"}
	for _, a := range exposed {
		if isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q) = true, want false", a)
		}
	}
}

func TestSplitTrimmed(t *testing.T) {
	got := splitTrimmed(" a , b ,, c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}
