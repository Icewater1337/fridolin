package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// liveLine is a line of the fake live CLI's log (FRIDOLIN_FAKE_LIVE_LOG).
type liveLine struct {
	Event   string `json:"event"`
	Kind    string `json:"kind"`
	Turn    string `json:"turn"`
	Content string `json:"content"`
	PID     int    `json:"pid"`
}

// liveHarness is a served bridge whose brain "main" keeps a live session
// for its owner's direct messages, with the fake live CLI's log in log.
func liveHarness(t *testing.T, mode string, extra func(h *harness)) (h *harness, log func() []liveLine) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "live.log")
	setenv(t, "FRIDOLIN_FAKE_LIVE_LOG", logPath)
	optionsOf(t).waits.liveReady = 5 * time.Second
	optionsOf(t).waits.liveSettle = 0
	h = newServedHarness(t, `{"dangerously_skip_permissions": true}`)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, LiveSession: mode})
	if extra != nil {
		extra(h)
	}
	return h, func() []liveLine {
		f, err := os.Open(logPath)
		if err != nil {
			return nil
		}
		defer f.Close()
		var out []liveLine
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var l liveLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				out = append(out, l)
			}
		}
		return out
	}
}

// waitLive waits until brain id's live session is ready for a turn.
func (h *harness) waitLive(id string) {
	h.t.Helper()
	h.WaitFor("the live session of "+id, func() bool {
		h.b.live.mu.Lock()
		defer h.b.live.mu.Unlock()
		s := h.b.live.sess[id]
		return s != nil && s.ready
	})
}

func messagesOf(lines []liveLine) (out []liveLine) {
	for _, l := range lines {
		if l.Event == "message" && l.Kind == "message" {
			out = append(out, l)
		}
	}
	return out
}

// TestLiveSessionTakesTheOwnersDMs: the first message of a brain with a
// live session runs as a one-off while the session starts and answers its
// check; the messages after it go to the session, which is told the whole
// prompt once and then only what is new, and each is admitted by the
// policy as a run is.
func TestLiveSessionTakesTheOwnersDMs(t *testing.T) {
	t.Parallel()
	h, log := liveHarness(t, manifest.LiveSessionOwnerDM, nil)

	if got := h.Answer(inbound{JID: harnessDM, Text: "one"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("with no session ready, the message runs as a one-off: %q", got)
	}
	h.waitLive("main")
	two := h.Answer(inbound{JID: harnessDM, Text: "two"})
	three := h.Answer(inbound{JID: harnessDM, Text: "three"})
	if !strings.HasPrefix(two, "live1:") || !strings.HasSuffix(two, ": says: two") && !strings.HasSuffix(two, "says: two") {
		t.Errorf("the session's answer to the second message: %q", two)
	}
	if !strings.HasPrefix(three, "live2:") || !strings.HasSuffix(three, "says: three") {
		t.Errorf("the session's answer to the third: %q", three)
	}

	lines := log()
	starts := 0
	for _, l := range lines {
		if l.Event == "start" {
			starts++
		}
	}
	msgs := messagesOf(lines)
	if starts != 1 || len(msgs) != 2 {
		t.Fatalf("one session, two messages in it; the log: %+v", lines)
	}
	if !strings.Contains(msgs[0].Content, "<live_session>") || !strings.Contains(msgs[0].Content, "<chat_history>") {
		t.Errorf("the session's first turn is told the history and how it differs from a one-off run:\n%s", msgs[0].Content)
	}
	if strings.Contains(msgs[1].Content, "<chat_history>") || strings.Contains(msgs[1].Content, "<live_session>") || !strings.Contains(msgs[1].Content, "<new_message>") {
		t.Errorf("a later turn is told the new message only:\n%s", msgs[1].Content)
	}
	// Each turn is a run of the policy's: three runs, one with the session.
	if n := len(h.auditOf("run_start")); n != 3 {
		t.Errorf("run_start lines: %d, want 3", n)
	}
	if n := len(h.auditOf("run_end")); n != 3 {
		t.Errorf("run_end lines: %d, want 3", n)
	}
}

// TestLiveSessionOnlyForTheOwnersDMs: a brain without the setting, a
// group, a message from anyone but an owner, or one the bridge hands in
// itself, never reaches a session.
func TestLiveSessionOnlyForTheOwnersDMs(t *testing.T) {
	t.Parallel()
	h, log := liveHarness(t, manifest.LiveSessionOff, nil)
	for i, text := range []string{"a", "b"} {
		if got := h.Answer(inbound{JID: harnessDM, Text: text}); !strings.HasPrefix(got, "echo:") {
			t.Errorf("message %d with live_session off: %q", i, got)
		}
	}
	if got := log(); len(got) != 0 {
		t.Errorf("a session started with live_session off: %+v", got)
	}

	const owner = "15551234567"
	dm := chat.Inbound{JID: harnessDM, Sender: owner, Source: chat.SourceWhatsApp}
	with := func(f func(*chat.Inbound), g func(*brain.Settings)) bool {
		m := dm
		if f != nil {
			f(&m)
		}
		s := &brain.Settings{Manifest: manifest.Manifest{LiveSession: manifest.LiveSessionOwnerDM}, Dangerously: true}
		if g != nil {
			g(s)
		}
		return h.b.live.eligible(brain.RunSpec{Settings: s, Message: m})
	}
	for _, c := range []struct {
		name string
		f    func(*chat.Inbound)
		g    func(*brain.Settings)
		want bool
	}{
		{"the owner's DM", nil, nil, true},
		{"the dev chat's", func(m *chat.Inbound) { m.Source = chat.SourceDev }, nil, true},
		{"a group", func(m *chat.Inbound) { m.JID = harnessGroup }, nil, false},
		{"another sender", func(m *chat.Inbound) { m.Sender = strangerNumber }, nil, false},
		{"an untagged message", func(m *chat.Inbound) { m.Silent = true }, nil, false},
		{"a scheduled agent's", func(m *chat.Inbound) { m.Source = chat.SourceScheduled }, nil, false},
		{"a delegated task", func(m *chat.Inbound) { m.Source = chat.SourceDelegate }, nil, false},
		{"an approval's answer", func(m *chat.Inbound) { m.Source = chat.SourceApprovalResult }, nil, false},
		{"a brain on Codex", nil, func(s *brain.Settings) { s.Provider = "codex" }, false},
		{"a brain that asks for permissions", nil, func(s *brain.Settings) { s.Dangerously = false }, false},
		{"a brain on the user's own Claude config", nil, func(s *brain.Settings) { s.InheritUser = true }, false},
		{"a brain with the setting off", nil, func(s *brain.Settings) { s.Manifest.LiveSession = "" }, false},
	} {
		if got := with(c.f, c.g); got != c.want {
			t.Errorf("%s: eligible = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLiveSessionFallsBackWhenChannelsAreOff: a CLI that never answers the
// session's check (channels off, a login refused) leaves the brain on
// one-off runs, and is not asked again at once.
func TestLiveSessionFallsBackWhenChannelsAreOff(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_LIVE_DEAF", "1")
	h, log := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	optionsOf(t).waits.liveReady = 300 * time.Millisecond
	h.b.waits.liveReady = 300 * time.Millisecond

	if got := h.Answer(inbound{JID: harnessDM, Text: "one"}); !strings.HasPrefix(got, "echo:") {
		t.Fatalf("first: %q", got)
	}
	h.WaitFor("the failed session to be given up", func() bool {
		h.b.live.mu.Lock()
		defer h.b.live.mu.Unlock()
		return h.b.live.fails["main"].n == 1 && h.b.live.sess["main"] == nil
	})
	for _, text := range []string{"two", "three"} {
		if got := h.Answer(inbound{JID: harnessDM, Text: text}); !strings.HasPrefix(got, "echo:") {
			t.Errorf("%s: %q", text, got)
		}
	}
	starts := 0
	for _, l := range log() {
		if l.Event == "start" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("the CLI was started %d times, want once: the failure holds for a while", starts)
	}
}

// TestLiveSessionDyingInATurn: a session that dies while it works on a
// message ends that message in an error, as a failed run would, without
// running it again; the messages after it run as one-offs until a new
// session is ready.
func TestLiveSessionDyingInATurn(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_LIVE_EXIT_ON", "boom")
	h, log := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	if got := h.Answer(inbound{JID: harnessDM, Text: "boom"}); strings.HasPrefix(got, "echo:") || strings.HasPrefix(got, "live") {
		t.Errorf("the message the session died on was answered as if it ran: %q", got)
	}
	if n := len(messagesOf(log())); n != 1 {
		t.Errorf("the message was handed to a session %d times, want once", n)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "after"}); !strings.HasPrefix(got, "echo:") {
		t.Errorf("the message after it: %q", got)
	}
	h.waitLive("main")
	if got := h.Answer(inbound{JID: harnessDM, Text: "again"}); !strings.HasPrefix(got, "live1:") {
		t.Errorf("a new session answers from its first turn: %q", got)
	}
}

// TestLiveSessionHasTheBridgesTools: the session's MCP server serves the
// tools of the turn's token, catch_up among them.
func TestLiveSessionHasTheBridgesTools(t *testing.T) {
	t.Parallel()
	steps, _ := json.Marshal([]testutil.FakeMCPStep{{Tool: ""}, {Tool: "catch_up", Args: json.RawMessage(`{}`)}})
	setenv(t, "FRIDOLIN_FAKE_MCP", string(steps))
	h, _ := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	got := h.Answer(inbound{JID: harnessDM, Text: "tools please #mcp"})
	for _, want := range []string{"live1:", "[main:list] ", "catch_up", "[main:catch_up] ", "message(s) came in since you last answered"} {
		if !strings.Contains(got, want) {
			t.Errorf("the session's answer lacks %q:\n%s", want, got)
		}
	}
}

// TestStoppingALiveTurnEndsTheSession: !stop (the brain's StopCurrent)
// cancels the run the turn is, whose half-done turn must not leave a Stop
// hook to land in the next one: the session goes, and the next message
// runs as a one-off while a new session starts.
func TestStoppingALiveTurnEndsTheSession(t *testing.T) {
	t.Parallel()
	setenv(t, "FRIDOLIN_FAKE_LIVE_HANG_ON", "hang")
	h, log := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	h.Inject(inbound{JID: harnessDM, Text: "hang"})
	h.WaitFor("the message to reach the session", func() bool { return len(messagesOf(log())) == 1 })
	if !h.b.brainMgr.Get("main").StopCurrent(false) {
		t.Fatal("nothing to stop")
	}
	h.WaitFor("the session to end", func() bool {
		h.b.live.mu.Lock()
		defer h.b.live.mu.Unlock()
		return h.b.live.sess["main"] == nil
	})
	if got := h.Answer(inbound{JID: harnessDM, Text: "after"}); !strings.HasPrefix(got, "echo:") {
		t.Errorf("the message after the stop: %q", got)
	}
	h.waitLive("main")
	if got := h.Answer(inbound{JID: harnessDM, Text: "later"}); !strings.HasPrefix(got, "live1:") {
		t.Errorf("a new session: %q", got)
	}
}

// TestLiveSessionStartsAnewAfterItsTurns: past liveTurns the next message
// runs as a one-off while a fresh session, told the history again, starts.
func TestLiveSessionStartsAnewAfterItsTurns(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.liveTurns = 2
	h, log := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	for _, text := range []string{"a", "b"} {
		if got := h.Answer(inbound{JID: harnessDM, Text: text}); !strings.HasPrefix(got, "live") {
			t.Fatalf("%s: %q", text, got)
		}
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "c"}); !strings.HasPrefix(got, "echo:") {
		t.Errorf("past its turns, the old session takes no more: %q", got)
	}
	h.waitLive("main")
	if got := h.Answer(inbound{JID: harnessDM, Text: "d"}); !strings.HasPrefix(got, "live1:") {
		t.Errorf("the new session: %q", got)
	}
	starts := 0
	for _, l := range log() {
		if l.Event == "start" {
			starts++
		}
	}
	if starts != 2 {
		t.Errorf("sessions started: %d, want 2", starts)
	}
}

// TestSeedLiveState: the session's Claude Code state says onboarding is
// done and the brain's folder trusted, and keeps what else it held.
func TestSeedLiveState(t *testing.T) {
	t.Parallel()
	dir, folder := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"userID":"u","projects":{"/elsewhere":{"allowedTools":["x"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // and again: it is no harm to do twice
		if err := seedLiveState(dir, folder); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		UserID    string                    `json:"userID"`
		Onboarded bool                      `json:"hasCompletedOnboarding"`
		Projects  map[string]map[string]any `json:"projects"`
		Theme     string                    `json:"theme"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u" || !got.Onboarded || got.Theme == "" || got.Projects["/elsewhere"]["allowedTools"] == nil || got.Projects[AbsPath(folder)]["hasTrustDialogAccepted"] != true {
		t.Errorf("the state: %s", data)
	}
}

// TestLiveSettings: the session's --settings carry the Stop hook that
// brings its answers to the bridge.
func TestLiveSettings(t *testing.T) {
	t.Parallel()
	var got struct {
		Skip  bool `json:"skipDangerousModePermissionPrompt"`
		Hooks struct {
			Stop []struct {
				Hooks []struct{ Type, Command string } `json:"hooks"`
			}
		}
		Excludes []string `json:"claudeMdExcludes"`
	}
	if err := json.Unmarshal([]byte(liveSettings(t.TempDir(), t.TempDir())), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Skip || len(got.Hooks.Stop) != 1 || len(got.Hooks.Stop[0].Hooks) != 1 || got.Hooks.Stop[0].Hooks[0].Command != `"$FRIDOLIN_BIN" live-turn-end` || len(got.Excludes) == 0 {
		t.Errorf("settings: %+v", got)
	}
}
