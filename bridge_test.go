package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"
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

	real := []string{"what time is it", "ok but why", "book it for 3pm", "PR 42?", "привет", "你好", "שלום!", "ßß"}
	for _, s := range real {
		if isJunkMessage(s) {
			t.Errorf("isJunkMessage(%q) = true, want false", s)
		}
	}
}

// FuzzIsJunkMessage: untagged group messages the filter calls junk are
// dropped unread, so it may only catch what carries nothing: a single
// character, no letter or digit in any script, or one of the short
// acknowledgements, which are ASCII once lowercased the way the filter
// compares them. Whitespace around a message and its ASCII letter case
// never change the verdict.
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

// The end of a block is found in any letter case, like its start: a
// "[Poll]…[/poll]" block is a poll, not markup in the chat.
func TestExtractPollsAnyCase(t *testing.T) {
	polls, remaining := extractPolls("Vote:\n[Poll max=2]\nQuestion: Which?\n- A\n- B\n[/poll] thanks")
	if len(polls) != 1 || polls[0].Question != "Which?" || polls[0].MaxSelectable != 2 {
		t.Errorf("got %+v", polls)
	}
	if remaining != "Vote:\n thanks" {
		t.Errorf("remaining = %q", remaining)
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

// anyPollCloseRe finds the end of a poll block in any letter case, the way
// pollHeaderRe finds its start.
var anyPollCloseRe = regexp.MustCompile(`(?i)\[/POLL\]`)

// FuzzExtractPolls feeds extractPolls model output. Whatever the model
// writes, each poll that comes out has a question, 2 to 12 trimmed
// options and a max within them, and no complete poll block reaches the
// chat (see checkPolls). A well-formed block built from the fuzzed
// question and options, with nothing around it that looks like one, comes
// back as exactly that poll, and the text around it as it was.
func FuzzExtractPolls(f *testing.F) {
	seeds := []struct {
		before, question, options, after string
		maxSel                           uint8
	}{
		{"Sure, let's vote.\n\n", "When should we meet?", "Monday 2pm\nTuesday 3pm", "\n\nLet me know.", 0},
		{"", "Which?", "A\nB\nC", "", 2},
		{"", "Which?", "A\nB", "", 9},
		{"", "Pick", strings.Repeat("option\n", 20), "", 0},
		{"", "Only one?", "A", "", 0},
		{"", "", "A\nB", "", 0},
		{"[POLL]\nQuestion: unterminated\n- A\n- B", "Q", "a\nb", "", 0},
		{"[POLL max=3]\nQuestion: Q\n- a\n- b\n[/POLL] and ", "Q", "a\nb", " [POLL]", 1},
		{"[PO[POLL]", "Q", "a\nb", "LL]", 0},
		{"İİ ", "Fråga?", "é\n\U0001F600", " \xff", 3},
	}
	for _, s := range seeds {
		f.Add(s.before, s.question, s.options, s.after, s.maxSel)
	}
	f.Fuzz(func(t *testing.T, before, question, options, after string, maxSel uint8) {
		checkPolls(t, before)

		q := strings.TrimSpace(strings.ReplaceAll(question, "\n", " "))
		var opts []string
		for _, o := range strings.Split(options, "\n") {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		var block strings.Builder
		if maxSel > 0 {
			fmt.Fprintf(&block, "[POLL max=%d]\n", maxSel)
		} else {
			block.WriteString("[POLL]\n")
		}
		block.WriteString("Question: " + q + "\n")
		for _, o := range opts {
			block.WriteString("- " + o + "\n")
		}
		block.WriteString("[/POLL]")
		text := before + block.String() + after
		polls, remaining := checkPolls(t, text)

		// Another start or end in the text, the fuzzed parts' own included,
		// makes a different block.
		if len(pollHeaderRe.FindAllString(text, -1)) != 1 || len(anyPollCloseRe.FindAllString(text, -1)) != 1 {
			return
		}
		if want := strings.TrimSpace(before + after); remaining != want {
			t.Errorf("remaining text = %q, want %q", remaining, want)
		}
		if q == "" || len(opts) < 2 {
			if len(polls) != 0 {
				t.Errorf("got %+v from a block without a question or two options", polls)
			}
			return
		}
		want := pollData{Question: q, Options: opts[:min(len(opts), maxPollOptions)], MaxSelectable: 1}
		if maxSel > 0 {
			want.MaxSelectable = int(maxSel)
		}
		want.MaxSelectable = min(want.MaxSelectable, len(want.Options))
		if len(polls) != 1 || !reflect.DeepEqual(polls[0], want) {
			t.Errorf("got %+v, want [%+v]", polls, want)
		}
	})
}

// checkPolls runs extractPolls on text and checks what holds for any text:
// each poll has a question and 2 to 12 options, each a trimmed line of its
// own; there are no more polls than closed blocks; and the remaining text,
// which goes to the chat, is no longer than the input, still valid UTF-8
// if the input was, and holds no complete block.
func checkPolls(t *testing.T, text string) ([]pollData, string) {
	t.Helper()
	polls, remaining := extractPolls(text)
	line := func(s string) bool {
		return s != "" && s == strings.TrimSpace(s) && !strings.Contains(s, "\n")
	}
	for _, p := range polls {
		if !line(p.Question) {
			t.Errorf("question %q is not a trimmed line", p.Question)
		}
		if n := len(p.Options); n < 2 || n > maxPollOptions {
			t.Errorf("poll %q has %d options, want 2 to %d", p.Question, n, maxPollOptions)
		}
		for _, o := range p.Options {
			if !line(o) {
				t.Errorf("option %q is not a trimmed line", o)
			}
		}
		if p.MaxSelectable < 0 || p.MaxSelectable > len(p.Options) {
			t.Errorf("poll %q: max %d for %d options", p.Question, p.MaxSelectable, len(p.Options))
		}
		if utf8.ValidString(text) && !utf8.ValidString(p.Question+strings.Join(p.Options, "")) {
			t.Errorf("poll %+v is not valid UTF-8", p)
		}
	}
	if closed := len(anyPollCloseRe.FindAllString(text, -1)); len(polls) > closed {
		t.Errorf("%d polls from %d closed blocks", len(polls), closed)
	} else if closed == 0 && remaining != strings.TrimSpace(text) {
		t.Errorf("text without a closed block came back as %q", remaining)
	}
	if len(remaining) > len(text) {
		t.Errorf("remaining text %q is longer than the input", remaining)
	}
	if utf8.ValidString(text) && !utf8.ValidString(remaining) {
		t.Errorf("remaining text %q is not valid UTF-8", remaining)
	}
	if loc := pollHeaderRe.FindStringIndex(remaining); loc != nil && anyPollCloseRe.MatchString(remaining[loc[1]:]) {
		t.Errorf("remaining text %q still holds a poll block", remaining)
	}
	return polls, remaining
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

func TestLoadConfigAllowsNoBrains(t *testing.T) {
	// A fresh install starts with no brains and adds them in the dashboard.
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadConfig(writeConfig(t, `{"brain_mode":true,"brain_dirs":[]}`)); err != nil {
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
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/p", BrainManifest{ID: "personal", Routing: BrainRouting{HandlesDMs: true}}),
		brainFolder(t, home+"/t", BrainManifest{ID: "team", Routing: BrainRouting{Groups: []string{"team@g.us"}}}),
		brainFolder(t, home+"/o", BrainManifest{ID: "other", Routing: BrainRouting{Default: true}}),
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
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/p", BrainManifest{ID: "personal", Routing: BrainRouting{HandlesDMs: true}}),
		brainFolder(t, home+"/t", BrainManifest{ID: "team", Routing: BrainRouting{Groups: []string{"team@g.us"}}}),
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
		"data_dir": "`+home+`/data"`+brainDirsJSON(t, brainFolder(t, home+"/o", BrainManifest{ID: "only"}))+`
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

	cfg := &Config{DataDir: filepath.Join(home, "data")}
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

func TestSandboxProfileDeniesSecretsAndAllowsKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sp := NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, &Config{DataDir: filepath.Join(home, "data")})
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

	sp := NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}, "b": {"/tmp/b"}}, &Config{DataDir: dataDir})
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
	profile := mustProfile(t, NewSandboxProfiler(map[string][]string{"a": {"/tmp/a"}}, cfg), "a")

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
| +15559876543    | Sam    | --- see the 2024 notes |
| :-------------- | :----: | ----: |
`))
	if ids["15551234567"] != "Alex" {
		t.Errorf("got %q for Alex", ids["15551234567"])
	}
	// A leading + must be normalised away so it matches the JID form, and
	// dashes in the notes do not make a row the line under the header.
	if ids["15559876543"] != "Sam" {
		t.Errorf("got %q for Sam", ids["15559876543"])
	}
	if _, ok := ids["WhatsApp Number"]; ok {
		t.Error("header row was parsed as an entry")
	}
	// The line under the header, in any alignment, is no entry.
	if len(ids) != 2 {
		t.Errorf("got %d entries, want 2: %v", len(ids), ids)
	}
	if name, ok := parseIdentityMap([]byte("| + | Nobody |\n"))[""]; ok {
		t.Errorf("a row without a number was read as %q", name)
	}
	if len(parseIdentityMap(nil)) != 0 {
		t.Error("a missing map should yield an empty map, not an error")
	}
}

// FuzzParseIdentityMap: the brain writes its contact map itself, so it can
// hold anything. Every entry that comes out is a normalised number and a
// name, each a trimmed cell; and a row with a number and a name in its first
// two cells is read back, whatever its notes say.
func FuzzParseIdentityMap(f *testing.F) {
	table := "# People\n\n| WhatsApp Number | Person | Notes |\n| --------------- | ------ | ----- |\n| 15551234567     | Alex   | work  |\n"
	f.Add([]byte(table), "+15559876543", "Sam", "")
	f.Add([]byte(table), "15551234567", "Alex Again", "replaces the first row")
	f.Add([]byte(""), "15551234567@s.whatsapp.net", "Sam", "met in 2024")
	f.Add([]byte("|a|b|\n|:--|--:|\r\n| x |"), "WhatsApp Number", "Person", "Notes")
	f.Add([]byte("||\n|\n| | |\n"), " 15551234567 ", " Sam ", " | ")
	f.Fuzz(func(t *testing.T, table []byte, number, name, notes string) {
		check := func(ids map[string]string) {
			for k, v := range ids {
				if k == "" || strings.ContainsAny(k, "@|\n") || normalizeJID(k) != k || k != strings.TrimSpace(k) {
					t.Errorf("key %q is not a normalised number", k)
				}
				if v == "" || strings.ContainsAny(v, "|\n") || v != strings.TrimSpace(v) {
					t.Errorf("name %q for %q is not a trimmed cell", v, k)
				}
			}
		}
		check(parseIdentityMap(table))

		// A row the brain appends, its cells made into cells.
		cell := func(s string) string {
			return strings.TrimSpace(strings.NewReplacer("|", " ", "\n", " ").Replace(s))
		}
		number, name, notes = cell(number), cell(name), cell(notes)
		ids := parseIdentityMap([]byte(string(table) + "\n| " + number + " | " + name + " | " + notes + " |\n"))
		check(ids)
		// Nothing to read back, or the header and the line under it.
		if number == "" || normalizeJID(number) == "" || name == "" || number == "WhatsApp Number" || strings.Trim(number+name+notes, "-: \t") == "" {
			return
		}
		if got := ids[normalizeJID(number)]; got != name {
			t.Errorf("row | %s | %s | %s | read back as %q, want %q", number, name, notes, got, name)
		}
	})
}

func TestNormalizeJID(t *testing.T) {
	cases := map[string]string{
		"15551234567@s.whatsapp.net": "15551234567",
		"+15551234567":               "15551234567",
		"120363000000000000@g.us":    "120363000000000000",
		"15551234567":                "15551234567",
		"++15551234567":              "15551234567",
		" + 15551234567 ":            "15551234567",
	}
	for in, want := range cases {
		if got := normalizeJID(in); got != want {
			t.Errorf("normalizeJID(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzNormalizeJID: a normalised JID is what allowed_numbers and the
// identity map hold, and what a sender is compared with. It has no server
// and no space around it, it is part of the input, normalising it again
// changes nothing, and every way of writing a number gives the number.
func FuzzNormalizeJID(f *testing.F) {
	for _, s := range []string{"15551234567@s.whatsapp.net", "+15551234567", "120363000000000000@g.us", "15551234567", "15551234567:12@s.whatsapp.net", "", "@", "+", "a@b@c", "+@s.whatsapp.net"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, jid string) {
		n := normalizeJID(jid)
		if strings.Contains(n, "@") {
			t.Errorf("normalizeJID(%q) = %q, which still has a server", jid, n)
		}
		if n != strings.TrimSpace(n) {
			t.Errorf("normalizeJID(%q) = %q, not trimmed", jid, n)
		}
		if !strings.Contains(jid, n) {
			t.Errorf("normalizeJID(%q) = %q, which is not part of it", jid, n)
		}
		if again := normalizeJID(n); again != n {
			t.Errorf("normalizeJID(%q) = %q, but normalising that gives %q", jid, n, again)
		}
		digits := strings.Map(func(r rune) rune {
			if '0' <= r && r <= '9' {
				return r
			}
			return -1
		}, jid)
		if digits == "" {
			return
		}
		for _, form := range []string{digits, "+" + digits, digits + "@s.whatsapp.net", "+" + digits + "@s.whatsapp.net", digits + "@g.us"} {
			if got := normalizeJID(form); got != digits {
				t.Errorf("normalizeJID(%q) = %q, want %q", form, got, digits)
			}
		}
	})
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
