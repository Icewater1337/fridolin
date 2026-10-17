package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
	t.Setenv("FRIDOLIN_FAKE_PLAUD", "1")
	t.Setenv("FRIDOLIN_FAKE_PLAUD_DIR", dir)
	// Every CLI call is a run of this test binary. Under -race each one
	// would wait a second on exit for late race reports, which adds up to
	// minutes over the Plaud tests.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return &fakePlaudFixture{dir: dir, t: t}
}

func (f *fakePlaudFixture) write(name, body string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// files sets what `plaud files` lists, newest first.
func (f *fakePlaudFixture) files(ids ...string) {
	data, _ := json.Marshal(ids)
	f.write("list.json", string(data))
}

// recording puts a recording named "Team sync" in Plaud, with Plaud's
// summary, and with its transcript once transcriptReady.
func (f *fakePlaudFixture) recording(id string, transcriptReady bool) {
	f.put(f.teamSync(id, transcriptReady))
}

// teamSync is the recording that recording puts in Plaud, to change before
// putting it there.
func (f *fakePlaudFixture) teamSync(id string, transcriptReady bool) fakePlaudRecording {
	start := "2026-10-16T18:00:00"
	r := fakePlaudRecording{ID: id, Name: "Team sync", CreatedAt: start, StartAt: &start, Duration: 300000,
		Notes: []fakePlaudBlock{{DataType: "auto_sum_note", Content: "Decided to ship on Friday.", TabName: "Summary"}}}
	if transcriptReady {
		r.Sources = []fakePlaudBlock{{DataType: "transaction", Content: fakePlaudTranscript(fakePlaudSegment{0, 4000, "Speaker 1", "Let's ship on Friday."})}}
	}
	return r
}

// put puts r in Plaud, or replaces it there.
func (f *fakePlaudFixture) put(r fakePlaudRecording) {
	data, _ := json.Marshal(r)
	f.write("rec_"+r.ID+".json", string(data))
}

func (f *fakePlaudFixture) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	return string(b)
}

// plaudDMBrain is brain p, which gets the DMs: the one brain of plaudBridge.
var plaudDMBrain = BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}}

// plaudGroupBrain is a brain id that serves the group and no DMs.
func plaudGroupBrain(id string) BrainManifest {
	return BrainManifest{ID: id, Name: strings.ToUpper(id), Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}}
}

func plaudBridge(t *testing.T, extra string) (*testBridge, *fakePlaudFixture) {
	return plaudBridgeWith(t, "", extra, plaudDMBrain)
}

// plaudBridgeWith takes top-level settings (allowed numbers), extra plaud
// settings and the folder brains to add once the bridge runs.
func plaudBridgeWith(t *testing.T, top, plaud string, brains ...BrainManifest) (*testBridge, *fakePlaudFixture) {
	fp := newFakePlaud(t)
	tb := newTestBridge(t, top+`, "plaud": {"enabled": true, "cli_path": "`+os.Args[0]+`"`+plaud+`}`)
	for _, m := range brains {
		tb.addFolderBrain(t, m)
	}
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

// plaudDeliverRecording is plaudDeliver for the recording r.
func plaudDeliverRecording(t *testing.T, tb *testBridge, fp *fakePlaudFixture, r fakePlaudRecording) error {
	t.Helper()
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(&plaudState{Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(r.ID, plaudOld1)
	fp.put(r)
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
	for _, c := range []struct {
		name   string
		plaud  string
		brains []BrainManifest
		brain  string // brain that must run the recording
		want   string // chat that must get the answer
	}{
		{name: "group brain without a reply chat answers in your DM",
			brains: []BrainManifest{plaudDMBrain, plaudGroupBrain("g")},
			plaud:  `, "brain": "g"`, brain: "g", want: dm},
		{name: "a group as reply chat is passed over for your DM",
			brains: []BrainManifest{plaudDMBrain, {ID: "g", Name: "G", ReplyChat: group}},
			plaud:  `, "brain": "g"`, brain: "g", want: dm},
		{name: "a direct reply chat is used",
			brains: []BrainManifest{plaudDMBrain, {ID: "g", Name: "G", ReplyChat: other}},
			plaud:  `, "brain": "g"`, brain: "g", want: other},
		{name: "automatic: the fallback brain that serves a group answers in your DM",
			brains: []BrainManifest{plaudGroupBrain("f")},
			brain:  "f", want: dm},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridgeWith(t, "", c.plaud, c.brains...)
			dir := filepath.Join(tb.home, c.brain)
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
	tb, fp := plaudBridgeWith(t, `, "allowed_numbers": []`, "", plaudGroupBrain("f"))
	err := plaudDeliver(t, tb, fp)
	if err == nil || !strings.Contains(err.Error(), "no private chat") {
		t.Fatalf("poll error = %v, want the missing private chat", err)
	}
	if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == 0 || slices.Contains(st.Seen, plaudNew) {
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

// A picked brain that went away (removed, renamed, not approved) is
// reported by name, on the card and as the check's error, and nothing is
// written or handed over until another one is picked. The settings refuse
// a brain that is not running, even one the brain list shows (its folder
// is gone), but still save the rest while the picked one is gone.
func TestPlaudMissingBrainIsReported(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "brain_dirs": [{"path": "~/gone", "approved": {"id": "gone", "name": "Gone"}}]`, `, "brain": "ghost"`, plaudDMBrain)
	err := plaudDeliver(t, tb, fp)
	if err == nil || !strings.Contains(err.Error(), "brain ghost") {
		t.Fatalf("poll error = %v, want the missing brain named", err)
	}
	if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == 0 {
		t.Errorf("state = %+v, want the recording still pending", st)
	}
	if tb.plaudForwarded() {
		t.Error("a transcript was written without a brain to give it to")
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &status)
	for _, k := range []string{"summary_error", "last_error"} {
		if e, _ := status[k].(string); !strings.Contains(e, "brain ghost") {
			t.Errorf("status %s = %q, want the missing brain named", k, e)
		}
	}
	if status["brain_missing"] != true {
		t.Errorf("status brain_missing = %v, want true", status["brain_missing"])
	}

	if body := tb.do("GET", "/api/brains", nil).Body.String(); !strings.Contains(body, `"id":"gone"`) {
		t.Fatalf("the brain list lacks the brain whose folder is gone: %s", body)
	}
	for _, c := range []struct {
		body map[string]any
		code int
		want string // in the answer
	}{
		{map[string]any{"brain": "nope"}, 422, `there is no approved brain \"nope\"`},
		{map[string]any{"brain": "gone"}, 422, `there is no approved brain \"gone\"`},
		{map[string]any{"brain": "ghost", "polished": true}, 200, "saved"},
		{map[string]any{"brain": "p"}, 200, "saved"},
		{map[string]any{"brain": ""}, 200, "saved"},
	} {
		if w := tb.do("PUT", "/api/plaud/settings", c.body); w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("PUT %v = %d %s, want %d %s", c.body, w.Code, w.Body.String(), c.code, c.want)
		}
	}
	if pc := tb.cfgs.Get().Plaud; pc.Brain != "" || !pc.Polished {
		t.Errorf("settings = %+v, want polished saved and the brain back to automatic", pc)
	}
}

// A recording that cannot be fetched is not counted as waiting for Plaud:
// the check fails with its error, which the card shows, and the recording
// arrives once the problem is gone.
func TestPlaudDeliveryErrorIsShown(t *testing.T) {
	for _, fixture := range []string{"fail_file", "fail_transcript"} {
		t.Run(fixture, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			fp.write(fixture, "")
			err := plaudDeliver(t, tb, fp)
			if err == nil || !strings.Contains(err.Error(), "recording "+plaudNew) {
				t.Fatalf("poll error = %v, want the recording's error", err)
			}
			if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == 0 {
				t.Errorf("state = %+v, want the recording still pending", st)
			}
			var status map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &status)
			if e, _ := status["last_error"].(string); !strings.Contains(e, plaudNew) || !strings.Contains(e, "exit status 1") {
				t.Errorf("status last_error = %q, want the recording's error", e)
			}

			_ = os.Remove(filepath.Join(fp.dir, fixture))
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
			var after map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status?account=0", nil).Body.Bytes(), &after)
			if e, ok := after["last_error"]; ok {
				t.Errorf("status last_error = %q after it was delivered", e)
			}
		})
	}
}

// A recording that keeps failing is given up after three days, like one
// Plaud never transcribes, and its error then says so instead of promising
// another try. The log blames Plaud only for the one it never transcribed.
func TestPlaudFailingRecordingIsGivenUpInTime(t *testing.T) {
	for _, c := range []struct {
		name    string
		age     time.Duration // since the recording was first seen
		ready   bool          // whether Plaud has its transcript
		fails   bool          // whether fetching it fails
		says    string        // in the check's error; "" for none
		logs    string        // in the log; "" for no giving up
		pending bool          // whether it is tried again
	}{
		{"recent", time.Hour, true, true, "(will retry)", "", true},
		{"failing for over three days", plaudPendingMaxAge + time.Hour, true, true, "(given up after 3 days)", "still failing after 3 days, giving up", false},
		{"untranscribed for over three days", plaudPendingMaxAge + time.Hour, false, false, "", "no transcript after 3 days, giving up", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			first := float64(time.Now().Add(-c.age).Unix())
			if err := tb.plaud.saveState(&plaudState{Account: "u_1", Seen: []string{plaudOld1}, Pending: map[string]float64{plaudNew: first}}); err != nil {
				t.Fatal(err)
			}
			fp.files(plaudNew, plaudOld1)
			fp.recording(plaudNew, c.ready)
			if c.fails {
				fp.write("fail_file", "")
			}
			logs := captureLog(t)
			err := tb.plaud.Poll(context.Background())
			if c.says == "" && err != nil || c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)) {
				t.Fatalf("poll error = %v, want it to say %q", err, c.says)
			}
			if got := strings.Join(logs(), "\n"); strings.Contains(got, "giving up") != (c.logs != "") || !strings.Contains(got, c.logs) {
				t.Errorf("log = %q, want it to say %q", got, c.logs)
			}
			st, _ := tb.plaud.loadState()
			if _, pending := st.Pending[plaudNew]; pending != c.pending || slices.Contains(st.Seen, plaudNew) == c.pending {
				t.Errorf("state = %+v, want pending=%v", st, c.pending)
			}
		})
	}
}

// With nothing waiting, a missing private chat blocks nothing: the poll must
// not fail, and log the same error, on every interval. The card still says
// what is missing, from the status call.
func TestPlaudWithoutAPrivateChatIsQuietWhileNothingWaits(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "allowed_numbers": []`, "", plaudGroupBrain("f"))
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
	for _, c := range []struct {
		name string
		top  string
		want []string // chats that get the notice
	}{
		{"group brain: your DM", "", []string{"15551234567@s.whatsapp.net"}},
		{"no private chat: nobody", `, "allowed_numbers": []`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridgeWith(t, c.top, `, "brain": "g"`, plaudDMBrain, plaudGroupBrain("g"))
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
	for _, c := range []struct {
		name  string
		top   string
		plaud string
		want  map[string]any
	}{
		{"DM brain", "", "",
			map[string]any{"summary_chat": dm, "summary_to_you": true, "summary_chat_brain": nil, "summary_error": nil}},
		{"group brain: your DM, which the DM brain answers", "", `, "brain": "g"`,
			map[string]any{"summary_chat": dm, "summary_to_you": true, "summary_chat_brain": "p", "summary_error": nil}},
		{"no private chat", `, "allowed_numbers": []`, `, "brain": "g"`,
			map[string]any{"summary_chat": nil, "summary_error": "no private chat for Plaud summaries — add your number under Chats → Allowed numbers, or set brain g's reply chat to a direct chat"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := plaudBridgeWith(t, c.top, c.plaud, plaudDMBrain, plaudGroupBrain("g"))
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
	if len(st.Pending) != 0 || !slices.Contains(st.Seen, plaudNew) {
		t.Fatalf("after transcript: %+v", st)
	}
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "* - Team sync - "+plaudNew+".txt"))
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
	token, err := os.ReadFile(tb.plaud.tokenPath())
	if err != nil || !strings.Contains(string(token), "test-access") {
		t.Fatalf("the sign-in left no login: %q, %v", token, err)
	}
	body := tb.do("GET", "/api/plaud/status", nil).Body.String()
	if strings.Contains(body, "test-access") || !strings.Contains(body, `"account":"you@example.com"`) {
		t.Errorf("status = %s", body)
	}
}

// plaudAfterRestart is a new manager on tb's configuration and brains, as
// after a restart, wired the way the bridge wires its own.
func plaudAfterRestart(tb *testBridge) *PlaudManager {
	p := NewPlaudManager(tb.cfgs, tb.brainMgr)
	p.handOff, p.notify = tb.handOff, tb.wa.SendMessage
	return p
}

// The manager needs only the configuration, the brains and the two
// functions it is given: it hands each recording to handOff and says
// nothing else to the bridge.
func TestPlaudManagerUsesOnlyWhatItIsGiven(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	type handed struct {
		brain, jid, sender, text string
		media                    []string
	}
	var got []handed
	p := NewPlaudManager(tb.cfgs, tb.brainMgr)
	p.handOff = func(br *Brain, jid, sender, text string, media []string) {
		got = append(got, handed{br.ID(), jid, sender, text, media})
	}
	p.notify = func(context.Context, string, string) error { t.Error("notify called"); return nil }
	plaudSignIn(t, tb)
	if err := p.saveState(&plaudState{Account: "u_1", Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("handed over %d times, want once: %+v", len(got), got)
	}
	h := got[0]
	if h.brain != "p" || h.jid != "15551234567@s.whatsapp.net" || h.sender != "plaud" || !strings.HasPrefix(h.text, "[Plaud] New recording") ||
		len(h.media) != 1 || !strings.HasSuffix(h.media[0], " - "+plaudNew+".txt") {
		t.Errorf("handed %+v", h)
	}
	time.Sleep(100 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("the bridge sent %+v, want nothing: only handOff was to be used", out)
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
			if err := plaudAfterRestart(tb).Poll(context.Background()); err != errPlaudAuth {
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
		{"login refused", "revoked", true},
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

			// Plaud's CLI deletes a login that expired: you sign in again.
			_ = os.Remove(filepath.Join(fp.dir, c.fixture))
			plaudSignIn(t, tb)
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

// plaudForwarded reports whether a recording was written out for a brain.
func (tb *testBridge) plaudForwarded() bool {
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "*.txt"))
	return len(files) > 0
}

// A state belongs to the account it was seeded for. Another account starts
// over, so its existing recordings are not sent; the same account keeps its
// place, so what it recorded while it was signed out still arrives.
func TestPlaudStateBelongsToTheAccount(t *testing.T) {
	for _, c := range []struct {
		name    string
		state   plaudState
		account string // who `me` says is signed in
		forward bool   // whether plaudNew is sent
		logs    string // why the log says it starts over, if it does
	}{
		{"another account", plaudState{Account: "u_1", Seen: []string{plaudOld1}}, "u_2", false, "signed in to another Plaud account"},
		{"the same account again", plaudState{Account: "u_1", Seen: []string{plaudOld1}}, "u_1", true, ""},
		{"a state from before accounts were recorded", plaudState{Seen: []string{plaudOld1}}, "u_1", true, ""},
		{"a state only the expiry notice wrote", plaudState{AuthNotified: 1}, "u_1", false, "only held an expiry notice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLog(t)
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			c.state.Pending = map[string]float64{}
			if err := tb.plaud.saveState(&c.state); err != nil {
				t.Fatal(err)
			}
			fp.write("account", c.account)
			fp.files(plaudNew, plaudOld1)
			fp.recording(plaudNew, true)
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := tb.plaudForwarded(); got != c.forward {
				t.Errorf("forwarded = %v, want %v", got, c.forward)
			}
			if c.forward {
				waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
			}
			if st, _ := tb.plaud.loadState(); st.Account != c.account || !slices.Contains(st.Seen, plaudNew) {
				t.Errorf("state = %+v, want account %s with the recording seen", st, c.account)
			}
			if got := strings.Join(logs(), "\n"); strings.Contains(got, "starting over") != (c.logs != "") || !strings.Contains(got, c.logs) {
				t.Errorf("log = %q, want it to say %q", got, c.logs)
			}
		})
	}
}

// The account is checked again when the login changes, and only then.
func TestPlaudChecksTheAccountWhenTheLoginChanges(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(&plaudState{Account: "u_1", Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	for i := 0; i < 2; i++ {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(fp.calls(), " me\n"); n != 1 {
		t.Errorf("`plaud me` ran %d times in two polls, want once", n)
	}

	// Signed out, and in again with another account.
	fp.write("account", "u_2")
	plaudSignIn(t, tb)
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(tb.plaud.tokenPath(), later, later)
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(fp.calls(), " me\n"); n != 2 {
		t.Errorf("`plaud me` ran %d times, want again after the new login", n)
	}
	if tb.plaudForwarded() {
		t.Error("the other account's existing recording was sent")
	}
	if st, _ := tb.plaud.loadState(); st.Account != "u_2" || !slices.Contains(st.Seen, plaudNew) {
		t.Errorf("state = %+v, want it seeded for u_2", st)
	}
}

// A login that changes in the middle of a poll ("Connect again" with another
// account finishing, brain_access switching) is not trusted for that poll:
// what Plaud lists may already be the other account's, and none of it may
// be sent. The next poll checks the account and starts over for it.
func TestPlaudLoginChangingDuringAPollSendsNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		state  *plaudState // nil: no first sync yet
		after  string      // the login changes once this command ran…
		before string      // …or just before this one runs
	}{
		{"while the first sync lists the recordings", nil, "", "files"},
		{"just after the account was looked up", &plaudState{Account: "u_1", Seen: []string{plaudOld1}}, "me", ""},
		{"before the recordings are listed", &plaudState{Account: "u_1", Seen: []string{plaudOld1}}, "", "files"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			if c.state != nil {
				c.state.Pending = map[string]float64{}
				if err := tb.plaud.saveState(c.state); err != nil {
					t.Fatal(err)
				}
			}
			fp.files(plaudOld1)
			for _, id := range []string{plaudOld2, plaudNew} {
				fp.recording(id, true)
			}
			// u_2 signs in: `me` names it, Plaud lists its recordings, and
			// the login file is rewritten.
			switched := false
			switchAccount := func() {
				switched = true
				fp.write("account", "u_2")
				fp.files(plaudNew, plaudOld2)
				if err := os.WriteFile(tb.plaud.tokenPath(), []byte(`{"user":"u_2"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				later := time.Now().Add(time.Minute)
				_ = os.Chtimes(tb.plaud.tokenPath(), later, later)
			}
			run := tb.plaud.run
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if !switched && args[0] == c.before {
					switchAccount()
				}
				out, err := run(ctx, args...)
				if !switched && args[0] == c.after {
					switchAccount()
				}
				return out, err
			}

			for i := 0; i < 3; i++ {
				if err := tb.plaud.Poll(context.Background()); err != nil {
					t.Fatalf("poll %d: %v", i, err)
				}
			}
			if !switched {
				t.Fatal("the login never changed")
			}
			if tb.plaudForwarded() {
				t.Error("the other account's existing recordings were sent")
			}
			st, _ := tb.plaud.loadState()
			if st.Account != "u_2" || !slices.Contains(st.Seen, plaudNew) || !slices.Contains(st.Seen, plaudOld2) || len(st.Pending) != 0 {
				t.Errorf("state = %+v, want it seeded for u_2", st)
			}
		})
	}
}

// plaudDelivered returns the transcript file written for recording id.
func (tb *testBridge) plaudDelivered(t *testing.T, id string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "* - "+id+".txt"))
	if len(files) != 1 {
		t.Fatalf("transcript files for %s = %v, want one", id, files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(files[0]) + "\n" + string(data)
}

// The transcript file holds what Plaud has, read the way the real CLI
// prints it: the summary whole and without the "Summary: <name>" header,
// even when a line of it starts like the spinner; the transcript without
// its header; the recording's own date even when Plaud has no start time;
// and no summary for an empty note.
func TestPlaudForwardReadsWhatTheCLIPrints(t *testing.T) {
	created := time.Date(2026, 10, 15, 7, 30, 0, 0, time.UTC).Local()
	for _, c := range []struct {
		name    string
		change  func(r *fakePlaudRecording)
		want    []string // in the file, which starts with its name
		notWant []string
	}{
		{"a summary line that starts like the spinner",
			func(r *fakePlaudRecording) { r.Notes[0].Content = plaudGoldenSummary },
			[]string{"=== Plaud AI summary ===\n" + plaudGoldenSummary + "\n\n=== Transcript ===\n"},
			[]string{"Team sync\n## Team sync"}},
		{"the transcript without its header",
			func(*fakePlaudRecording) {},
			[]string{"=== Transcript ===\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n"},
			[]string{"Transcript: Team sync"}},
		{"no start time: the day it reached Plaud",
			func(r *fakePlaudRecording) { r.StartAt, r.CreatedAt = nil, "2026-10-15T07:30:00" },
			[]string{created.Format("02.01.2006") + " - Team sync - ", "Recorded: " + created.Format("02.01.2006 15:04") + "\n"},
			[]string{"Recorded: -"}},
		{"an empty summary note",
			func(r *fakePlaudRecording) { r.Notes[0].Content = "" },
			nil,
			[]string{"=== Plaud AI summary ===", "no content yet"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			r := fp.teamSync(plaudNew, true)
			c.change(&r)
			if err := plaudDeliverRecording(t, tb, fp, r); err != nil {
				t.Fatal(err)
			}
			got := tb.plaudDelivered(t, plaudNew)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("the file lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got, w) {
					t.Errorf("the file has %q:\n%s", w, got)
				}
			}
		})
	}
}

// With plaud.polished the cleaned-up transcript is sent when Plaud has
// one. Plaud makes the raw transcript first and may never make the other,
// so after plaudPolishedWait the raw one goes instead, with a note that
// says so; before that the recording waits.
func TestPlaudPolishedFallsBackToTheRawTranscript(t *testing.T) {
	raw := "[00:00 - 00:04] Speaker 1: Let's ship on Friday."
	for _, c := range []struct {
		name     string
		polished *string // the polished block's segment, nil for none
		age      time.Duration
		want     string // the transcript sent, "" for none yet
		note     bool
	}{
		{"cleaned up", ptr("Let us ship on Friday."), time.Minute, "[00:00 - 00:04] Speaker 1: Let us ship on Friday.", false},
		{"none yet", nil, time.Minute, "", false},
		{"an empty one yet", ptr(""), time.Minute, "", false},
		{"none after the wait", nil, plaudPolishedWait + time.Minute, raw, true},
		{"an empty one after the wait", ptr(""), plaudPolishedWait + time.Minute, raw, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, `, "polished": true`)
			plaudSignIn(t, tb)
			first := float64(time.Now().Add(-c.age).Unix())
			if err := tb.plaud.saveState(&plaudState{Account: "u_1", Seen: []string{plaudOld1}, Pending: map[string]float64{plaudNew: first}}); err != nil {
				t.Fatal(err)
			}
			r := fp.teamSync(plaudNew, true)
			if c.polished != nil {
				b := fakePlaudBlock{DataType: "transaction_polish"}
				if *c.polished != "" {
					b.Content = fakePlaudTranscript(fakePlaudSegment{0, 4000, "Speaker 1", *c.polished})
				}
				r.Sources = append(r.Sources, b)
			}
			fp.files(plaudNew, plaudOld1)
			fp.put(r)
			logs := captureLog(t)
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if tb.plaudForwarded() {
					t.Fatal("handed over before the cleaned-up transcript was there or the wait was up")
				}
				if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == 0 {
					t.Errorf("state = %+v, want the recording still waiting", st)
				}
				return
			}
			got := tb.plaudDelivered(t, plaudNew)
			if !strings.Contains(got, "=== Transcript ===\n"+c.want+"\n") {
				t.Errorf("the file lacks the transcript %q:\n%s", c.want, got)
			}
			if strings.Contains(got, "this is the raw one") != c.note || logContains(logs, "sending the raw one") != c.note {
				t.Errorf("note about the raw transcript in the file or the log, want %v:\n%s\n%q", c.note, got, logs())
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// A recording handed over again, as after a crash before the state was
// saved, rewrites its transcript file instead of adding a copy, even when
// it was renamed in Plaud in between.
func TestPlaudRetryRewritesTheSameTranscript(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	if err := plaudDeliver(t, tb, fp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })

	renamed := fp.teamSync(plaudNew, true)
	renamed.Name = "Weekly sync"
	fp.put(renamed)
	if err := tb.plaud.saveState(&plaudState{Seen: []string{plaudOld1}, Pending: map[string]float64{plaudNew: float64(time.Now().Unix())}}); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "second brain reply", func() bool { return len(tb.dev.Outbox()) == 2 })

	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "plaud", "*"))
	if len(files) != 1 || !strings.HasSuffix(files[0], " - "+plaudNew+".txt") {
		t.Fatalf("transcript files = %v, want one named after the recording id", files)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "Plaud recording: Weekly sync") {
		t.Errorf("the file was not rewritten:\n%s", data)
	}
}

// Brains may write to the media folder, so one can put a symlink where a
// recording's transcript will go. The transcript replaces the link instead
// of being written through it to the file it points to.
func TestPlaudTranscriptReplacesASymlink(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tb.cfgs.Get().MediaDir, "plaud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "01.01.2026 - x - "+plaudNew+".txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := plaudDeliver(t, tb, fp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })

	if data, _ := os.ReadFile(target); string(data) != "keep me" {
		t.Errorf("the transcript was written through the link:\n%s", data)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("the transcript's entry is %v, want a regular file in place of the link", fi.Mode())
	}
	if data, _ := os.ReadFile(link); !strings.Contains(string(data), "=== Transcript ===\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.") {
		t.Errorf("the file lacks the transcript:\n%s", data)
	}
}

// The earlier transcript is found whatever characters the media folder's
// path holds, and only the file of that very recording.
func TestPlaudTranscriptPathFindsTheEarlierFile(t *testing.T) {
	const id = "of_00000000000000000000aa"
	for _, folder := range []string{"media", "media [1]", "media[", "media *?"} {
		t.Run(folder, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), folder)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			fresh := filepath.Join(dir, "17.10.2026 - Weekly sync - "+id+".txt")
			if got := plaudTranscriptPath(dir, "17.10.2026", "Weekly sync", id); got != fresh {
				t.Errorf("with no earlier file: %s, want %s", got, fresh)
			}
			for _, other := range []string{"16.10.2026 - Team sync - " + id + "b.txt", "16.10.2026 - Team sync - x" + id + ".txt"} {
				if err := os.WriteFile(filepath.Join(dir, other), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			earlier := filepath.Join(dir, "16.10.2026 - Team sync - "+id+".txt")
			if err := os.WriteFile(earlier, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := plaudTranscriptPath(dir, "17.10.2026", "Weekly sync", id); got != earlier {
				t.Errorf("with an earlier file: %s, want %s", got, earlier)
			}
		})
	}
}

// "Check now" hands new recordings to a brain, so with sending turned off
// it is refused, and a check asked for anyway does nothing: one from the
// poller, or one Check now started just before the switch went off.
func TestPlaudCheckNowNeedsSendingOn(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(&plaudState{Account: "u_1", Seen: []string{plaudOld1}, Pending: map[string]float64{}}); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"enabled": false}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 409 {
		t.Errorf("check now with sending off = %d %s, want 409", w.Code, w.Body.String())
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tb.plaud.beginPoll() {
		t.Fatal("a check is still running")
	}
	if err := tb.plaud.runPoll(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if calls := fp.calls(); calls != "" || tb.plaudForwarded() {
		t.Fatalf("Plaud was checked with sending off:\n%s", calls)
	}

	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"enabled": true}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 202 {
		t.Fatalf("check now = %d %s, want 202", w.Code, w.Body.String())
	}
	waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
}

// While a check is running, "Check now" says so instead of claiming to
// start another one that would do nothing.
func TestPlaudCheckNowWhileChecking(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	fp.files(plaudOld1)
	for _, c := range []struct {
		busy bool
		code int
		want string
	}{
		{true, 200, "already checking"},
		{false, 202, "syncing"},
	} {
		tb.plaud.mu.Lock()
		tb.plaud.busy = c.busy
		tb.plaud.mu.Unlock()
		w := tb.do("POST", "/api/plaud/sync", nil)
		var got struct{ Status string }
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != c.code || got.Status != c.want {
			t.Errorf("check now (busy=%v) = %d %q, want %d %q", c.busy, w.Code, got.Status, c.code, c.want)
		}
	}
	waitFor(t, 10*time.Second, "the check to finish", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return !tb.plaud.busy && tb.plaudStateExists()
	})
}

// A check that Check now started ends when Fridolin stops, like the
// poller's own checks, instead of running on for up to ten minutes.
func TestPlaudStopEndsCheckNow(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	fp.files(plaudOld1)
	tb.plaud.Start(context.Background())
	tb.plaud.Stop()
	if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 202 {
		t.Fatalf("check now = %d %s, want 202", w.Code, w.Body.String())
	}
	waitFor(t, 10*time.Second, "the check to end", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return !tb.plaud.busy
	})
	if calls := fp.calls(); calls != "" {
		t.Errorf("Plaud was checked after Fridolin stopped:\n%s", calls)
	}
	tb.plaud.mu.Lock()
	lastErr := tb.plaud.lastErr
	tb.plaud.mu.Unlock()
	if !strings.Contains(lastErr, context.Canceled.Error()) {
		t.Errorf("last error = %q, want the check cancelled", lastErr)
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
		return s.Connect.State == "running" && s.Connect.AuthURL == "https://web.plaud.ai/platform/oauth?client_id=x&state=y"
	})
	fp.write("login_go", "")
	waitFor("the sign-in to finish", func(s status) bool { return s.Connect.State == "done" && s.Connected })
}

// The CLI runs in its own home, not in the bridge's working folder (the
// data folder under the service), whose .env it would load. It runs in a
// process group of its own, so a check that is cancelled ends with all of
// it, and what it prints is capped.
func TestPlaudCLIRunsContained(t *testing.T) {
	t.Run("in its home", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		if _, err := tb.plaud.run(context.Background(), "logout"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fp.calls(), "CWD="+tb.plaud.cliHome()+" logout") {
			t.Errorf("the CLI did not run in %s:\n%s", tb.plaud.cliHome(), fp.calls())
		}
	})
	t.Run("a cancel ends what it started", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		plaudSignIn(t, tb)
		fp.write("hang", "")
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := tb.plaud.run(ctx, "files"); err == nil {
			t.Fatal("a cancelled run reported success")
		}
		if d := time.Since(start); d > 8*time.Second {
			t.Errorf("the cancelled run took %v to end", d)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(func() string { b, _ := os.ReadFile(filepath.Join(fp.dir, "child.pid")); return string(b) }()))
		if pid <= 0 {
			t.Fatal("the fake started no child")
		}
		waitFor(t, 5*time.Second, "the child to end", func() bool { return syscall.Kill(pid, 0) != nil })
	})
	t.Run("its output is capped", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		plaudSignIn(t, tb)
		fp.write("flood", "")
		out, err := tb.plaud.run(context.Background(), "files")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != plaudOutputLimit {
			t.Errorf("kept %d bytes of the output, want the first %d", len(out), plaudOutputLimit)
		}
	})
}

// A sign-in that fails says why, from the CLI's exit status: a denial is
// not an expired login, and a timeout says how long the CLI waits.
func TestPlaudSignInSaysWhyItFailed(t *testing.T) {
	for _, c := range []struct {
		fixture string
		want    string
	}{
		{"login_deny", "access was not granted"},
		{"login_timeout", "the sign-in timed out: Plaud's tool waits two minutes"},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			fp.write(c.fixture, "")
			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			var job connectJob
			waitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				job = *tb.plaud.Status(context.Background(), false).Connect
				return job.State != "running"
			})
			if job.State != "error" || !strings.Contains(job.Error, c.want) || strings.Contains(job.Error, "expired") {
				t.Errorf("sign-in = %+v, want an error saying %q", job, c.want)
			}
		})
	}
}

func TestPlaudConfigValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{`{"plaud":{"brain":"Not A Slug"}}`, `{"plaud":{"interval_seconds":5}}`} {
		if _, err := LoadConfig(writeConfig(t, bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

// TestPlaudCLIGetsTheAllowlistedEnvironment: the Plaud CLI gets what
// childEnv gives a helper program, with the bridge's HOME, PATH and
// DO_NOT_TRACK replaced by its own: not the Claude CLI's login, and not the
// names in child_env_passthrough, which are for brains. Only `plaud login`,
// which opens your browser, gets your desktop session's variables.
func TestPlaudCLIGetsTheAllowlistedEnvironment(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "child_env_passthrough": ["GROQ_API_KEY"]`, "")
	for k, v := range map[string]string{
		"HTTPS_PROXY":             "http://proxy.example.com:3128",
		"DO_NOT_TRACK":            "0",
		"DISPLAY":                 ":0",
		"GROQ_API_KEY":            "test-groq-key",
		"CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-token",
		"FRIDOLIN_TEST_SECRET":    "test-secret",
	} {
		t.Setenv(k, v)
	}
	bin, err := tb.plaud.cliPath()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		run     func(t *testing.T)
		desktop bool
	}{
		{"a sync", func(t *testing.T) {
			plaudSignIn(t, tb) // like the real CLI, the fake answers only with a login
			if _, err := tb.plaud.runCLI(context.Background(), "me"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"the sign-in", func(t *testing.T) {
			_ = os.Remove(tb.plaud.tokenPath()) // signed out again, so the sign-in runs
			if w := tb.do("POST", "/api/plaud/connect", nil); w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			waitFor(t, 5*time.Second, "login", func() bool { return tb.plaud.connected() })
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t)
			data, err := os.ReadFile(filepath.Join(fp.dir, "env.log"))
			if err != nil {
				t.Fatal(err)
			}
			got := map[string][]string{}
			for _, kv := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
				k, v, _ := strings.Cut(kv, "=")
				got[k] = append(got[k], v)
			}
			for k, want := range map[string]string{
				"HOME":                     tb.plaud.cliHome(),
				"PATH":                     plaudPATH(bin),
				"HTTPS_PROXY":              "http://proxy.example.com:3128",
				"DO_NOT_TRACK":             "1",
				"PLAUD_TELEMETRY_DISABLED": "1",
			} {
				if v := got[k]; len(v) != 1 || v[0] != want {
					t.Errorf("the CLI got %s=%q, want %q once", k, v, want)
				}
			}
			if _, ok := got["DISPLAY"]; ok != tc.desktop {
				t.Errorf("the CLI got DISPLAY: %v, want %v", ok, tc.desktop)
			}
			for _, k := range []string{"GROQ_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "FRIDOLIN_TEST_SECRET"} {
				if v, ok := got[k]; ok {
					t.Errorf("the CLI got %s=%q", k, v)
				}
			}
		})
	}
}
