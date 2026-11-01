package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
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
	// GH is the token of the gh login the CLI saw with a message, and
	// GHConfigDir its GH_CONFIG_DIR as it started.
	GH          string `json:"gh"`
	GHStatus    int    `json:"gh_status"`
	GHConfigDir string `json:"gh_config_dir"`
}

// liveHarness is a served bridge whose brain "main" keeps a live session
// for its owner's direct messages, with the fake live CLI's log in log.
func liveHarness(t *testing.T, mode string, extra func(h *harness)) (h *harness, log func() []liveLine) {
	t.Helper()
	return liveHarnessOf(t, manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, LiveSession: mode}, extra)
}

// liveHarnessOf is liveHarness with brain m.
func liveHarnessOf(t *testing.T, m manifest.Manifest, extra func(h *harness)) (h *harness, log func() []liveLine) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "live.log")
	setenv(t, "FRIDOLIN_FAKE_LIVE_LOG", logPath)
	optionsOf(t).waits.liveReady = 5 * time.Second
	optionsOf(t).waits.liveSettle = 0
	h = newServedHarness(t, `{"dangerously_skip_permissions": true}`)
	h.AddBrain(m)
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
		{"a brain with a GitHub grant", nil, func(s *brain.Settings) { s.Manifest.GitHub = &chat.GitHubGrant{Repos: []string{"you/a"}} }, true},
		{"a message with a GitHub grant of its own", func(m *chat.Inbound) { m.GitHub = &chat.GitHubGrant{Repos: []string{"you/a"}} }, nil, false},
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
// runs as a one-off while a new session starts, also when it comes before
// the old session's process is gone.
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

// TestLiveSessionTokenIsItsOwn: the session's brain API token, which
// speaks for the owner's direct messages for as long as it lives, is in a
// folder of its own, not in the run-tokens folder every run of the brain
// reads.
func TestLiveSessionTokenIsItsOwn(t *testing.T) {
	t.Parallel()
	h, _ := liveHarness(t, manifest.LiveSessionOwnerDM, nil)
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	h.b.live.mu.Lock()
	access := h.b.live.sess["main"].access
	h.b.live.mu.Unlock()
	data := h.b.cfgs.Boot().DataDir
	if access.TokenDir != liveTokenDir(data, "main") {
		t.Errorf("the session's token folder: %s", access.TokenDir)
	}
	files, _ := filepath.Glob(filepath.Join(runTokenDir(data, "main"), "*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), access.Token) {
			t.Errorf("%s holds the session's token", f)
		}
	}
	held := false
	files, _ = filepath.Glob(filepath.Join(access.TokenDir, "run-*.token"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && string(b) == access.Token {
			held = true
		}
	}
	if !held {
		t.Errorf("no file in %s holds the session's token: %v", access.TokenDir, files)
	}
}

// TestLiveSessionLogsInToGitHubPerTurn: a brain with a github grant of its
// own keeps its live session, whose gh has a login only while a turn is
// under way: a token of the grant minted for that turn, live as the turn
// uses it, and revoked once the turn is over, as a one-off run's. The
// session's gh folder is in its own token folder, and goes with it, also
// when it dies in a turn.
func TestLiveSessionLogsInToGitHubPerTurn(t *testing.T) {
	t.Parallel()
	fake := ghapptest.New()
	t.Cleanup(fake.Close)
	setenv(t, "FRIDOLIN_FAKE_GH_API", fake.URL)
	setenv(t, "FRIDOLIN_FAKE_LIVE_EXIT_ON", "boom")
	grant := &chat.GitHubGrant{Repos: []string{"you/fridolin"}, Permissions: map[string]string{"issues": "write"}}
	h, log := liveHarnessOf(t, manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}, LiveSession: manifest.LiveSessionOwnerDM, GitHub: grant}, func(h *harness) {
		h.b.github.client.API, h.b.github.web = fake.URL, fake.URL
		creds, err := h.b.github.client.Convert(context.Background(), ghapptest.Code)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.b.github.secrets.save(creds); err != nil {
			t.Fatal(err)
		}
	})
	h.Answer(inbound{JID: harnessDM, Text: "one"})
	h.waitLive("main")
	for _, text := range []string{"two", "three"} {
		if got := h.Answer(inbound{JID: harnessDM, Text: text}); !strings.HasPrefix(got, "live") {
			t.Fatalf("the session did not answer %q: %q", text, got)
		}
	}
	var dir string
	for _, l := range log() {
		if l.Event == "start" {
			dir = l.GHConfigDir
		}
	}
	if filepath.Dir(dir) != liveTokenDir(h.b.cfgs.Boot().DataDir, "main") {
		t.Fatalf("the session's GH_CONFIG_DIR: %q", dir)
	}
	testutil.WaitFor(t, 5*time.Second, "every token revoked", func() bool { return len(fake.Live()) == 0 })
	if _, err := os.Stat(filepath.Join(dir, "hosts.yml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("between turns the session's gh has a login: %v", err)
	}

	// It dies in a turn: that turn's token is revoked all the same, and
	// the folder goes.
	h.Answer(inbound{JID: harnessDM, Text: "boom"})
	testutil.WaitFor(t, 5*time.Second, "the token of the turn it died in revoked", func() bool { return len(fake.Live()) == 0 })
	testutil.WaitFor(t, 5*time.Second, "the session's gh folder removed", func() bool { _, err := os.Stat(dir); return errors.Is(err, fs.ErrNotExist) })

	all := fake.AllMinted()
	minted := map[string]bool{}
	for _, m := range all {
		minted[m.Token] = fmt.Sprint(m.Repos, m.Permissions) == "[fridolin] map[issues:write]"
	}
	msgs := messagesOf(log())
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.GH == "" || seen[m.GH] || !minted[m.GH] || m.GHStatus != 200 {
			t.Errorf("each turn is logged in with a live token of the grant minted for it; the turns saw %+v, minted %+v", msgs, all)
			break
		}
		seen[m.GH] = true
	}
	if len(msgs) != 3 {
		t.Errorf("turns: %d, want 3", len(msgs))
	}
}

// TestLiveSessionReady: a session takes a message only while it is up, for
// the chat it serves, and as long as the brain's settings are those it was
// started with; one that ended, or was started with other settings, makes
// way for a new one, with the message a one-off meanwhile. The reaper ends
// an idle session, never one in a turn.
func TestLiveSessionReady(t *testing.T) {
	t.Parallel()
	b := &App{waits: defaultWaits()}
	b.waits.liveIdle = 40 * time.Millisecond
	h := newLiveHost(b)
	t.Cleanup(h.Stop)
	r := &commandRunner{}
	spec := brain.RunSpec{ID: "main", Dir: t.TempDir(), Settings: &brain.Settings{Model: "opus"}, Message: chat.Inbound{JID: harnessDM}}
	session := func() *liveSession {
		s := &liveSession{brain: "main", chat: harnessDM, fingerprint: liveFingerprint(r, spec), hub: claudecli.NewLiveHub(),
			ready: true, started: time.Now(), idleSince: time.Now(), busy: true}
		h.mu.Lock()
		h.sess["main"] = s
		h.fails["main"] = liveFailure{until: time.Now().Add(time.Hour)} // no new session starts here
		h.mu.Unlock()
		return s
	}
	free := func(s *liveSession) {
		h.mu.Lock()
		s.busy, s.idleSince = false, time.Now()
		h.mu.Unlock()
	}
	stopped := func(s *liveSession) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return s.stopped
	}

	s := session()
	time.Sleep(4 * b.waits.liveIdle)
	if stopped(s) {
		t.Fatal("the reaper ended a session in a turn")
	}
	free(s)
	if got := h.ready(r, spec); got != s {
		t.Fatalf("the session for its chat and settings: %v", got)
	}
	other := spec
	other.Message.JID = "15557654321@s.whatsapp.net" // personal-data:ignore (a placeholder number)
	if got := h.ready(r, other); got != nil || stopped(s) {
		t.Errorf("another chat's message got the session, or ended it: %v", got)
	}
	changed := spec
	changed.Settings = &brain.Settings{Model: "sonnet"}
	if got := h.ready(r, changed); got != nil || !stopped(s) {
		t.Errorf("with other settings, the session took the message, or stayed: %v", got)
	}
	if got := h.ready(r, spec); got != nil {
		t.Errorf("a session that ended took a message: %v", got)
	}

	s = session()
	free(s)
	deadline := time.Now().Add(5 * time.Second)
	for !stopped(s) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !stopped(s) {
		t.Error("the reaper left an idle session")
	}
}

// TestOneOffRunInTheSessionsChatEndsIt: a scheduled agent's run, or any
// other one-off, that answers in the chat a session serves ends the session,
// as its later turns would never see that exchange; another chat's does not.
func TestOneOffRunInTheSessionsChatEndsIt(t *testing.T) {
	t.Parallel()
	b := &App{waits: defaultWaits()}
	h := newLiveHost(b)
	t.Cleanup(h.Stop)
	s := &liveSession{brain: "main", chat: harnessDM, hub: claudecli.NewLiveHub(), ready: true}
	h.mu.Lock()
	h.sess["main"] = s
	h.mu.Unlock()
	stopped := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return s.stopped
	}
	spec := brain.RunSpec{ID: "main", Settings: &brain.Settings{}, Message: chat.Inbound{JID: "15557654321@s.whatsapp.net", Source: chat.SourceScheduled}} // personal-data:ignore (a placeholder number)
	h.run(&commandRunner{}, t.Context(), spec, claudecli.Callbacks{}, nil)
	if stopped() {
		t.Fatal("a one-off run in another chat ended the session")
	}
	spec.Message.JID = harnessDM
	if _, ok := h.run(&commandRunner{}, t.Context(), spec, claudecli.Callbacks{}, nil); ok || !stopped() {
		t.Errorf("a one-off run in the session's chat: taken = %v, stopped = %v", ok, stopped())
	}
}
