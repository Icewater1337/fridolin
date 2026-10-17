package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	plaudOld1 = "of_0000000000000000000001"
	plaudOld2 = "of_0000000000000000000002"
	plaudNew  = "of_0000000000000000000003"
)

type fakePlaudFixture struct {
	dir string
	t   *testing.T
}

func newFakePlaud(t *testing.T) *fakePlaudFixture {
	dir := t.TempDir()
	t.Setenv("WCB_FAKE_PLAUD", "1")
	t.Setenv("WCB_FAKE_PLAUD_DIR", dir)
	return &fakePlaudFixture{dir: dir, t: t}
}

func (f *fakePlaudFixture) write(name, body string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// files sets what `plaud files` lists, in the CLI's table layout.
func (f *fakePlaudFixture) files(ids ...string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\nFiles on this page: %d\n\n  %-34s  %-36s  %-12s  DURATION\n  %s\n", len(ids), "ID", "NAME", "DATE", strings.Repeat("─", 98))
	for _, id := range ids {
		fmt.Fprintf(&sb, "  %-34s  %-36s  %-12s  5m00s\n", id, "Some name", "2026-10-16")
	}
	sb.WriteString("\nPage 1\n")
	f.write("files.txt", sb.String())
}

func (f *fakePlaudFixture) recording(id string, transcriptReady bool) {
	state := "unavailable"
	if transcriptReady {
		state = "available"
	}
	f.write("file_"+id+".txt", "\nFile Details:\n\n  id:           "+id+"\n  name:         Team sync\n  created_at:   2026-10-16T18:00:00\n  start_at:     2026-10-16T18:00:00\n  duration:     5m00s\n  serial_number: -\n  audio:        unknown (run `plaud audio` to check)\n  transcript:   "+state+"\n  summary:      available\n\n")
	f.write("transcript_"+id+".txt", "\nTranscript: Team sync\n\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n\n")
	f.write("summary_"+id+".txt", "\nSummary: Team sync\n\nDecided to ship on Friday.\n\n")
}

func (f *fakePlaudFixture) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	return string(b)
}

func plaudBridge(t *testing.T, extra string) (*testBridge, *fakePlaudFixture) {
	return plaudBridgeWith(t, `, "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}]`, extra)
}

// plaudBridgeWith takes top-level settings (brains, allowed numbers) and
// extra plaud settings.
func plaudBridgeWith(t *testing.T, top, plaud string) (*testBridge, *fakePlaudFixture) {
	fp := newFakePlaud(t)
	tb := newTestBridge(t, top+`, "plaud": {"enabled": true, "cli_path": "`+os.Args[0]+`"`+plaud+`}`)
	return tb, fp
}

// plaudDeliver starts from a finished sign-in and first sync (written
// directly; each fake CLI call is a process), then lets one finished
// recording arrive and polls once.
func plaudDeliver(t *testing.T, tb *testBridge, fp *fakePlaudFixture) error {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700)
	_ = os.WriteFile(tb.plaud.tokenPath(), []byte("{}"), 0o600)
	if err := tb.plaud.saveState(&plaudState{Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	return tb.plaud.Poll(context.Background())
}

// The recording runs in the brain picked for Plaud, even when that brain
// answers in the DM, which belongs to the DM brain.
func TestPlaudRunsInThePickedBrain(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb, fp := plaudBridge(t, "")
	gDir := tb.addFolderBrain(t, BrainManifest{ID: "g", Name: "Recordings", ReplyChat: dm})
	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain": "g"}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if err := plaudDeliver(t, tb, fp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	if out := tb.dev.Outbox()[0]; out.JID != dm || !strings.Contains(out.Text, "echo:"+gDir+":plaud says: [Plaud] New recording") {
		t.Errorf("reply = %+v, want it from %s in the DM", out, gDir)
	}
	if hist, _ := os.ReadFile(filepath.Join(tb.home, "p", "conversation-history.jsonl")); strings.Contains(string(hist), "[Plaud]") {
		t.Errorf("the DM brain got the recording:\n%s", hist)
	}
	if hist, _ := os.ReadFile(filepath.Join(gDir, "conversation-history.jsonl")); !strings.Contains(string(hist), "[Plaud]") {
		t.Errorf("g's history lacks the recording:\n%s", hist)
	}
	tb.plaud.mu.Lock()
	last := tb.plaud.lastForwarded
	tb.plaud.mu.Unlock()
	if last == nil || last.Brain != "g" {
		t.Errorf("last forwarded = %+v, want brain g", last)
	}
}

// Recordings are private: the answer goes to the picked brain's reply chat
// only when that is a direct chat, else to your DM, never to a group — even
// for a brain that serves one, or whose reply chat is one.
func TestPlaudSummariesStayOutOfGroups(t *testing.T) {
	const (
		dm    = "15551234567@s.whatsapp.net"
		other = "15559876543@s.whatsapp.net"
		group = "120363000000000000@g.us"
	)
	dmBrain := `{"name": "p", "work_dir": "~/p", "handles_dms": true}`
	for _, c := range []struct {
		name  string
		top   string // brains and other top-level settings
		plaud string
		add   *BrainManifest // a folder brain added after start
		brain string         // brain that must run the recording
		want  string         // chat that must get the answer
	}{
		{name: "group brain without a reply chat answers in your DM",
			top:   `, "brains": [` + dmBrain + `, {"name": "g", "work_dir": "~/g", "groups": ["` + group + `"]}]`,
			plaud: `, "brain": "g"`, brain: "g", want: dm},
		{name: "a group as reply chat is passed over for your DM",
			top:   `, "brains": [` + dmBrain + `]`,
			plaud: `, "brain": "g"`, add: &BrainManifest{ID: "g", Name: "G", ReplyChat: group}, brain: "g", want: dm},
		{name: "a direct reply chat is used",
			top:   `, "brains": [` + dmBrain + `]`,
			plaud: `, "brain": "g"`, add: &BrainManifest{ID: "g", Name: "G", ReplyChat: other}, brain: "g", want: other},
		{name: "automatic: the fallback brain that serves a group answers in your DM",
			top:   `, "brains": [{"name": "f", "work_dir": "~/f", "groups": ["` + group + `"]}]`,
			brain: "f", want: dm},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridgeWith(t, c.top, c.plaud)
			dir := filepath.Join(tb.home, c.brain)
			if c.add != nil {
				dir = tb.addFolderBrain(t, *c.add)
			}
			if err := plaudDeliver(t, tb, fp); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
			time.Sleep(100 * time.Millisecond)
			out := tb.dev.Outbox()
			for _, m := range out {
				if strings.HasSuffix(m.JID, "@g.us") {
					t.Fatalf("a recording's answer went to a group: %+v", m)
				}
			}
			if out[0].JID != c.want || !strings.Contains(out[0].Text, "echo:"+dir+":plaud says:") {
				t.Errorf("reply = %+v, want it from %s in %s", out[0], dir, c.want)
			}
		})
	}
}

// With no private chat at all, nothing is handed over: the recording waits,
// nothing is written to the media folder, and the reason shows on the
// dashboard. Once there is a DM, it arrives.
func TestPlaudWithoutAPrivateChatWaits(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "allowed_numbers": [], "brains": [{"name": "f", "work_dir": "~/f", "groups": ["120363000000000000@g.us"]}]`, "")
	err := plaudDeliver(t, tb, fp)
	if err == nil || !strings.Contains(err.Error(), "no private chat") {
		t.Fatalf("poll error = %v, want the missing private chat", err)
	}
	if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == 0 || contains(st.Seen, plaudNew) {
		t.Errorf("state = %+v, want the recording still pending", st)
	}
	if files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "*")); len(files) != 0 {
		t.Errorf("files written without a destination: %v", files)
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &status)
	if e, _ := status["last_error"].(string); !strings.Contains(e, "no private chat") {
		t.Errorf("status last_error = %q", e)
	}
	time.Sleep(100 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Fatalf("outbox = %+v, want nothing", out)
	}

	if _, err := tb.cfgs.Update("", func(c *Config) error { c.AllowedNumbers = []string{"15551234567"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	if out := tb.dev.Outbox()[0]; out.JID != "15551234567@s.whatsapp.net" {
		t.Errorf("reply = %+v, want it in the DM", out)
	}
}

// With nothing waiting, a missing private chat blocks nothing: the poll must
// not fail, and log the same error, on every interval. The card still says
// what is missing, from the status call.
func TestPlaudWithoutAPrivateChatIsQuietWhileNothingWaits(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "allowed_numbers": [], "brains": [{"name": "f", "work_dir": "~/f", "groups": ["120363000000000000@g.us"]}]`, "")
	_ = os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700)
	_ = os.WriteFile(tb.plaud.tokenPath(), []byte("{}"), 0o600)
	if err := tb.plaud.saveState(&plaudState{Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Errorf("poll with nothing waiting = %v, want nil", err)
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &status)
	if e, _ := status["summary_error"].(string); !strings.Contains(e, "no private chat") {
		t.Errorf("status summary_error = %q, want the missing private chat", e)
	}
	if e, _ := status["last_error"].(string); e != "" {
		t.Errorf("status last_error = %q, want none", e)
	}
}

// The expiry notice follows the same rule: your DM for a group brain, and
// nothing at all without a private chat (the dashboard shows the expiry).
func TestPlaudExpiryNoticeStaysPrivate(t *testing.T) {
	const group = "120363000000000000@g.us"
	for _, c := range []struct {
		name string
		top  string
		want []string // chats that get the notice
	}{
		{"group brain: your DM",
			`, "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}, {"name": "g", "work_dir": "~/g", "groups": ["` + group + `"]}]`,
			[]string{"15551234567@s.whatsapp.net"}},
		{"no private chat: nobody",
			`, "allowed_numbers": [], "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}, {"name": "g", "work_dir": "~/g", "groups": ["` + group + `"]}]`,
			nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridgeWith(t, c.top, `, "brain": "g"`)
			_ = os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700)
			_ = os.WriteFile(tb.plaud.tokenPath(), []byte("{}"), 0o600)
			fp.write("expired", "")
			if err := tb.plaud.Poll(context.Background()); err != errPlaudAuth {
				t.Fatalf("poll: err = %v", err)
			}
			var got []string
			for _, m := range tb.dev.Outbox() {
				got = append(got, m.JID)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("notice sent to %v, want %v", got, c.want)
			}
		})
	}
}

// The status tells the card where summaries go, and warns when that chat
// belongs to another brain or when there is no private chat.
func TestPlaudStatusShowsWhereSummariesGo(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	brains := `, "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}, {"name": "g", "work_dir": "~/g", "groups": ["120363000000000000@g.us"]}]`
	for _, c := range []struct {
		name  string
		top   string
		plaud string
		want  map[string]any
	}{
		{"DM brain", brains, "",
			map[string]any{"summary_chat": dm, "summary_to_you": true, "summary_chat_brain": nil, "summary_error": nil}},
		{"group brain: your DM, which the DM brain answers", brains, `, "brain": "g"`,
			map[string]any{"summary_chat": dm, "summary_to_you": true, "summary_chat_brain": "p", "summary_error": nil}},
		{"no private chat", `, "allowed_numbers": []` + brains, `, "brain": "g"`,
			map[string]any{"summary_chat": nil, "summary_error": "no private chat for Plaud summaries — add your number under Settings → Access, or set brain g's reply chat to a direct chat"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := plaudBridgeWith(t, c.top, c.plaud)
			var got map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &got)
			for k, want := range c.want {
				if got[k] != want {
					t.Errorf("%s = %#v, want %#v", k, got[k], want)
				}
			}
		})
	}
}

func TestPlaudFirstSyncThenForwardsNewRecordings(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	ctx := context.Background()
	if w := tb.do("POST", "/api/plaud/connect", nil); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitFor(t, 5*time.Second, "login", func() bool { return tb.plaud.connected() })

	// The first sync only remembers what exists.
	fp.files(plaudOld1, plaudOld2)
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := tb.plaud.loadState()
	if len(st.Seen) != 2 || len(tb.dev.Outbox()) != 0 {
		t.Fatalf("seed: state=%+v outbox=%v", st, tb.dev.Outbox())
	}

	// A new recording waits until Plaud has transcribed it.
	fp.files(plaudNew, plaudOld1, plaudOld2)
	fp.recording(plaudNew, false)
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := tb.plaud.loadState(); len(st.Pending) != 1 {
		t.Fatalf("pending = %v", st.Pending)
	}

	fp.recording(plaudNew, true)
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = tb.plaud.loadState()
	if len(st.Pending) != 0 || !contains(st.Seen, plaudNew) {
		t.Fatalf("after transcript: %+v", st)
	}
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "*Team sync.txt"))
	if len(files) != 1 {
		t.Fatalf("transcript files = %v", files)
	}
	data, _ := os.ReadFile(files[0])
	for _, want := range []string{"Plaud recording: Team sync", "=== Plaud AI summary ===", "Decided to ship", "=== Transcript ===", "[00:00 - 00:04]"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("transcript file lacks %q", want)
		}
	}
	// The brain got it like an attachment and answered in its chat.
	waitFor(t, 5*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	if out := tb.dev.Outbox()[0]; !strings.Contains(out.Text, "plaud says: [Plaud] New recording") || out.JID != "15551234567@s.whatsapp.net" {
		t.Errorf("reply = %+v", out)
	}
	hist, _ := os.ReadFile(filepath.Join(tb.home, "p", "conversation-history.jsonl"))
	if !strings.Contains(string(hist), "[attachment: "+files[0]+"]") {
		t.Error("the transcript was not handed over as an attachment")
	}

	// Polling again sends nothing twice.
	n := len(tb.dev.Outbox())
	_ = tb.plaud.Poll(ctx)
	time.Sleep(200 * time.Millisecond)
	if len(tb.dev.Outbox()) != n {
		t.Error("a recording was sent twice")
	}

	// The login is private to Fridolin, and the API never shows it.
	if !strings.Contains(fp.calls(), "HOME="+filepath.Join(tb.cfgs.Get().DataDir, "plaud", "home")) {
		t.Errorf("CLI not run with the private home:\n%s", fp.calls())
	}
	body := tb.do("GET", "/api/plaud/status", nil).Body.String()
	if strings.Contains(body, "FAKE-") || !strings.Contains(body, `"account":"you@example.com"`) {
		t.Errorf("status = %s", body)
	}
}

// plaudSignIn leaves a login in place, as a finished sign-in does.
func plaudSignIn(t *testing.T, tb *testBridge) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.plaud.tokenPath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (tb *testBridge) plaudStateExists() bool {
	_, err := os.Stat(filepath.Join(tb.plaud.dir(), "state.json"))
	return err == nil
}

// The expiry notice goes out once a day. Before the first sync its time is
// kept in memory: writing a state for it would count as the first sync.
// After it, the time is saved in the state and survives a restart.
func TestPlaudExpiredLoginNotifiesOncePerDay(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("seeded=%v", seeded), func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			if seeded {
				if err := tb.plaud.saveState(&plaudState{Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
					t.Fatal(err)
				}
			}
			fp.write("expired", "")
			for i := 0; i < 2; i++ {
				if err := tb.plaud.Poll(context.Background()); err != errPlaudAuth {
					t.Fatalf("poll %d: err = %v", i, err)
				}
			}
			out := tb.dev.Outbox()
			if len(out) != 1 || !strings.Contains(out[0].Text, "Plaud: the login expired") {
				t.Errorf("notices = %+v, want exactly one", out)
			}
			if !seeded {
				if tb.plaudStateExists() {
					t.Error("the notice wrote a state before the first sync")
				}
				return
			}
			if st, _ := tb.plaud.loadState(); st.AuthNotified == 0 {
				t.Error("the notice time was not saved in the state")
			}
			if err := NewPlaudManager(tb.Bridge).Poll(context.Background()); err != errPlaudAuth {
				t.Fatalf("poll after restart: err = %v", err)
			}
			if out := tb.dev.Outbox(); len(out) != 1 {
				t.Errorf("notices after a restart = %+v, want still one", out)
			}
		})
	}
}

// However the first sync fails, it leaves no state behind, so the next check
// syncs again and still forwards none of the recordings that already exist.
func TestPlaudFailedFirstSyncForwardsNothingOld(t *testing.T) {
	for _, c := range []struct {
		name, fixture string
		auth          bool
	}{
		{"login expired", "expired", true},
		{"Plaud unreachable", "offline", false},
		{"listing fails", "fail_files", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			fp.files(plaudOld1, plaudOld2)
			fp.recording(plaudOld1, true)
			fp.recording(plaudOld2, true)
			fp.write(c.fixture, "")
			err := tb.plaud.Poll(context.Background())
			if err == nil || errors.Is(err, errPlaudAuth) != c.auth {
				t.Fatalf("first poll: err = %v, want a failure (auth=%v)", err, c.auth)
			}
			if tb.plaudStateExists() {
				t.Fatal("a failed first sync left a state behind")
			}

			_ = os.Remove(filepath.Join(fp.dir, c.fixture))
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if st, _ := tb.plaud.loadState(); st == nil || len(st.Seen) != 2 || len(st.Pending) != 0 {
				t.Fatalf("state after the sync = %+v, want both recordings seen", st)
			}
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "*")); len(files) != 0 {
				t.Errorf("old recordings were written out: %v", files)
			}
			if strings.Contains(fp.calls(), " file ") {
				t.Errorf("old recordings were fetched:\n%s", fp.calls())
			}
			time.Sleep(100 * time.Millisecond)
			for _, m := range tb.dev.Outbox() {
				if strings.Contains(m.Text, "plaud says") {
					t.Errorf("an old recording reached the brain: %+v", m)
				}
			}
		})
	}
}

func TestPlaudBrainAccessSharesTheLogin(t *testing.T) {
	tb, fp := plaudBridge(t, `, "brain_access": true`)
	fp.files()
	_, _ = tb.plaud.run(context.Background(), "files")
	if !strings.Contains(fp.calls(), "HOME="+tb.home+" ") {
		t.Errorf("with brain_access the CLI must use the real home:\n%s", fp.calls())
	}
	if strings.Contains(mustProfile(t, tb.brainMgr.profiler, "p"), filepath.Join(tb.home, ".plaud")) {
		t.Error("with brain_access brains must be able to read ~/.plaud")
	}
	tb2, _ := plaudBridge(t, "")
	if !strings.Contains(mustProfile(t, tb2.brainMgr.profiler, "p"), filepath.Join(tb2.home, ".plaud")) {
		t.Error("without brain_access the sandbox must deny ~/.plaud")
	}
}

func TestPlaudConnectShowsTheSignInLinkWhileWaiting(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	fp.write("login_wait", "")
	if rec := tb.do("POST", "/api/plaud/connect", nil); rec.Code != 202 {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	type status struct {
		Connected bool `json:"connected"`
		Connect   struct {
			State   string `json:"state"`
			AuthURL string `json:"auth_url"`
		} `json:"connect"`
	}
	waitFor := func(what string, ok func(status) bool) {
		t.Helper()
		for i := 0; i < 200; i++ {
			var s status
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &s)
			if ok(s) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the sign-in link", func(s status) bool {
		return s.Connect.State == "running" && s.Connect.AuthURL == "https://example.com/oauth?client=x"
	})
	fp.write("login_go", "")
	waitFor("the sign-in to finish", func(s status) bool { return s.Connect.State == "done" && s.Connected })
}

func TestPlaudConfigValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{`{"plaud":{"brain":"Not A Slug"}}`, `{"plaud":{"interval_seconds":5}}`} {
		if _, err := LoadConfig(writeConfig(t, bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
