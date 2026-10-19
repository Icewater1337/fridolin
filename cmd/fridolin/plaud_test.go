package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
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
var plaudDMBrain = manifest.Manifest{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true}}

// plaudGroupBrain is a brain id that serves the group and no DMs.
func plaudGroupBrain(id string) manifest.Manifest {
	return manifest.Manifest{ID: id, Name: strings.ToUpper(id), Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}}
}

func plaudBridge(t *testing.T, extra string) (*testBridge, *fakePlaudFixture) {
	return plaudBridgeWith(t, "", extra, plaudDMBrain)
}

// plaudBridgeWith takes top-level settings (allowed numbers), extra plaud
// settings and the folder brains to add once the bridge runs.
func plaudBridgeWith(t *testing.T, top, plaud string, brains ...manifest.Manifest) (*testBridge, *fakePlaudFixture) {
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
	if err := tb.plaud.saveState(plaudSeededState("", plaudOld1)); err != nil {
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
	if err := tb.plaud.saveState(plaudSeededState("", plaudOld1)); err != nil {
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
	gDir := tb.addFolderBrain(t, manifest.Manifest{ID: "g", Name: "Recordings", ReplyChat: dm})
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
	// The transcript is in g's inbox, which the DM brain cannot read.
	media := tb.cfgs.Get().MediaDir
	if files, _ := filepath.Glob(filepath.Join(media, "*", "plaud", "*.txt")); len(files) != 1 || filepath.Dir(files[0]) != filepath.Join(media, "g", "plaud") {
		t.Errorf("transcripts = %v, want one in g's inbox", files)
	}
	waitFor(t, 10*time.Second, "the run to be noted", func() bool {
		last := tb.plaud.Status().LastForwarded
		return last != nil && last.Brain == "g" && last.Status == plaudRunDone
	})
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
		brains []manifest.Manifest
		brain  string // brain that must run the recording
		want   string // chat that must get the answer
	}{
		{name: "group brain without a reply chat answers in your DM",
			brains: []manifest.Manifest{plaudDMBrain, plaudGroupBrain("g")},
			plaud:  `, "brain": "g"`, brain: "g", want: dm},
		{name: "a group as reply chat is passed over for your DM",
			brains: []manifest.Manifest{plaudDMBrain, {ID: "g", Name: "G", ReplyChat: group}},
			plaud:  `, "brain": "g"`, brain: "g", want: dm},
		{name: "a direct reply chat is used",
			brains: []manifest.Manifest{plaudDMBrain, {ID: "g", Name: "G", ReplyChat: other}},
			plaud:  `, "brain": "g"`, brain: "g", want: other},
		{name: "automatic: the fallback brain that serves a group answers in your DM",
			brains: []manifest.Manifest{plaudGroupBrain("f")},
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
	if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == nil || slices.Contains(st.Seen, plaudNew) {
		t.Errorf("state = %+v, want the recording still pending", st)
	}
	if files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "*")); len(files) != 0 {
		t.Errorf("files written without a destination: %v", files)
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
	if e, _ := status["last_error"].(string); !strings.Contains(e, "no private chat") {
		t.Errorf("status last_error = %q", e)
	}
	time.Sleep(100 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Fatalf("outbox = %+v, want nothing", out)
	}

	if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.AllowedNumbers = []string{"15551234567"}; return nil }); err != nil {
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

// The card labels its "Automatic" choice with the brain Automatic picks,
// which is not the target while another brain is picked.
func TestPlaudStatusNamesTheAutomaticBrain(t *testing.T) {
	for _, c := range []struct {
		name, picked, target string
	}{
		{"automatic", "", "p"},
		{"picked", "g", "g"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := plaudBridge(t, "")
			tb.addFolderBrain(t, manifest.Manifest{ID: "g", Name: "Recordings"})
			if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain": c.picked}); w.Code != 200 {
				t.Fatalf("settings: %d %s", w.Code, w.Body.String())
			}
			var status map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
			if status["auto_brain"] != "p" || status["target_brain"] != c.target {
				t.Errorf("auto_brain = %v, target_brain = %v; want p (the DM brain) and %s", status["auto_brain"], status["target_brain"], c.target)
			}
		})
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
	if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == nil {
		t.Errorf("state = %+v, want the recording still pending", st)
	}
	if tb.plaudForwarded() {
		t.Error("a transcript was written without a brain to give it to")
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
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
			if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == nil {
				t.Errorf("state = %+v, want the recording still pending", st)
			}
			var status map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
			if e, _ := status["last_error"].(string); !strings.Contains(e, plaudNew) || !strings.Contains(e, "Failed to fetch") || strings.Contains(e, "- Fetching") {
				t.Errorf("status last_error = %q, want the recording's error without the spinner", e)
			}

			_ = os.Remove(filepath.Join(fp.dir, fixture))
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
			var after map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &after)
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
			first := time.Now().Add(-c.age)
			if err := tb.plaud.saveState(plaudWaiting(plaudSeededState("u_1", plaudOld1), plaudNew, first)); err != nil {
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

// plaudRecorder stands in for the bridge's submit: it notes every hand-off
// and keeps its done, or refuses it with refuse.
type plaudRecorder struct {
	mu     sync.Mutex
	handed []string
	done   map[string]func(error)
	refuse error
}

func (r *plaudRecorder) submit(br *Brain, jid, sender, text string, media []string, done func(error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refuse != nil {
		return r.refuse
	}
	id := strings.TrimSuffix(media[0][strings.LastIndex(media[0], " - ")+3:], ".txt")
	r.handed = append(r.handed, id)
	if r.done == nil {
		r.done = map[string]func(error){}
	}
	r.done[id] = done
	return nil
}

// end ends id's run with err.
func (r *plaudRecorder) end(id string, err error) {
	r.mu.Lock()
	done := r.done[id]
	r.mu.Unlock()
	done(err)
}

func (r *plaudRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.handed)
}

// A recording counts as seen only once its brain's run answered it. While
// the run is on its way it is not handed over again. A run you stopped is
// not repeated, and Send again on the dashboard hands it over once more.
// A failed run is repeated later, up to plaudRunTries runs, then the
// recording is given up and the chat hears about it. A brain that does not
// take it (its queue is full) leaves it waiting, with the reason on the
// card. After a restart, one whose run never ended is handed over again.
func TestPlaudRecordingIsSeenOnlyOnceItsRunEnds(t *testing.T) {
	stop := []error{errBrainInterrupted, errBrainDropped}
	for _, c := range []struct {
		name    string
		ends    []error // the runs' outcomes, one per check
		seen    bool
		status  string
		handed  int // hand-offs in all
		givenUp bool
	}{
		{"answered", []error{nil}, true, plaudRunDone, 1, false},
		{"stopped while it ran", stop[:1], true, plaudRunStopped, 1, false},
		{"dropped before it ran", stop[1:], true, plaudRunStopped, 1, false},
		{"failed once", []error{errors.New("claude exited: exit status 1"), nil}, true, plaudRunDone, 2, false},
		{"failed every time", []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}, true, plaudRunFailed, plaudRunTries, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			rec := &plaudRecorder{}
			tb.plaud.submit = rec.submit
			plaudSignIn(t, tb)
			if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
				t.Fatal(err)
			}
			fp.files(plaudNew, plaudOld1)
			fp.recording(plaudNew, true)
			ctx := context.Background()
			for i, end := range c.ends {
				if err := tb.plaud.update(func(st *plaudState) error {
					if it := st.Pending[plaudNew]; it != nil {
						it.NextTry = time.Time{}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := tb.plaud.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				if rec.count() != i+1 {
					t.Fatalf("check %d: handed over %d times, want %d", i+1, rec.count(), i+1)
				}
				// On its way: not seen, and not handed over again.
				if tb.plaudSeenAll(plaudNew) {
					t.Fatalf("check %d: seen before its run ended", i+1)
				}
				if err := tb.plaud.Poll(ctx); err != nil || rec.count() != i+1 {
					t.Fatalf("a check during the run: %v, handed over %d times, want %d", err, rec.count(), i+1)
				}
				rec.end(plaudNew, end)
			}
			if got := tb.plaudSeenAll(plaudNew); got != c.seen {
				t.Errorf("seen = %v, want %v", got, c.seen)
			}
			s := tb.plaud.Status()
			if s.LastForwarded == nil || s.LastForwarded.Status != c.status {
				t.Errorf("last forwarded = %+v, want status %s", s.LastForwarded, c.status)
			}
			if err := tb.plaud.Poll(ctx); err != nil || rec.count() != c.handed {
				t.Errorf("after the runs: %v, handed over %d times, want %d", err, rec.count(), c.handed)
			}
			if (len(s.GivenUp) == 1) != c.givenUp {
				t.Errorf("given up = %+v, want %v", s.GivenUp, c.givenUp)
			}
			if c.givenUp {
				waitFor(t, 5*time.Second, "the notice", func() bool { return len(tb.dev.Outbox()) == 1 })
				if m := tb.dev.Outbox()[0]; !strings.Contains(m.Text, "the brain's run failed 3 times (e3)") {
					t.Errorf("notice = %q", m.Text)
				}
			}
			if c.status == plaudRunStopped || c.givenUp {
				if w := tb.do("POST", "/api/plaud/resend", map[string]string{"id": plaudNew}); w.Code != 202 {
					t.Fatalf("send again: %d %s", w.Code, w.Body.String())
				}
				waitFor(t, 5*time.Second, "the recording to be handed over again", func() bool { return rec.count() == c.handed+1 })
			}
		})
	}
	t.Run("not taken", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		rec := &plaudRecorder{refuse: errQueueFull}
		tb.plaud.submit = rec.submit
		plaudSignIn(t, tb)
		if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.files(plaudNew, plaudOld1)
		fp.recording(plaudNew, true)
		err := tb.plaud.Poll(context.Background())
		if err == nil || !strings.Contains(err.Error(), "brain p did not take it: brain queue is full") || !strings.Contains(err.Error(), "will retry") {
			t.Errorf("poll = %v, want the full queue named", err)
		}
		st, _ := tb.plaud.loadState()
		if it := st.Pending[plaudNew]; it == nil || !it.Handed.IsZero() || len(st.Forwarded) != 0 {
			t.Errorf("state = %+v, want it still waiting", st)
		}
		rec.refuse = nil
		if err := tb.plaud.Poll(context.Background()); err != nil || rec.count() != 1 {
			t.Errorf("once the brain takes it: %v, handed over %d times", err, rec.count())
		}
	})
	t.Run("a restart meanwhile", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		rec := &plaudRecorder{}
		tb.plaud.submit = rec.submit
		plaudSignIn(t, tb)
		if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.files(plaudNew, plaudOld1)
		fp.recording(plaudNew, true)
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Fridolin stops, which stops the run.
		tb.plaud.Stop()
		rec.end(plaudNew, errBrainInterrupted)
		restarted := plaudAfterRestart(tb)
		restarted.submit = rec.submit
		if err := restarted.Poll(context.Background()); err != nil || rec.count() != 2 {
			t.Fatalf("after the restart: %v, handed over %d times, want 2", err, rec.count())
		}
		rec.end(plaudNew, nil)
		if !tb.plaudSeenAll(plaudNew) {
			t.Error("not seen after the second run answered it")
		}
	})
	t.Run("its card entry trimmed meanwhile", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		rec := &plaudRecorder{}
		tb.plaud.submit = rec.submit
		plaudSignIn(t, tb)
		if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.files(plaudNew, plaudOld1)
		fp.recording(plaudNew, true)
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Ten other recordings were handed over since.
		if err := tb.plaud.update(func(st *plaudState) error { st.Forwarded = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		rec.end(plaudNew, nil)
		if fw := tb.plaud.Status().LastForwarded; fw == nil || fw.ID != plaudNew || fw.Brain != "p" || fw.Status != plaudRunDone {
			t.Errorf("last forwarded = %+v, want it answered by brain p", fw)
		}
	})
}

// plaudSeenAll reports whether every recording in ids is seen and none
// waits.
func (tb *testBridge) plaudSeenAll(ids ...string) bool {
	st, _ := tb.plaud.loadState()
	for _, id := range ids {
		if st == nil || st.Pending[id] != nil || !slices.Contains(st.Seen, id) {
			return false
		}
	}
	return true
}

// With the real bridge: a hand-off is queued at once, so a full queue is
// an error for Plaud rather than a "try again" in the chat, and !stop in
// the chat its summaries go to stops it, waiting or running, as a stop
// rather than a failure.
func TestPlaudHandOffThroughTheBridge(t *testing.T) {
	tb, _ := plaudBridgeWith(t, `, "queue": {"max_pending": 1}`, "", plaudDMBrain)
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "3000")
	br := tb.brainMgr.Get("p")
	const dm = "15551234567@s.whatsapp.net"
	var mu sync.Mutex
	ended := map[string]error{}
	done := func(name string) func(error) {
		return func(err error) { mu.Lock(); ended[name] = err; mu.Unlock() }
	}
	if err := tb.submitHandOff(br, dm, "plaud", "first", nil, done("first")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
	if err := tb.submitHandOff(br, dm, "plaud", "second", nil, done("second")); err != nil {
		t.Fatal(err)
	}
	if err := tb.submitHandOff(br, dm, "plaud", "third", nil, done("third")); !errors.Is(err, errQueueFull) {
		t.Errorf("a hand-off to a full queue = %v, want errQueueFull", err)
	}
	tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: dm, Sender: "15551234567", Text: "!stop", Source: "whatsapp"}})
	waitFor(t, 10*time.Second, "both to end", func() bool { mu.Lock(); defer mu.Unlock(); return len(ended) == 2 })
	if !errors.Is(ended["first"], errBrainInterrupted) || !errors.Is(ended["second"], errBrainDropped) {
		t.Errorf("ended = %v, want the first stopped and the second dropped", ended)
	}
	for _, m := range tb.dev.Outbox() {
		if strings.Contains(m.Text, "queue is full") {
			t.Errorf("the chat was told to try again: %q", m.Text)
		}
	}
}

// A recording that is not ready is asked about at the next check, then
// ever less often, up to every 15 minutes, rather than once a minute for
// three days.
func TestPlaudBacksOffOnARecordingThatIsNotReady(t *testing.T) {
	for tries, want := range []time.Duration{0, 0, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		if got := plaudRetryDelay(tries); got != want {
			t.Errorf("plaudRetryDelay(%d) = %v, want %v", tries, got, want)
		}
	}
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudWaiting(plaudSeededState("u_1", plaudOld1), plaudNew, time.Now())); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, false)
	asked := func() int { return strings.Count(fp.calls(), " file "+plaudNew+"\n") }
	for i, want := range []int{1, 2, 2, 2} {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if asked() != want {
			t.Fatalf("after check %d Plaud was asked %d times, want %d", i+1, asked(), want)
		}
	}
	st, _ := tb.plaud.loadState()
	if it := st.Pending[plaudNew]; it.Tries != 2 || time.Until(it.NextTry) < time.Minute || it.Name != "Team sync" {
		t.Errorf("waiting = %+v, want 2 tries, the next in about 2 minutes, and its name", it)
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.Pending[plaudNew].NextTry = time.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	fp.recording(plaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tb.plaudForwarded() {
		t.Error("not handed over once due and ready")
	}
}

// A recording Fridolin gives up on is listed on the card with the reason,
// and the chat hears about it once, unless it was deleted in Plaud, which
// gives up at once and quietly.
func TestPlaudGivesUpVisibly(t *testing.T) {
	for _, c := range []struct {
		name    string
		age     time.Duration
		setup   func(fp *fakePlaudFixture)
		reason  string
		notices int
	}{
		{"never transcribed", plaudPendingMaxAge + time.Hour,
			func(fp *fakePlaudFixture) { fp.recording(plaudNew, false) }, "Plaud had no transcript for it after 3 days", 1},
		{"fetching keeps failing", plaudPendingMaxAge + time.Hour,
			func(fp *fakePlaudFixture) { fp.recording(plaudNew, true); fp.write("fail_transcript", "") }, "fetching it from Plaud failed for 3 days", 1},
		{"deleted in Plaud", time.Minute,
			func(*fakePlaudFixture) {}, "it was deleted in Plaud", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			if err := tb.plaud.saveState(plaudWaiting(plaudSeededState("u_1", plaudOld1), plaudNew, time.Now().Add(-c.age))); err != nil {
				t.Fatal(err)
			}
			fp.files(plaudNew, plaudOld1)
			c.setup(fp)
			for range 2 {
				_ = tb.plaud.Poll(context.Background())
			}
			s := tb.plaud.Status()
			if len(s.GivenUp) != 1 || s.GivenUp[0].ID != plaudNew || !strings.Contains(s.GivenUp[0].Reason, c.reason) {
				t.Fatalf("given up = %+v, want %s: %q", s.GivenUp, plaudNew, c.reason)
			}
			if *s.Pending != 0 {
				t.Errorf("%d still waiting", *s.Pending)
			}
			var got []string
			for _, m := range tb.dev.Outbox() {
				got = append(got, m.Text)
			}
			if len(got) != c.notices || c.notices > 0 && (!strings.Contains(got[0], "“Team sync” was not handed to brain p: "+c.reason) || tb.dev.Outbox()[0].JID != "15551234567@s.whatsapp.net") {
				t.Errorf("notices = %q, want %d about it in the DM", got, c.notices)
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
	if err := tb.plaud.saveState(plaudSeededState("", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Errorf("poll with nothing waiting = %v, want nil", err)
	}
	var status map[string]any
	_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
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
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &got)
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
	// Seen once the brain's run answered it.
	tb.waitPlaudSeen(t, plaudNew)
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, plaudDMBrain.ID, "plaud", "* - Team sync - "+plaudNew+".txt"))
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
	p.submit, p.notify = tb.submitHandOff, tb.wa.SendMessage
	return p
}

// The manager needs only the configuration, the brains and the two
// functions it is given: it hands each recording to submit and says
// nothing else to the bridge.
func TestPlaudManagerUsesOnlyWhatItIsGiven(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	type handed struct {
		brain, jid, sender, text string
		media                    []string
	}
	var got []handed
	p := NewPlaudManager(tb.cfgs, tb.brainMgr)
	p.submit = func(br *Brain, jid, sender, text string, media []string, done func(error)) error {
		got = append(got, handed{br.ID(), jid, sender, text, media})
		return nil
	}
	p.notify = func(context.Context, string, string) error { t.Error("notify called"); return nil }
	plaudSignIn(t, tb)
	if err := p.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
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
		t.Errorf("the bridge sent %+v, want nothing: only submit was to be used", out)
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

// waitPlaudSeen waits until recording id is seen and no longer waits, as
// once the brain's run answered it.
func (tb *testBridge) waitPlaudSeen(t *testing.T, id string) {
	t.Helper()
	waitFor(t, 10*time.Second, id+" to be seen", func() bool {
		st, _ := tb.plaud.loadState()
		return st != nil && st.Pending[id] == nil && slices.Contains(st.Seen, id)
	})
}

// plaudSeeded reports whether the first sync ran.
func (tb *testBridge) plaudSeeded() bool {
	st, _ := tb.plaud.loadState()
	return st.seededFor("")
}

// plaudSeededState is the state after the first sync for account ("" for
// one from before accounts were recorded), which saw the recordings seen.
func plaudSeededState(account string, seen ...string) *plaudState {
	st := newPlaudState()
	st.Seeded, st.Account, st.Seen = true, account, seen
	return st
}

// plaudWaiting adds recording id, first seen at, to the recordings st
// waits to hand over.
func plaudWaiting(st *plaudState, id string, at time.Time) *plaudState {
	st.Pending[id] = &plaudPending{FirstSeen: at}
	return st
}

// The expiry notice goes out once a day. Its time is saved in the state
// and survives a restart, also before the first sync, which the saved
// time does not count as.
func TestPlaudExpiredLoginNotifiesOncePerDay(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("seeded=%v", seeded), func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			if seeded {
				if err := tb.plaud.saveState(plaudSeededState("", plaudOld1)); err != nil {
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
			if tb.plaudSeeded() != seeded {
				t.Errorf("seeded = %v after the notice, want %v", !seeded, seeded)
			}
			if st, _ := tb.plaud.loadState(); st.AuthNotified.IsZero() {
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

// The status the dashboard polls never waits for the CLI. Who is signed in
// is asked once after the sign-in, and for a login from before sign-ins
// were recorded once in the background, not on every call.
func TestPlaudStatusDoesNotRunTheCLI(t *testing.T) {
	account := func(tb *testBridge) string {
		var s struct{ Account string }
		_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &s)
		return s.Account
	}
	t.Run("after signing in", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		if err := tb.plaud.Connect(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 10*time.Second, "the sign-in", func() bool { c := tb.plaud.Status().Connect; return c != nil && c.State == "done" })
		for range 5 {
			if got := account(tb); got != "you@example.com" {
				t.Fatalf("account = %q", got)
			}
		}
		if n := strings.Count(fp.calls(), " me\n"); n != 1 {
			t.Errorf("`plaud me` ran %d times, want once, after the sign-in", n)
		}
	})
	t.Run("an older login", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		plaudSignIn(t, tb)
		waitFor(t, 10*time.Second, "the account", func() bool { return account(tb) == "you@example.com" })
		for range 5 {
			account(tb)
		}
		if n := strings.Count(fp.calls(), " me\n"); n != 1 {
			t.Errorf("`plaud me` ran %d times, want once", n)
		}
	})
}

// plaudRecordLogin records Fridolin's sign-in in st, as a finished sign-in
// does.
func plaudRecordLogin(tb *testBridge, st *plaudState) *plaudState {
	st.Login = &plaudLogin{Dir: tb.plaud.cliHome(), Since: time.Now()}
	return st
}

// Plaud's CLI deletes a login that expired. Fridolin remembers it signed
// in, so the card says the login expired rather than that it never was
// connected, and the chat keeps getting its daily notice, without any CLI
// call, until you connect again or disconnect.
func TestPlaudExpiredLoginKeepsNotifying(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	fp.write("expired", "")
	ctx := context.Background()
	past := time.Now().Add(-time.Second)
	notices := func() int {
		n := 0
		for _, m := range tb.dev.Outbox() {
			if strings.Contains(m.Text, "Plaud: the login expired") {
				n++
			}
		}
		return n
	}
	tb.plaud.tick(ctx, past)
	if s := tb.plaud.Status(); !s.Expired || s.Connected || notices() != 1 {
		t.Fatalf("after the login lapsed: expired=%v connected=%v notices=%d, want expired and one notice", s.Expired, s.Connected, notices())
	}
	calls := fp.calls()
	tb.plaud.tick(ctx, past)
	if notices() != 1 {
		t.Errorf("notices = %d within a day, want 1", notices())
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.AuthNotified = time.Now().Add(-25 * time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	tb.plaud.tick(ctx, past)
	if notices() != 2 {
		t.Errorf("notices = %d a day later, want 2", notices())
	}
	if fp.calls() != calls {
		t.Errorf("the CLI ran while the login was gone:\n%s", strings.TrimPrefix(fp.calls(), calls))
	}

	if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.AuthNotified = time.Now().Add(-25 * time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	tb.plaud.tick(ctx, past)
	if s := tb.plaud.Status(); s.Expired || s.Connected || notices() != 2 {
		t.Errorf("after disconnecting: expired=%v connected=%v notices=%d, want neither and no more notices", s.Expired, s.Connected, notices())
	}
}

// Connecting again after a disconnect makes the login Fridolin's again:
// the card shows who signed in, and once the login lapses the chat hears
// so, as before the disconnect.
func TestPlaudConnectingAfterADisconnectCountsAgain(t *testing.T) {
	for _, access := range []bool{false, true} {
		t.Run(fmt.Sprintf("brain_access=%v", access), func(t *testing.T) {
			tb, fp := plaudBridge(t, fmt.Sprintf(`, "brain_access": %v`, access))
			plaudSignIn(t, tb)
			if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
				t.Fatal(err)
			}
			fp.files(plaudOld1)
			if err := tb.plaud.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the sign-in", func() bool { c := tb.plaud.Status().Connect; return c != nil && c.State != "running" })
			if c := tb.plaud.Status().Connect; c.State != "done" {
				t.Fatalf("sign-in %s: %s", c.State, c.Error)
			}
			if st, _ := tb.plaud.loadState(); st == nil || st.Disconnected {
				t.Errorf("state = %+v, want the disconnect forgotten", st)
			}
			if s := tb.plaud.Status(); !s.Connected || s.Account != "you@example.com" {
				t.Errorf("connected=%v account=%q, want connected as you@example.com", s.Connected, s.Account)
			}

			fp.write("expired", "")
			tb.plaud.tick(context.Background(), time.Now().Add(-time.Second))
			n := 0
			for _, m := range tb.dev.Outbox() {
				if strings.Contains(m.Text, "Plaud: the login expired") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d expiry notices, want 1", n)
			}
		})
	}
}

// A login Plaud refuses while its tokens.json stays (revoked on Plaud's
// side) also shows as expired, until a check works again.
func TestPlaudRefusedLoginShowsExpired(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	fp.write("revoked", "")
	ctx := context.Background()
	if err := tb.plaud.Poll(ctx); !errors.Is(err, errPlaudAuth) {
		t.Fatalf("poll = %v, want the login refused", err)
	}
	if s := tb.plaud.Status(); !s.Expired || !s.Connected {
		t.Errorf("expired=%v connected=%v, want both", s.Expired, s.Connected)
	}
	_ = os.Remove(filepath.Join(fp.dir, "revoked"))
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if s := tb.plaud.Status(); s.Expired {
		t.Error("still expired after a check that worked")
	}
}

// Connect again after Plaud refused a login whose tokens.json stays: with
// a login in place `plaud login` only says "Already logged in", so Connect
// signs the refused login out first, as Disconnect does, then signs in
// afresh, and the card shows the new login connected. A sign-in that keeps
// the refused login, because the sign-out could not remove it, fails, and
// so does one whose new login Plaud refuses as well; the card then shows
// the login expired, and the chat, told once already, is not told again.
// A first sign-in signs nothing out; one whose login Plaud refuses right
// away shows it expired too, and the check after it tells the chat once.
func TestPlaudConnectAgainAfterARefusedLogin(t *testing.T) {
	cases := []struct {
		name    string
		access  bool     // plaud.brain_access
		refused bool     // Plaud refuses the login when Connect starts
		keeps   bool     // `plaud logout` leaves the login (logout_keeps)
		again   bool     // Plaud refuses the sign-in's new login too (login_refused)
		cmds    []string // the CLI commands the sign-in runs, in order
		want    string   // how the sign-in ends
		says    string   // what its error says
	}{
		{"refused login", false, true, false, false, []string{"logout", "login", "me"}, "done", ""},
		{"refused login shared with brains", true, true, false, false, []string{"logout", "login", "me"}, "done", ""},
		// Fridolin's private copy goes even when the CLI keeps it.
		{"refused login the sign-out leaves", false, true, true, false, []string{"logout", "login", "me"}, "done", ""},
		{"refused login the sign-out leaves, shared with brains", true, true, true, false, []string{"logout", "login"}, "error", "could not be signed out"},
		{"refused again right after the sign-in", false, true, false, true, []string{"logout", "login", "me"}, "error", "Plaud refuses the login it left"},
		{"refused again right after the sign-in, shared with brains", true, true, false, true, []string{"logout", "login", "me"}, "error", "Plaud refuses the login it left"},
		{"first sign-in", false, false, false, false, []string{"login", "me"}, "done", ""},
		{"first sign-in Plaud refuses right away", false, false, false, true, []string{"login", "me"}, "error", "Plaud refuses the login it left"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, fmt.Sprintf(`, "brain_access": %v`, tc.access))
			ctx := context.Background()
			notices := func() int {
				n := 0
				for _, m := range tb.dev.Outbox() {
					if strings.Contains(m.Text, "Plaud: the login expired") {
						n++
					}
				}
				return n
			}
			if tc.refused {
				plaudRefuseLogin(t, tb, fp)
				if s := tb.plaud.Status(); !s.Expired || !s.Connected || notices() != 1 {
					t.Fatalf("before connecting: expired=%v connected=%v notices=%d, want expired, connected and one notice", s.Expired, s.Connected, notices())
				}
			}
			if tc.again {
				fp.write("login_refused", "")
				fp.write("revoked", "")
			}
			if tc.keeps {
				fp.write("logout_keeps", "")
			}
			before, told := fp.calls(), notices()
			st, _ := tb.plaud.loadState()
			var notified time.Time
			if st != nil {
				notified = st.AuthNotified
			}

			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			var job connectJob
			var refused bool
			// Not through Status: once the sign-in ended, that may ask who
			// is signed in, a command of its own.
			waitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				tb.plaud.mu.Lock()
				defer tb.plaud.mu.Unlock()
				job, refused = *tb.plaud.connect, tb.plaud.refused
				return job.State != "running"
			})
			if n := notices(); n != told {
				t.Errorf("%d expiry notices after connecting, want %d", n, told)
			}
			var cmds []string
			for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.calls(), before)), "\n") {
				if f := strings.Fields(line); len(f) > 2 {
					cmds = append(cmds, f[2])
				}
			}
			if !slices.Equal(cmds, tc.cmds) {
				t.Errorf("the sign-in ran %q, want %q", cmds, tc.cmds)
			}
			if job.State != tc.want {
				t.Fatalf("sign-in %s (%s), want %s", job.State, job.Error, tc.want)
			}
			st, _ = tb.plaud.loadState()
			s := tb.plaud.Status()
			if tc.want == "done" {
				if refused || !s.Connected || s.Expired || s.Account != "you@example.com" || !st.AuthNotified.IsZero() {
					t.Errorf("after the sign-in: refused=%v connected=%v expired=%v account=%q notified=%v; want connected as you@example.com, the notice armed again",
						refused, s.Connected, s.Expired, s.Account, st.AuthNotified)
				}
				if err := tb.plaud.Poll(ctx); err != nil {
					t.Errorf("the check after the sign-in: %v", err)
				}
			} else {
				if !strings.Contains(job.Error, tc.says) || !refused || !s.Expired || !st.AuthNotified.Equal(notified) {
					t.Errorf("after the sign-in: error %q, refused=%v expired=%v notified=%v (was %v); want it to say so, refused and expired, notified as before",
						job.Error, refused, s.Expired, st.AuthNotified, notified)
				}
				if err := tb.plaud.Poll(ctx); !errors.Is(err, errPlaudAuth) {
					t.Errorf("the check after the sign-in = %v, want the login still refused", err)
				}
			}
			// After a sign-in Plaud refused, the chat has heard of it once:
			// before the sign-in, or from the check after a first one.
			want := told
			if tc.want == "error" {
				want = max(told, 1)
			}
			if n := notices(); n != want {
				t.Errorf("%d expiry notices after the check, want %d", n, want)
			}
		})
	}
}

// plaudRefuseLogin signs in, as a login from an earlier check, and has
// Plaud refuse it while its tokens.json stays (revoked on Plaud's side): a
// check gets the refusal, and the chat its one expiry notice.
func plaudRefuseLogin(t *testing.T, tb *testBridge, fp *fakePlaudFixture) {
	t.Helper()
	plaudRevokeLogin(t, tb, fp)
	if err := tb.plaud.Poll(context.Background()); !errors.Is(err, errPlaudAuth) {
		t.Fatalf("poll = %v, want the login refused", err)
	}
}

// plaudRevokeLogin is plaudRefuseLogin up to the check that gets the
// refusal.
func plaudRevokeLogin(t *testing.T, tb *testBridge, fp *fakePlaudFixture) {
	t.Helper()
	plaudSignIn(t, tb)
	st := plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))
	// Who signed in was asked just now, so that the status does not ask
	// again.
	st.Login.Checked = time.Now()
	if err := tb.plaud.saveState(st); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudOld1)
	fp.write("revoked", "")
}

// Connect signs the refused login out in the CLI home it was refused in,
// taken when Connect starts: brain_access turned on before the sign-out
// runs must not have it sign out ~/.plaud instead, the login your
// terminal's plaud uses.
func TestPlaudConnectSignsOutTheRefusedLoginWhereItIs(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudRefuseLogin(t, tb, fp)
	terminal := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(terminal), 0o700); err != nil {
		t.Fatal(err)
	}
	const yours = `{"access_token":"terminal"}`
	if err := os.WriteFile(terminal, []byte(yours), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fp.calls()
	// The sign-out waits for the CLI, which this holds, until brain_access
	// is on.
	tb.plaud.cliMu.Lock()
	err := tb.plaud.Connect()
	if err == nil {
		_, err = tb.cfgs.Update("", func(c *config.Config) error { c.Plaud.BrainAccess = true; return nil })
	}
	tb.plaud.cliMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the sign-in to end", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return tb.plaud.connect.State != "running"
	})
	var logouts []string
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.calls(), before)), "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[2] == "logout" {
			logouts = append(logouts, strings.TrimPrefix(f[0], "HOME="))
		}
	}
	if want := tb.plaud.privateHome(); !slices.Equal(logouts, []string{want}) {
		t.Errorf("the sign-out ran with HOME %q, want %s once", logouts, want)
	}
	// The sign-in after it may adopt that login ("Already logged in"), as
	// any Connect after brain_access changed does, but not replace it.
	if got, _ := os.ReadFile(terminal); string(got) != yours {
		t.Errorf("the login in ~/.plaud is now %q, want yours, which the sign-out must not have removed", got)
	}
}

// The sign-out before the sign-in gets as long as Disconnect gives one,
// not the sign-in's ten minutes: a Plaud that hangs on it must not keep
// the card running for that long before `plaud login` prints its link.
// Fridolin's private copy of the login goes all the same, so the sign-in
// after it signs in afresh.
func TestPlaudConnectGivesTheSignOutItsOwnTime(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudRefuseLogin(t, tb, fp)
	old := plaudSignOutTimeout
	plaudSignOutTimeout = 300 * time.Millisecond
	t.Cleanup(func() { plaudSignOutTimeout = old })
	fp.write("hang_logout", "")
	before := fp.calls()
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	// Well below the 30 s the fake's logout hangs for.
	var job connectJob
	waitFor(t, 10*time.Second, "the sign-in to end", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		job = *tb.plaud.connect
		return job.State != "running"
	})
	var cmds []string
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.calls(), before)), "\n") {
		if f := strings.Fields(line); len(f) > 2 {
			cmds = append(cmds, f[2])
		}
	}
	if want := []string{"logout", "login", "me"}; !slices.Equal(cmds, want) || job.State != "done" {
		t.Errorf("the sign-in ran %q and ended %s (%s), want %q and done", cmds, job.State, job.Error, want)
	}
}

// A check that loses its login because you disconnected, or moved it with
// brain_access, while it ran tells the chat nothing: only a login that
// lapsed gets the expiry notice. Nor does it record the login again, which
// would show it as expired and bring the daily notice back.
func TestPlaudSigningOutDuringACheckIsNoExpiry(t *testing.T) {
	disconnect := func(tb *testBridge) error { return tb.plaud.Disconnect() }
	moveLogin := func(tb *testBridge) error {
		_, err := tb.cfgs.Update("", func(c *config.Config) error { c.Plaud.BrainAccess = true; return nil })
		return err
	}
	for _, c := range []struct {
		name   string
		seeded bool
		cmd    string // the command before (or after) which it happens
		after  bool
		act    func(tb *testBridge) error
		state  string
	}{
		{"disconnected before a recording is fetched", true, "file", false, disconnect, plaudSignedOut},
		{"brain_access turned on before a recording is fetched", true, "file", false, moveLogin, plaudMoved},
		{"disconnected while the account is looked up", true, "me", true, disconnect, plaudSignedOut},
		{"disconnected during the first sync", false, "files", true, disconnect, plaudSignedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			st := newPlaudState()
			if c.seeded {
				st = plaudSeededState("u_1", plaudOld1)
			}
			if err := tb.plaud.saveState(plaudRecordLogin(tb, st)); err != nil {
				t.Fatal(err)
			}
			fp.files(plaudNew, plaudOld1)
			fp.recording(plaudNew, true)
			run := tb.plaud.run
			var once sync.Once
			act := func() {
				once.Do(func() {
					if err := c.act(tb); err != nil {
						t.Error(err)
					}
				})
			}
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == c.cmd && !c.after {
					act()
				}
				out, err := run(ctx, args...)
				if args[0] == c.cmd && c.after {
					act()
				}
				return out, err
			}
			ctx := context.Background()
			if err := tb.plaud.Poll(ctx); err != nil && !errors.Is(err, errPlaudAuth) {
				t.Fatal(err)
			}
			tb.plaud.tick(ctx, time.Now().Add(-time.Second))
			if out := tb.dev.Outbox(); len(out) != 0 {
				t.Errorf("sent %+v, want nothing", out)
			}
			if got := tb.plaud.loginStatus(); got != c.state {
				t.Errorf("login %s, want %s", got, c.state)
			}
		})
	}
}

// A check ends with the command it is at once its login is no longer the
// one in use. brain_access may be saved while your terminal is signed in
// to ~/.plaud with another account: the commands after it would run with
// that login, which has none of Fridolin's recordings, also once you
// connected it: the check looked up the account of the login before. Or
// you disconnect with brain_access on, and the sign-out leaves the login
// in ~/.plaud behind. No recording is given up as
// deleted in Plaud or handed over after that, the state does not start
// over for the other account, and what was not handed over waits for the
// login you connect.
func TestPlaudCheckEndsWhenItsLoginGoes(t *testing.T) {
	const newer = "of_0000000000000000000004"
	// Your terminal's login, of another account, which has none of these
	// recordings, and brain_access turned on.
	moveLogin := func(tb *testBridge, fp *fakePlaudFixture) error {
		token := filepath.Join(tb.home, ".plaud", "tokens.json")
		if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(token, []byte("{}"), 0o600); err != nil {
			return err
		}
		fp.write("account", "u_2")
		for _, id := range []string{plaudNew, newer} {
			if err := os.Remove(filepath.Join(fp.dir, "rec_"+id+".json")); err != nil {
				return err
			}
		}
		_, err := tb.cfgs.Update("", func(c *config.Config) error { c.Plaud.BrainAccess = true; return nil })
		return err
	}
	// The same, and you connect that login, as a sign-in that finishes does.
	moveAndConnect := func(tb *testBridge, fp *fakePlaudFixture) error {
		if err := moveLogin(tb, fp); err != nil {
			return err
		}
		return tb.plaud.update(func(st *plaudState) error { plaudRecordLogin(tb, st); return nil })
	}
	disconnectKeeping := func(tb *testBridge, fp *fakePlaudFixture) error {
		fp.write("logout_keeps", "")
		if err := tb.plaud.Disconnect(); err == nil {
			return errors.New("disconnect did not say the login is still there")
		}
		return nil
	}
	for _, c := range []struct {
		name   string
		access bool   // brain_access before
		cmd    string // the command before (or after) which it happens
		after  bool
		act    func(tb *testBridge, fp *fakePlaudFixture) error
		// later is how many of the check's commands ran after it happened:
		// the one that was on its way then.
		later   int
		waiting []string
		state   string
	}{
		{"brain_access turned on while the account is looked up", false, "me", false, moveLogin, 1, nil, plaudMoved},
		{"brain_access turned on before a recording is fetched", false, "file", false, moveLogin, 1, []string{plaudNew, newer}, plaudMoved},
		{"brain_access turned on once a recording was handed over", false, "summary", true, moveLogin, 0, []string{newer}, plaudMoved},
		{"brain_access turned on and connected before a recording is fetched", false, "file", false, moveAndConnect, 1, []string{plaudNew, newer}, plaudConnected},
		{"disconnected once a recording was handed over", true, "summary", true, disconnectKeeping, 0, []string{newer}, plaudSignedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, fmt.Sprintf(`, "brain_access": %v`, c.access))
			plaudSignIn(t, tb)
			if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
				t.Fatal(err)
			}
			fp.files(newer, plaudNew, plaudOld1)
			fp.recording(plaudNew, true)
			fp.recording(newer, true)
			var once sync.Once
			var acted bool
			var later []string
			act := func() {
				once.Do(func() {
					if err := c.act(tb, fp); err != nil {
						t.Error(err)
					}
					acted = true
				})
			}
			run := tb.plaud.run
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == c.cmd && !c.after {
					act()
				}
				if acted {
					later = append(later, strings.Join(args, " "))
				}
				out, err := run(ctx, args...)
				if args[0] == c.cmd && c.after {
					act()
				}
				return out, err
			}
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			st, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.GivenUp) != 0 {
				t.Errorf("given up: %+v, want none", st.GivenUp)
			}
			if st.Account != "u_1" || !slices.Contains(st.Seen, plaudOld1) {
				t.Errorf("account %q, seen %v: want the state of u_1 kept", st.Account, st.Seen)
			}
			for _, id := range c.waiting {
				if item := st.Pending[id]; item == nil || item.Tries != 0 || !item.Handed.IsZero() {
					t.Errorf("recording %s: %+v, want it waiting, untried", id, item)
				}
			}
			if len(later) != c.later {
				t.Errorf("ran %q afterwards, want %d commands", later, c.later)
			}
			if got := tb.plaud.loginStatus(); got != c.state {
				t.Errorf("login %s, want %s", got, c.state)
			}
		})
	}
}

// Changes to the state never get lost to a poll that works through
// recordings meanwhile: a sign-in that finishes during a poll keeps its
// record. A login from before sign-ins were recorded gets one with the
// first check that works.
func TestPlaudStateChangesDuringAPollStay(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	signedIn := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	run := tb.plaud.run
	tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "file" {
			st, _ := tb.plaud.loadState()
			if st.Login == nil || st.Login.Dir != tb.plaud.cliHome() {
				t.Errorf("the old login was not recorded: %+v", st.Login)
			}
			if err := tb.plaud.update(func(st *plaudState) error {
				st.Login = &plaudLogin{Dir: tb.plaud.cliHome(), Since: signedIn}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}
		return run(ctx, args...)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	tb.waitPlaudSeen(t, plaudNew)
	st, _ := tb.plaud.loadState()
	if st.Login == nil || !st.Login.Since.Equal(signedIn) || !slices.Contains(st.Seen, plaudNew) {
		t.Errorf("state = %+v, login %+v: want the sign-in made during the poll, and the recording seen", st, st.Login)
	}
}

// However the first sync fails, it does not count as done, so the next
// check syncs again and still forwards none of the recordings that already
// exist.
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
			if tb.plaudSeeded() {
				t.Fatal("a failed first sync counted as done")
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
			if files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "*")); len(files) != 0 {
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
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "*.txt"))
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
		{"another account", *plaudSeededState("u_1", plaudOld1), "u_2", false, "signed in to another Plaud account"},
		{"the same account again", *plaudSeededState("u_1", plaudOld1), "u_1", true, ""},
		{"a state from before accounts were recorded", *plaudSeededState("", plaudOld1), "u_1", true, ""},
		{"a state only the expiry notice wrote", plaudState{AuthNotified: time.Unix(1, 0)}, "u_1", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLog(t)
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			c.state.Pending = map[string]*plaudPending{}
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
				tb.waitPlaudSeen(t, plaudNew)
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
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
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
		{"just after the account was looked up", plaudSeededState("u_1", plaudOld1), "me", ""},
		{"before the recordings are listed", plaudSeededState("u_1", plaudOld1), "", "files"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			if c.state != nil {
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
	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "* - "+id+".txt"))
	if len(files) != 1 {
		t.Fatalf("transcript files for %s = %v, want one", id, files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(files[0]) + "\n" + string(data)
}

// The message a recording comes with says what arrived, in a fixed first
// line, then the instruction: plaud.message, or a built-in one that points
// to the plaud skill and names no folder or date convention of its own.
// Dates go year first. The status gives the dashboard the built-in
// instruction and the interval it uses.
func TestPlaudMessageSaysWhatArrived(t *testing.T) {
	recorded := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC).Local()
	head := `[Plaud] New recording "Team sync" (` + recorded.Format("2006-01-02 15:04") + `, 5m00s). The attached .txt is Plaud's finished transcript`
	for _, c := range []struct {
		name, settings string
		instruction    string
		interval       int
	}{
		{"built in", "", defaultPlaudMessage, 60},
		{"plaud.message", `, "message": "File {name} under inbox/.", "interval_seconds": 120`, "File Team sync under inbox/.", 120},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, c.settings)
			var text, file string
			tb.plaud.submit = func(br *Brain, jid, sender, txt string, media []string, done func(error)) error {
				text, file = txt, filepath.Base(media[0])
				return nil
			}
			if err := plaudDeliver(t, tb, fp); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, "\n\n"+c.instruction) {
				t.Errorf("message = %q, want %q… then %q", text, head, c.instruction)
			}
			for _, personal := range []string{"transcripts/", "DD.MM.YYYY", "memories"} {
				if strings.Contains(text, personal) {
					t.Errorf("the message names %q: %q", personal, text)
				}
			}
			if want := recorded.Format("2006-01-02") + " - Team sync - " + plaudNew + ".txt"; file != want {
				t.Errorf("file = %s, want %s", file, want)
			}
			if s := tb.plaud.Status(); s.DefaultMessage != defaultPlaudMessage || s.IntervalSeconds != c.interval {
				t.Errorf("status default_message = %q, interval_seconds = %d", s.DefaultMessage, s.IntervalSeconds)
			}
		})
	}
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
			[]string{created.Format("2006-01-02") + " - Team sync - ", "Recorded: " + created.Format("2006-01-02 15:04") + "\n"},
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
			first := time.Now().Add(-c.age)
			if err := tb.plaud.saveState(plaudWaiting(plaudSeededState("u_1", plaudOld1), plaudNew, first)); err != nil {
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
				if st, _ := tb.plaud.loadState(); st.Pending[plaudNew] == nil {
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

// plaudIDs returns n recording ids, newest first, that start with prefix
// (one hex digit).
func plaudIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("of_%s%021x", prefix, n-i)
	}
	return ids
}

// A check pages back through Plaud's list until it reaches a recording it
// knows, so a gap (the bridge down, the login expired, sending off) misses
// nothing. A backlog arrives a few recordings per check, oldest first.
func TestPlaudPagesBackToTheLastKnownRecording(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	var handed []string
	tb.plaud.submit = func(br *Brain, jid, sender, text string, media []string, done func(error)) error {
		handed = append(handed, strings.TrimSuffix(media[0][strings.LastIndex(media[0], " - ")+3:], ".txt"))
		done(nil)
		return nil
	}
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld2, plaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudIDs("a", 120)
	fp.files(append(slices.Clone(backlog), plaudOld1, plaudOld2)...)
	for _, id := range backlog {
		fp.recording(id, true)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := fp.calls(); !strings.Contains(calls, "files --page 2 --page-size 100\n") || strings.Contains(calls, "--page 3") {
		t.Errorf("listed:\n%s\nwant pages 1 and 2 of 100", calls)
	}
	oldest := slices.Clone(backlog)
	slices.Reverse(oldest)
	if !slices.Equal(handed, oldest[:plaudHandedMax]) {
		t.Errorf("handed over %v, want the oldest %d: %v", handed, plaudHandedMax, oldest[:plaudHandedMax])
	}
	if st, _ := tb.plaud.loadState(); len(st.Pending) != len(backlog)-plaudHandedMax {
		t.Errorf("%d waiting, want %d", len(st.Pending), len(backlog)-plaudHandedMax)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(handed, oldest[:2*plaudHandedMax]) {
		t.Errorf("after two checks handed over %v, want the oldest %d", handed, 2*plaudHandedMax)
	}
}

// Only so many recordings are with brains at once, those of earlier checks
// included: a backlog goes on, oldest first, as their runs end. The limit
// leaves half of a small queue for your own messages.
func TestPlaudBacklogTricklesInAsRunsEnd(t *testing.T) {
	for _, c := range []struct{ maxPending, want int }{{10, plaudHandedMax}, {100, plaudHandedMax}, {6, 3}, {3, 1}, {1, 1}} {
		if got := plaudHandedLimit(&config.Config{Queue: config.QueueConfig{MaxPending: c.maxPending}}); got != c.want {
			t.Errorf("limit with queue.max_pending %d = %d, want %d", c.maxPending, got, c.want)
		}
	}
	tb, fp := plaudBridge(t, "")
	rec := &plaudRecorder{}
	tb.plaud.submit = rec.submit
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudIDs("d", 12)
	fp.files(append(slices.Clone(backlog), plaudOld1)...)
	for _, id := range backlog {
		fp.recording(id, true)
	}
	oldest := slices.Clone(backlog)
	slices.Reverse(oldest)
	ctx := context.Background()
	ended := 0
	for _, step := range []struct {
		end  int // runs that end before the check, oldest first
		want int // hand-offs in all after it
	}{{0, plaudHandedMax}, {0, plaudHandedMax}, {2, plaudHandedMax + 2}, {0, plaudHandedMax + 2}, {5, 2*plaudHandedMax + 2}} {
		for _, id := range oldest[ended : ended+step.end] {
			rec.end(id, nil)
		}
		ended += step.end
		if err := tb.plaud.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		rec.mu.Lock()
		handed := slices.Clone(rec.handed)
		rec.mu.Unlock()
		if !slices.Equal(handed, oldest[:step.want]) {
			t.Fatalf("after %d more runs ended: handed over %d, want the oldest %d", step.end, len(handed), step.want)
		}
	}
}

// With the real bridge and a slow brain, a backlog never fills the brain's
// queue, however many checks come while it works: none of them finds the
// queue full, and your own message to that brain still gets in.
func TestPlaudBacklogLeavesRoomInTheBrainsQueue(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "60000") // the cleanup stops the runs
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudIDs("c", 15)
	fp.files(append(slices.Clone(backlog), plaudOld1)...)
	for _, id := range backlog {
		fp.recording(id, true)
	}
	limit := plaudHandedLimit(tb.cfgs.Get())
	for i := range 4 {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
		if s := tb.plaud.Status(); *s.Handed != limit || *s.Pending != len(backlog)-limit {
			t.Fatalf("check %d: %d with the brain and %d waiting, want %d and %d", i+1, *s.Handed, *s.Pending, limit, len(backlog)-limit)
		}
	}
	if _, _, _, err := tb.brainMgr.Submit(Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}}); err != nil {
		t.Errorf("your own message to the brain = %v, want it taken", err)
	}
}

// The first sync remembers every page of recordings, oldest first, so
// that trimming the list drops the oldest.
func TestPlaudFirstSyncReadsEveryPage(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	existing := plaudIDs("b", 150)
	fp.files(existing...)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := tb.plaud.loadState()
	want := slices.Clone(existing)
	slices.Reverse(want)
	if !slices.Equal(st.Seen, want) || len(st.Pending) != 0 {
		t.Errorf("seen %d, pending %d; want all %d seen, oldest first", len(st.Seen), len(st.Pending), len(existing))
	}
}

// Only the id column of `plaud files` holds ids: a recording named like
// one does not add a recording.
func TestPlaudListedIDsComeFromTheIDColumn(t *testing.T) {
	golden := loadPlaudGolden(t)["files"].Stdout
	if got := plaudListedIDs(golden); !slices.Equal(got, []string{plaudGoldTeam, plaudGoldDoctor, plaudGoldOld}) {
		t.Errorf("ids from the recorded listing = %v", got)
	}
	dir := t.TempDir()
	r := fakePlaudRecording{ID: plaudNew, Name: "of_00000000000000000000ff copy", CreatedAt: "2026-10-16T18:00:00", Duration: 60000}
	writeFakePlaudRecordings(t, dir, []string{plaudNew}, []fakePlaudRecording{r})
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".plaud"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".plaud", "tokens.json"), []byte("{}"), 0o600)
	cmd := exec.Command(os.Args[0], "files")
	cmd.Env = []string{"FRIDOLIN_FAKE_PLAUD=1", "FRIDOLIN_FAKE_PLAUD_DIR=" + dir, "HOME=" + home, "GORACE=atexit_sleep_ms=0"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := plaudListedIDs(string(out)); !slices.Equal(got, []string{plaudNew}) {
		t.Errorf("ids = %v from:\n%s", got, out)
	}
}

// A state.json from before the version field still reads: its Unix-second
// times become times, and it counts as seeded when it named the account or
// remembered a recording, as it did then. A saved state is version 2.
func TestPlaudReadsTheOldState(t *testing.T) {
	for _, c := range []struct {
		name, file string
		want       plaudState
	}{
		{"seeded", `{"account":"u_1","seen":["of_a"],"pending":{"of_b":1760000000},"auth_notified":1760000100}`,
			plaudState{Version: 1, Seeded: true, Account: "u_1", Seen: []string{"of_a"},
				Pending: map[string]*plaudPending{"of_b": {FirstSeen: time.Unix(1760000000, 0)}}, AuthNotified: time.Unix(1760000100, 0)}},
		{"from before accounts were recorded", `{"seen":["of_a"],"pending":{},"auth_notified":0}`,
			plaudState{Version: 1, Seeded: true, Seen: []string{"of_a"}, Pending: map[string]*plaudPending{}}},
		{"only an expiry notice", `{"seen":null,"pending":{},"auth_notified":5}`,
			plaudState{Version: 1, Pending: map[string]*plaudPending{}, AuthNotified: time.Unix(5, 0)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := plaudBridge(t, "")
			if err := os.MkdirAll(tb.plaud.dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tb.plaud.dir(), "state.json"), []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(st)
			want, _ := json.Marshal(c.want)
			if st.Version != c.want.Version || string(got) != string(want) {
				t.Errorf("read %s\nwant %s", got, want)
			}
			if err := tb.plaud.saveState(st); err != nil {
				t.Fatal(err)
			}
			saved, _ := os.ReadFile(filepath.Join(tb.plaud.dir(), "state.json"))
			if !strings.Contains(string(saved), `"version": 2`) || strings.Contains(string(saved), "1760000000") {
				t.Errorf("saved:\n%s", saved)
			}
			again, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if again.seededFor("") != st.Seeded || !again.AuthNotified.Equal(st.AuthNotified) {
				t.Errorf("read back %+v, want %+v", again, st)
			}
		})
	}
}

// Recordings waiting to be handed over go in the order they showed up,
// whatever their ids, and the last one handed over is still on the card
// after a restart.
func TestPlaudPendingGoesOldestFirst(t *testing.T) {
	const older, newer = "of_00000000000000000000bb", "of_00000000000000000000ba"
	tb, fp := plaudBridge(t, "")
	var order []string
	tb.plaud.submit = func(br *Brain, jid, sender, text string, media []string, done func(error)) error {
		order = append(order, media[0][len(media[0])-len(older)-4:len(media[0])-4])
		done(nil)
		return nil
	}
	plaudSignIn(t, tb)
	st := plaudWaiting(plaudSeededState("u_1", plaudOld1), older, time.Now().Add(-2*time.Hour))
	if err := tb.plaud.saveState(plaudWaiting(st, newer, time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	fp.files(newer, older, plaudOld1)
	fp.recording(older, true)
	fp.recording(newer, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != older+","+newer {
		t.Errorf("handed over %v, want %s first", order, older)
	}
	if fw := plaudAfterRestart(tb).Status().LastForwarded; fw == nil || fw.Name != "Team sync" || fw.Brain != "p" {
		t.Errorf("last forwarded after a restart = %+v", fw)
	}
}

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
	if err := tb.plaud.saveState(plaudWaiting(plaudSeededState("", plaudOld1), plaudNew, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "second brain reply", func() bool { return len(tb.dev.Outbox()) == 2 })

	files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "*"))
	if len(files) != 1 || !strings.HasSuffix(files[0], " - "+plaudNew+".txt") {
		t.Fatalf("transcript files = %v, want one named after the recording id", files)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "Plaud recording: Weekly sync") {
		t.Errorf("the file was not rewritten:\n%s", data)
	}
}

// A brain may write to its own media inbox, so it can put a symlink where a
// recording's transcript will go. The transcript replaces the link instead
// of being written through it to the file it points to.
func TestPlaudTranscriptReplacesASymlink(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tb.cfgs.Get().MediaDir, plaudDMBrain.ID, "plaud")
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

// A brain can also swap the plaud folder in its inbox for a symlink, to
// another brain's inbox or anywhere else the bridge may write. The bridge
// writes the transcript only through the inbox, never out of it: the
// recording waits for another try, and nothing lands where the link
// points.
func TestPlaudTranscriptStaysInTheInbox(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	media := tb.cfgs.Get().MediaDir
	elsewhere := t.TempDir()
	for name, target := range map[string]string{"out of the media folder": elsewhere, "into another brain's inbox": filepath.Join(media, "other")} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			inbox := filepath.Join(media, plaudDMBrain.ID)
			if err := os.MkdirAll(inbox, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(inbox, "plaud")
			_ = os.RemoveAll(link)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := plaudDeliver(t, tb, fp); err == nil {
				t.Error("the recording was delivered through the link")
			}
			if names := dirNames(t, target); len(names) != 0 {
				t.Errorf("written through the link: %q", names)
			}
		})
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
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
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
		return !tb.plaud.busy && tb.plaudSeeded()
	})
}

// A sign-in that waits for the browser ends when Fridolin stops, and Stop
// waits for it: it would write to the data folder after Fridolin stopped.
func TestPlaudStopEndsASignIn(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	fp.write("login_wait", "")
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the sign-in to start", func() bool { return strings.Contains(fp.calls(), " login") })
	tb.plaud.Stop()
	if c := tb.plaud.Status().Connect; c == nil || c.State != "error" {
		t.Errorf("sign-in after Stop = %+v, want it ended", c)
	}
}

// Status polling and a new sign-in must not add background work once
// Stop has finished waiting. In particular, a status read after shutdown
// must not start an account lookup that writes into the old data folder.
func TestPlaudStopRefusesNewBackgroundWork(t *testing.T) {
	t.Run("phone app build", func(t *testing.T) {
		tb, _ := plaudBridge(t, "")
		tb.plaud.Stop()
		if err := tb.plaud.BuildApp(); !errors.Is(err, errPlaudStopping) {
			t.Errorf("BuildApp after Stop = %v, want errPlaudStopping", err)
		}
	})
	t.Run("account lookup", func(t *testing.T) {
		tb, _ := plaudBridge(t, "")
		tb.plaud.Stop()
		release := make(chan struct{})
		tb.plaud.run = func(context.Context, ...string) (string, error) {
			<-release
			return "", context.Canceled
		}
		tb.plaud.refreshAccount(time.Time{})
		tb.plaud.mu.Lock()
		busy := tb.plaud.accountBusy
		tb.plaud.mu.Unlock()
		close(release)
		tb.plaud.bg.Wait()
		if busy {
			t.Error("account lookup started after Stop returned")
		}
	})
	t.Run("sign-in", func(t *testing.T) {
		tb, _ := plaudBridge(t, "")
		tb.plaud.Stop()
		if err := tb.plaud.Connect(); !errors.Is(err, errPlaudStopping) {
			t.Errorf("Connect after Stop = %v, want errPlaudStopping", err)
		}
		tb.plaud.Stop()
	})
}

// A run that ends while Fridolin stops has its end recorded before Stop
// returns: the brain's worker records it after the reply went out, and
// would otherwise write to the data folder after Fridolin stopped.
func TestPlaudStopWaitsForARunsEnd(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	rec := &plaudRecorder{}
	tb.plaud.submit = rec.submit
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil || rec.count() != 1 {
		t.Fatalf("poll = %v, handed over %d times", err, rec.count())
	}
	// The run's end waits to write the state while Stop is called.
	tb.plaud.stMu.Lock()
	go rec.end(plaudNew, nil)
	waitFor(t, 5*time.Second, "the run's end to begin", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return !tb.plaud.inFlight[plaudNew]
	})
	stopped := make(chan struct{})
	go func() { tb.plaud.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("Stop returned before the run's end was recorded")
	case <-time.After(200 * time.Millisecond):
	}
	tb.plaud.stMu.Unlock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if !tb.plaudSeenAll(plaudNew) {
		t.Error("the run's end was not recorded")
	}
}

// A check that Check now started ends when Fridolin stops, like the
// poller's own checks, instead of running on for up to ten minutes, and
// Stop waits for its end: what a check writes to the data folder must not
// come after it. That holds in a command of the CLI and while it sends the
// expiry notice of a login Plaud refused. Once Fridolin is stopping, Check
// now starts none.
func TestPlaudStopEndsCheckNow(t *testing.T) {
	cases := []struct {
		name string
		// hold has the check wait in held until it is cancelled.
		hold    func(t *testing.T, tb *testBridge, fp *fakePlaudFixture, held func(context.Context) error)
		lastErr error // what the check then ends with
	}{
		{"in a command", func(t *testing.T, tb *testBridge, fp *fakePlaudFixture, held func(context.Context) error) {
			plaudSignIn(t, tb)
			fp.files(plaudOld1)
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) { return "", held(ctx) }
		}, context.Canceled},
		{"sending the expiry notice", func(t *testing.T, tb *testBridge, fp *fakePlaudFixture, held func(context.Context) error) {
			plaudRevokeLogin(t, tb, fp)
			tb.plaud.notify = func(ctx context.Context, _, _ string) error { return held(ctx) }
		}, errPlaudAuth},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			asked, done := make(chan struct{}), make(chan struct{})
			// Released at the end, so that a Stop that does not end the
			// check fails the test instead of hanging it.
			t.Cleanup(func() { close(done) })
			var once sync.Once
			c.hold(t, tb, fp, func(ctx context.Context) error {
				once.Do(func() { close(asked) })
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-done:
					return errors.New("held past the test")
				}
			})
			tb.plaud.Start(context.Background())
			if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 202 {
				t.Fatalf("check now = %d %s, want 202", w.Code, w.Body.String())
			}
			select {
			case <-asked:
			case <-time.After(10 * time.Second):
				t.Fatal("the check did not get there")
			}
			stopped := make(chan struct{})
			go func() { tb.plaud.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Fatal("Stop did not end the check")
			}
			tb.plaud.mu.Lock()
			busy, lastErr := tb.plaud.busy, tb.plaud.lastErr
			tb.plaud.mu.Unlock()
			if busy || !strings.Contains(lastErr, c.lastErr.Error()) {
				t.Errorf("after Stop: checking %v, last error %q; want the check over, with %q", busy, lastErr, c.lastErr)
			}

			if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 503 {
				t.Errorf("check now while stopping = %d %s, want 503", w.Code, w.Body.String())
			}
		})
	}
}

// A fetch the bridge was killed in leaves its folder, with the recording's
// text, in the data folder. The next start removes it, and nothing else.
func TestPlaudStartRemovesUnfinishedFetches(t *testing.T) {
	tb, _ := plaudBridge(t, "")
	left := filepath.Join(tb.plaud.dir(), plaudFetchPrefix+"123")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "transcript.txt"), []byte("[00:00 - 00:04] Speaker 1: Let's ship on Friday."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	tb.plaud.Start(context.Background())
	tb.plaud.Stop()
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unfinished fetch is still there: %v", err)
	}
	if st, err := tb.plaud.loadState(); err != nil || !st.Seeded {
		t.Errorf("the state = %+v, %v; want it kept", st, err)
	}
}

// Turning plaud.brain_access on or off moves where the CLI keeps its
// login. Fridolin does not take over the login it finds there, which may
// be your terminal's, of another account: it waits, without running the
// CLI, until you connect again, and then starts over for that account if
// it is another one.
func TestPlaudBrainAccessNeedsConnectingAgain(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
		t.Fatal(err)
	}
	// Your terminal is signed in to another account.
	shared := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp.write("account", "u_2")
	fp.files(plaudNew, plaudOld1)
	fp.recording(plaudNew, true)

	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain_access": true}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	if s := tb.plaud.Status(); s.Connected || !s.LoginMoved || s.Expired {
		t.Errorf("after turning brain_access on: connected=%v moved=%v expired=%v, want only moved", s.Connected, s.LoginMoved, s.Expired)
	}
	tb.plaud.tick(ctx, time.Now().Add(-time.Second))
	if calls := fp.calls(); calls != "" {
		t.Fatalf("the CLI ran with a login Fridolin did not make:\n%s", calls)
	}

	// Connect again: the CLI is already signed in there, and that is now
	// Fridolin's login. Its account is another one, so nothing old is sent.
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the sign-in", func() bool { return tb.plaud.connected() })
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if tb.plaudForwarded() {
		t.Error("the other account's recording was sent")
	}
	if st, _ := tb.plaud.loadState(); st.Account != "u_2" || st.Login == nil || st.Login.Dir != tb.home {
		t.Errorf("state = %+v, login %+v: want it started over for u_2 with the login in %s", st, st.Login, tb.home)
	}

	// And back: the private login that is still there needs connecting too.
	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain_access": false}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if s := tb.plaud.Status(); s.Connected || !s.LoginMoved {
		t.Errorf("after turning brain_access off: connected=%v moved=%v, want moved", s.Connected, s.LoginMoved)
	}
}

// A look-up of the account that is still running when brain_access is
// saved does not take over the login in the new home, which may be your
// terminal's: the login stays moved until you connect again.
func TestPlaudAccountLookUpLeavesAMovedLoginAlone(t *testing.T) {
	tb, _ := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
		t.Fatal(err)
	}
	private := tb.plaud.cliHome()
	shared := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := tb.plaud.run
	tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
		out, err := run(ctx, args...)
		if args[0] == "me" {
			if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Plaud.BrainAccess = true; return nil }); err != nil {
				t.Error(err)
			}
		}
		return out, err
	}
	if _, err := tb.plaud.me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tb.plaud.loginStatus(); got != plaudMoved {
		t.Errorf("login %s, want moved", got)
	}
	if st, _ := tb.plaud.loadState(); st.Login == nil || st.Login.Dir != private || st.Login.Account != "" {
		t.Errorf("login = %+v, want Fridolin's sign-in in %s, untouched", st.Login, private)
	}
}

// Disconnect fails, rather than claim it signed out, when the login is
// still there afterwards (brains and your terminal would go on using it).
// Fridolin itself no longer does.
func TestPlaudDisconnectReportsALoginThatStays(t *testing.T) {
	tb, fp := plaudBridge(t, `, "brain_access": true`)
	plaudSignIn(t, tb)
	fp.write("logout_keeps", "")
	w := tb.do("POST", "/api/plaud/disconnect", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "still there") {
		t.Errorf("disconnect = %d %s, want 502 saying the login is still there", w.Code, w.Body.String())
	}
	if s := tb.plaud.Status(); s.Connected || s.Expired {
		t.Errorf("after disconnecting: connected=%v expired=%v, want neither: Fridolin signed out of it", s.Connected, s.Expired)
	}
	_ = os.Remove(filepath.Join(fp.dir, "logout_keeps"))
	if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 || tb.plaud.connected() {
		t.Errorf("disconnect = %d %s, connected = %v", w.Code, w.Body.String(), tb.plaud.connected())
	}
	// Your terminal signs in to ~/.plaud again: not Fridolin's login until
	// you connect, nor Fridolin's to sign out.
	plaudSignIn(t, tb)
	if tb.plaud.connected() {
		t.Error("a login made after disconnecting counts without connecting")
	}
	if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 || !tb.plaud.hasLogin() {
		t.Errorf("disconnect again = %d %s, terminal's login there = %v; want it left alone", w.Code, w.Body.String(), tb.plaud.hasLogin())
	}
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the sign-in", func() bool { return tb.plaud.connected() })
}

// Disconnecting while the card asks to connect again, after brain_access
// moved the login, signs out only Fridolin's private login. ~/.plaud may
// hold your terminal's own login, which Fridolin never made, or the one
// Fridolin made there, which the dashboard said would stay for your
// terminal when you turned brain_access off: it is left alone, and does
// not count as Fridolin's afterwards.
func TestPlaudDisconnectAfterTheLoginMoved(t *testing.T) {
	for _, access := range []bool{true, false} {
		t.Run(fmt.Sprintf("brain_access turned %v", map[bool]string{true: "on", false: "off"}[access]), func(t *testing.T) {
			tb, fp := plaudBridge(t, fmt.Sprintf(`, "brain_access": %v`, !access))
			if err := tb.plaud.saveState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
				t.Fatal(err)
			}
			private := filepath.Join(tb.plaud.privateHome(), ".plaud", "tokens.json")
			shared := filepath.Join(tb.home, ".plaud", "tokens.json")
			for _, token := range []string{private, shared} {
				if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(token, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain_access": access}); w.Code != 200 {
				t.Fatalf("settings: %d %s", w.Code, w.Body.String())
			}
			if s := tb.plaud.Status(); !s.LoginMoved {
				t.Fatalf("login moved = %v, want true", s.LoginMoved)
			}
			if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 {
				t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(private); err == nil {
				t.Error("the private login is still there")
			}
			if _, err := os.Stat(shared); err != nil {
				t.Errorf("the login in ~/.plaud was signed out: %v", err)
			}
			if calls := fp.calls(); strings.Count(calls, " logout") != 1 || !strings.Contains(calls, "HOME="+tb.plaud.privateHome()+" ") {
				t.Errorf("calls:\n%s\nwant one logout, in the private home", calls)
			}
			if s := tb.plaud.Status(); s.Connected || s.Expired || s.LoginMoved {
				t.Errorf("after disconnecting: connected=%v expired=%v moved=%v, want signed out", s.Connected, s.Expired, s.LoginMoved)
			}
		})
	}
}

// Brains' runs get Plaud's telemetry opt-out too, whether or not brains may
// use the CLI: a brain that runs it must not report to Plaud's analytics.
func TestBrainRunsKeepPlaudTelemetryOff(t *testing.T) {
	tb, _ := plaudBridge(t, "")
	envLog := filepath.Join(t.TempDir(), "brain.env")
	t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)
	tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
	waitFor(t, 10*time.Second, "the run", func() bool { return len(tb.dev.Outbox()) > 0 })
	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range plaudOptOut {
		if !slices.Contains(strings.Split(string(logged), "\n"), want) {
			t.Errorf("the brain's run lacks %s", want)
		}
	}
}

// With plaud.brain_access brains find the Plaud CLI on their PATH: its
// folder comes last when it holds no node, as the test binary's does (see
// TestBrainPlaudPATH); without it their PATH is the bridge's.
func TestBrainsFindThePlaudCLIWhenTheyMayUseIt(t *testing.T) {
	for _, access := range []bool{true, false} {
		t.Run(fmt.Sprintf("brain_access=%v", access), func(t *testing.T) {
			tb, _ := plaudBridge(t, fmt.Sprintf(`, "brain_access": %v`, access))
			envLog := filepath.Join(t.TempDir(), "brain.env")
			t.Setenv("FRIDOLIN_FAKE_ENV_LOG", envLog)
			tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
			waitFor(t, 10*time.Second, "the run", func() bool { return len(tb.dev.Outbox()) > 0 })
			logged, _ := os.ReadFile(envLog)
			var path string
			for _, kv := range strings.Split(string(logged), "\n") {
				if v, ok := strings.CutPrefix(kv, "PATH="); ok {
					path = v
				}
			}
			cliDir := filepath.Dir(os.Args[0])
			if dirs := filepath.SplitList(path); (dirs[len(dirs)-1] == cliDir) != access {
				t.Errorf("PATH = %s, want %s last: %v", path, cliDir, access)
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
	// Brains read the login, and save it when the CLI refreshes it, but
	// cannot remove it or put anything else in its place.
	denyRead := func(home string) string { return `(deny file-read* (subpath "` + filepath.Join(home, ".plaud") + `"))` }
	profile := mustProfile(t, tb.brainMgr.profiler, "p")
	if strings.Contains(profile, denyRead(tb.home)) {
		t.Error("with brain_access brains must be able to read ~/.plaud")
	}
	tokens := filepath.Join(tb.home, ".plaud", "tokens.json")
	for _, op := range []string{"file-write-data", "file-write-mode"} {
		if !strings.Contains(profile, `(allow `+op+` (literal "`+tokens+`"))`) {
			t.Errorf("with brain_access brains must be able to save a refreshed login (%s)", op)
		}
	}
	if strings.Contains(profile, `(allow file-write* (literal "`+tokens+`"))`) {
		t.Error("with brain_access brains may rewrite the Plaud login, not remove or replace it")
	}
	tb2, _ := plaudBridge(t, "")
	profile = mustProfile(t, tb2.brainMgr.profiler, "p")
	if !strings.Contains(profile, denyRead(tb2.home)) {
		t.Error("without brain_access the sandbox must deny ~/.plaud")
	}
	if strings.Contains(profile, "tokens.json") {
		t.Error("without brain_access brains must not write the Plaud login")
	}
}

// A shared login that is a symlink, which an older version's sandbox let a
// brain put in its place, is not used: the CLI, which runs outside the
// sandbox, would write your next sign-in through it (into ~/.zshrc, say),
// and chmod what it points to. A sign-out still runs, and removes only the
// link.
func TestPlaudRefusesALoginThatIsASymlink(t *testing.T) {
	tb, fp := plaudBridge(t, `, "brain_access": true`)
	fp.files()
	const rcText = "export PATH=/usr/bin\n"
	rc := writeTestFile(t, filepath.Join(tb.home, ".zshrc"), rcText)
	if err := os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rc, tb.plaud.tokenPath()); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"files", "login"} {
		if _, err := tb.plaud.runCLIOut(context.Background(), nil, cmd); err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Errorf("plaud %s: %v, want it refused", cmd, err)
		}
	}
	if calls := fp.calls(); calls != "" {
		t.Errorf("the CLI ran:\n%s", calls)
	}
	if _, err := tb.plaud.runCLIOut(context.Background(), nil, "logout"); err != nil {
		t.Errorf("plaud logout: %v", err)
	}
	if _, err := os.Lstat(tb.plaud.tokenPath()); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	if data, _ := os.ReadFile(rc); string(data) != rcText {
		t.Errorf("~/.zshrc = %q, want it untouched", data)
	}
}

// TestPlaudRefusesALoginElsewhere: only a symlinked tokens.json was
// refused, but a brain under an older sandbox could also have swapped
// ~/.plaud itself for a symlink, or left a hard link to the login, which
// the bridge's CLI, outside the sandbox, then wrote through. Both are
// refused now as the symlink is; a login of its own is not.
func TestPlaudRefusesALoginElsewhere(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home, elsewhere string)
		says  string // "" for a login that is fine
	}{
		{"a login of its own", func(t *testing.T, home, _ string) {
			writeTestFile(t, filepath.Join(home, ".plaud", "tokens.json"), "{}")
		}, ""},
		{"no login yet", func(t *testing.T, home, _ string) {}, ""},
		{"a symlinked folder", func(t *testing.T, home, elsewhere string) {
			writeTestFile(t, filepath.Join(elsewhere, "tokens.json"), "{}")
			if err := os.Symlink(elsewhere, filepath.Join(home, ".plaud")); err != nil {
				t.Fatal(err)
			}
		}, ".plaud is a symlink"},
		{"a file where the folder goes", func(t *testing.T, home, _ string) {
			writeTestFile(t, filepath.Join(home, ".plaud"), "x")
		}, ".plaud is not a folder"},
		{"a hard link", func(t *testing.T, home, elsewhere string) {
			other := writeTestFile(t, filepath.Join(elsewhere, "zshrc"), "x")
			if err := os.MkdirAll(filepath.Join(home, ".plaud"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(other, filepath.Join(home, ".plaud", "tokens.json")); err != nil {
				t.Fatal(err)
			}
		}, "tokens.json is a file with another name too"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, elsewhere := t.TempDir(), t.TempDir()
			c.setup(t, home, elsewhere)
			err := checkPlaudLogin(home)
			if c.says == "" {
				if err != nil {
					t.Errorf("checkPlaudLogin = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Errorf("checkPlaudLogin = %v, want it saying %q", err, c.says)
			}
		})
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
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &s)
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
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := tb.plaud.run(ctx, "files"); done <- err }()
		// Cancel only once the child exists: slow runner startup must not
		// turn this process-group test into cancellation before execution.
		pid := 0
		waitFor(t, 10*time.Second, "the fake child to start", func() bool {
			data, _ := os.ReadFile(filepath.Join(fp.dir, "child.pid"))
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			return pid > 0
		})
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a cancelled run reported success")
			}
		case <-time.After(8 * time.Second):
			t.Fatal("the cancelled run did not end")
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

// Fridolin runs one Plaud command at a time, since the CLI rewrites its
// login without a lock; only the sign-in, which waits for the browser,
// runs beside them. Disconnect signs out even when the request that asked
// for it is gone.
func TestPlaudRunsOneCommandAtATime(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	fp.files(plaudOld1)
	fp.write("slow", "")
	done := make(chan struct{})
	for _, cmd := range []string{"me", "files", "me", "file"} {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = tb.plaud.run(context.Background(), cmd, plaudOld1)
		}()
	}
	for range 4 {
		<-done
	}
	if overlap, _ := os.ReadFile(filepath.Join(fp.dir, "overlap.log")); len(overlap) > 0 {
		t.Errorf("commands ran at the same time:\n%s", overlap)
	}

	// A sign-in waiting for the browser holds nothing up.
	_ = os.Remove(tb.plaud.tokenPath())
	fp.write("login_wait", "")
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the sign-in to start", func() bool { return strings.Contains(fp.calls(), " login") })
	start := time.Now()
	_, _ = tb.plaud.run(context.Background(), "me")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("a command waited %v for the sign-in", d)
	}
	fp.write("login_go", "")
	waitFor(t, 10*time.Second, "the sign-in to end", func() bool { return tb.plaud.connected() })

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	w := tb.do("POST", "/api/plaud/disconnect", nil, func(r *http.Request) { *r = *r.WithContext(gone) })
	if w.Code != 200 || !strings.Contains(fp.calls(), " logout\n") {
		t.Errorf("disconnect = %d %s, calls:\n%s", w.Code, w.Body.String(), fp.calls())
	}
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
				job = *tb.plaud.Status().Connect
				return job.State != "running"
			})
			if job.State != "error" || !strings.Contains(job.Error, c.want) || strings.Contains(job.Error, "expired") || strings.Contains(job.Error, "Waiting for") {
				t.Errorf("sign-in = %+v, want an error saying %q", job, c.want)
			}
		})
	}
}

// A sign-in is judged by the login it leaves: the CLI may fail after it
// wrote a new one, which signed in all the same, while a failure that
// leaves the old login in place did not.
func TestPlaudConnectJudgesTheSignInByTheLogin(t *testing.T) {
	for _, c := range []struct {
		name, fixture string
		oldLogin      bool
		want          string
	}{
		{"signed in", "", false, "done"},
		{"failed after writing a new login", "login_then_fail", false, "done"},
		{"failed, the old login stays", "fail_login", true, "error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			if c.oldLogin {
				if err := os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tb.plaud.tokenPath(), []byte(`{"old":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if c.fixture != "" {
				fp.write(c.fixture, "")
			}
			if rec := tb.do("POST", "/api/plaud/connect", nil); rec.Code != 202 {
				t.Fatalf("connect: %d %s", rec.Code, rec.Body)
			}
			var job connectJob
			waitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				job = *tb.plaud.Status().Connect
				return job.State != "running"
			})
			if job.State != c.want {
				t.Errorf("sign-in %s (%s), want %s", job.State, job.Error, c.want)
			}
		})
	}
}

// The card offers the sign-in link the CLI prints when it cannot open a
// browser, and no other: not a link from a line before it, not one that is
// not https (or http to this machine), and not one still arriving.
func TestPlaudSignInURL(t *testing.T) {
	const link = "https://web.plaud.ai/platform/oauth?client_id=x&state=y"
	for _, c := range []struct {
		name, out, want string
	}{
		{"as the CLI prints it", "\nOpening browser for authentication...\n\nCould not open browser. Open this URL manually:\n  " + link + "\n", link},
		{"on the same line", "Could not open browser. Open this URL manually: " + link + "\n", link},
		{"after another link", "See https://docs.example.com/help first.\nCould not open browser. Open this URL manually:\n  " + link + "\n", link},
		{"another link only", "See https://docs.example.com/help first.\n", ""},
		{"plain http elsewhere", "Open this URL manually:\n  http://web.plaud.ai/platform/oauth\n", ""},
		{"http to this machine", "Open this URL manually:\n  http://127.0.0.1:8199/auth/callback?code=1\n", "http://127.0.0.1:8199/auth/callback?code=1"},
		{"not a web link", "Open this URL manually:\n  javascript:alert(1)\n", ""},
		{"still arriving", "Open this URL manually:\n  " + link, ""},
		{"in colour", "\x1b[33mCould not open browser. Open this URL manually:\n  " + link + "\x1b[39m\n", link},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := plaudSignInURL(c.out); got != c.want {
				t.Errorf("plaudSignInURL(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

// The card says which version of the CLI is installed, read from its npm
// package without running it, and warns about one Fridolin does not read.
func TestPlaudCLIVersion(t *testing.T) {
	for _, c := range []struct {
		version string
		warns   string // in the warning; "" for none
	}{
		{plaudCLITested, ""},
		{"0.3.16", ""},
		{"0.3.14", "older than " + plaudCLITested},
		{"0.2.9", "older than"},
		{"0.4.0", "newer than " + plaudCLITested},
		{"1.0.0", "newer than"},
	} {
		t.Run(c.version, func(t *testing.T) {
			// What npm install -g leaves: bin/plaud, a link into the package.
			dir := t.TempDir()
			pkg := filepath.Join(dir, "lib", "node_modules", "@plaud-ai", "cli")
			if err := os.MkdirAll(filepath.Join(pkg, "dist"), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name": "@plaud-ai/cli", "version": "`+c.version+`"}`), 0o644)
			_ = os.WriteFile(filepath.Join(pkg, "dist", "index.js"), []byte("#!/usr/bin/env node\n"), 0o755)
			bin := filepath.Join(dir, "bin", "plaud")
			_ = os.MkdirAll(filepath.Dir(bin), 0o755)
			if err := os.Symlink("../lib/node_modules/@plaud-ai/cli/dist/index.js", bin); err != nil {
				t.Fatal(err)
			}
			tb := newTestBridge(t, `, "plaud": {"cli_path": "`+bin+`"}`)
			cli := tb.plaud.Status().CLI
			if cli.Version != c.version || (cli.Warning == "") != (c.warns == "") || !strings.Contains(cli.Warning, c.warns) {
				t.Errorf("cli = %+v, want version %s and a warning saying %q", cli, c.version, c.warns)
			}
		})
	}
	tb, _ := plaudBridge(t, "")
	if cli := tb.plaud.Status().CLI; !cli.Found || cli.Version != "" || cli.Warning != "" {
		t.Errorf("a CLI outside an npm package: %+v, want found, no version and no warning", cli)
	}
}

// Without plaud.cli_path and with no plaud on PATH, the CLI of the newest
// nvm node version is used, by version and not by name.
func TestFindPlaudCLIPrefersTheNewestNode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range []string{"v9.11.2", "v20.9.0", "v20.10.0", "v18.20.4", "system"} {
		bin := filepath.Join(home, nvmNodeVersions, v, "bin", "plaud")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := findPlaudCLI("")
	if want := filepath.Join(home, nvmNodeVersions, "v20.10.0", "bin", "plaud"); err != nil || got != want {
		t.Errorf("findPlaudCLI = %s, %v; want %s", got, err, want)
	}
}

// The Plaud CLI runs outside the sandbox, and its #!/usr/bin/env node
// looks node up on the PATH it runs with. That PATH starts with the CLI's
// own folder only when that holds the node it was installed with (nvm's
// layout), and otherwise has that folder last, after the folders of the
// bridge's PATH that helperPath keeps: none outside HOME, where a brain
// may have planted a node.
func TestPlaudPATH(t *testing.T) {
	planted, home := plantedOutsideHome(t)
	homeBin := filepath.Join(home, ".local", "bin")
	t.Setenv("PATH", planted+":"+homeBin+":/usr/bin:/bin")
	nvmBin := filepath.Join(home, nvmNodeVersions, "v22.0.0", "bin")
	bare := filepath.Join(t.TempDir(), "bin")
	writeExecutables(t, filepath.Join(nvmBin, "plaud"), filepath.Join(nvmBin, "node"), filepath.Join(bare, "plaud"))
	cases := []struct {
		name, bin, first, last string // last: "" for any
		node                   string // the node the CLI's PATH finds first; "" for any but the planted one
	}{
		{"node next to the CLI", filepath.Join(nvmBin, "plaud"), nvmBin, "", filepath.Join(nvmBin, "node")},
		{"no node next to the CLI", filepath.Join(bare, "plaud"), homeBin, bare, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := plaudPATH(tc.bin)
			dirs := filepath.SplitList(path)
			if len(dirs) == 0 || dirs[0] != tc.first {
				t.Errorf("plaudPATH = %s, want it to start with %s", path, tc.first)
			}
			if tc.last != "" && dirs[len(dirs)-1] != tc.last {
				t.Errorf("plaudPATH = %s, want it to end with %s", path, tc.last)
			}
			for i, d := range dirs {
				if d == planted || d == bare && i < len(dirs)-1 {
					t.Errorf("plaudPATH = %s, which has %s", path, d)
				}
			}
			for _, d := range []string{homeBin, "/usr/bin", "/bin"} {
				if !slices.Contains(dirs, d) {
					t.Errorf("plaudPATH = %s, which lacks %s", path, d)
				}
			}
			if got := lookPathIn("node", path); tc.node != "" && got != tc.node || got == filepath.Join(planted, "node") {
				t.Errorf("node on plaudPATH = %q, want %q", got, tc.node)
			}
		})
	}
}

// TestBrainPlaudPATH: brains that may use the Plaud CLI find it on their
// PATH also where the bridge's PATH does not have it. Its folder comes
// ahead of the bridge's PATH only when it holds the CLI's node (nvm):
// sandbox-exec starts claude by name on that PATH for every brain's run,
// and the CLI's #!/usr/bin/env node looks up node on it. Any other CLI
// folder comes last, so that nothing in it runs in place of a program on
// the bridge's PATH in another brain's run. The sandbox keeps brains from
// writing in any of these folders, as anywhere outside their own places,
// and a brain folder may not hold one (see bridgeProgramsOf), so that no
// brain can put a node in the CLI's folder that would move it first.
func TestBrainPlaudPATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(homeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", homeBin+":/usr/bin:/bin")
	nvmBin := filepath.Join(home, nvmNodeVersions, "v22.0.0", "bin")
	inHome := filepath.Join(home, "tools", "plaud", "node_modules", ".bin")
	outside := filepath.Join(t.TempDir(), "bin")
	writeExecutables(t, filepath.Join(nvmBin, "plaud"), filepath.Join(nvmBin, "node"),
		filepath.Join(inHome, "plaud"), filepath.Join(outside, "plaud"))
	cases := []struct {
		name, cliPath, dir string
		first              bool // the CLI's folder comes first, else last
	}{
		{"nvm, with the CLI's node", "", nvmBin, true},
		{"a cli_path in HOME without a node", filepath.Join(inHome, "plaud"), inHome, false},
		{"a cli_path outside HOME without a node", filepath.Join(outside, "plaud"), outside, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Plaud: config.PlaudConfig{BrainAccess: true, CLIPath: tc.cliPath}}
			got := brainPlaudPATH(cfg)
			dirs := filepath.SplitList(got)
			if want := filepath.Join(tc.dir, "plaud"); lookPathIn("plaud", got) != want {
				t.Errorf("brainPlaudPATH = %s, on which brains do not find %s", got, want)
			}
			if tc.first && dirs[0] != tc.dir || !tc.first && dirs[len(dirs)-1] != tc.dir {
				t.Errorf("brainPlaudPATH = %s, want %s first: %v", got, tc.dir, tc.first)
			}
			i := strings.Index(got, os.Getenv("PATH"))
			if i < 0 {
				t.Fatalf("brainPlaudPATH = %s, which lacks the bridge's PATH", got)
			}
			progs := NewSandboxProfiler(nil, cfg).bridgeProgramsOf(home)
			covered := func(dir string) bool { return slices.Contains(progs.path, dir) || withinAny(dir, progs.dirs) }
			for _, d := range filepath.SplitList(strings.TrimSuffix(got[:i], ":")) {
				if !covered(d) {
					t.Errorf("brainPlaudPATH = %s, whose %s comes ahead of the bridge's PATH but is not kept from brains", got, d)
				}
			}
			if !covered(tc.dir) {
				t.Errorf("the sandbox does not keep brains from planting a node in %s", tc.dir)
			}
		})
	}
}

// writeExecutables writes an executable shell script at each of paths.
func writeExecutables(t *testing.T, paths ...string) {
	t.Helper()
	for _, f := range paths {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlaudConfigValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{`{"plaud":{"brain":"Not A Slug"}}`, `{"plaud":{"interval_seconds":5}}`} {
		if _, err := loadConfig(writeConfig(t, bad)); err == nil {
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
		cmd     string // whose environment is checked
		run     func(t *testing.T)
		desktop bool
	}{
		{"a sync", "me", func(t *testing.T) {
			plaudSignIn(t, tb) // like the real CLI, the fake answers only with a login
			if _, err := tb.plaud.runCLI(context.Background(), "me"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"the sign-in", "login", func(t *testing.T) {
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
			// The sign-in asks who signed in right after, which must not
			// stand in for it.
			data, err := os.ReadFile(filepath.Join(fp.dir, "env_"+tc.cmd+".log"))
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

// With plaud.phone_dir, recordings come from the folder the phone app's
// files are synced to, and Plaud's CLI never runs: the first check only
// remembers what is there, then each new transcript goes to the brain,
// also one that arrives after newer ones, and nothing a sync tool leaves
// beside them counts.
func TestPlaudPhoneFolder(t *testing.T) {
	phone := t.TempDir()
	tb, fp := plaudBridge(t, `, "phone_dir": "`+phone+`"`)
	ctx := context.Background()
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(phone, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !tb.plaud.connected() {
		t.Fatal("a phone folder needs no sign-in")
	}

	put("2026-10-18_09-00-00.txt", "[00:00 - 00:03] Speaker 1: Old.")
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := tb.plaud.loadState(); !slices.Equal(st.Seen, []string{"phone_2026-10-18_09-00-00"}) || st.Login != nil {
		t.Fatalf("seed: %+v", st)
	}

	put("2026-10-18_10-00-00.txt", "[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n[00:04 - 01:05] Speaker 2: Agreed.\n")
	put("2026-10-18_08-00-00.txt", "") // transcribed late, and silent
	put("2026-10-18_10-00-00.mp3", "audio")
	put(".syncthing.2026-10-18_11-00-00.txt.tmp", "half")
	put("2026-10-18_11-00-00.sync-conflict-20261018-110000-ABCDEFG.txt", "conflict")
	put("notes.txt", "mine")
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	tb.waitPlaudSeen(t, "phone_2026-10-18_10-00-00")
	tb.waitPlaudSeen(t, "phone_2026-10-18_08-00-00")
	if st, _ := tb.plaud.loadState(); len(st.Seen) != 3 || len(st.Pending) != 0 {
		t.Fatalf("state = %+v", st)
	}
	data, err := os.ReadFile(filepath.Join(tb.cfgs.Get().MediaDir, plaudDMBrain.ID, "plaud", "2026-10-18 - Phone recording - phone_2026-10-18_10-00-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plaud recording: Phone recording\nRecorded: 2026-10-18 10:00\nDuration: 01:05\n", "=== Transcript ===\n[00:00 - 00:04] Speaker 1: Let's ship"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("transcript file lacks %q:\n%s", want, data)
		}
	}
	waitFor(t, 5*time.Second, "brain replies", func() bool { return len(tb.dev.Outbox()) == 2 })
	if out := tb.dev.Outbox(); !slices.ContainsFunc(out, func(m devchat.Message) bool {
		return strings.Contains(m.Text, `[Plaud] New recording "Phone recording" (2026-10-18 10:00, 01:05)`)
	}) {
		t.Errorf("replies = %+v", out)
	}
	if calls := fp.calls(); calls != "" {
		t.Errorf("the CLI ran:\n%s", calls)
	}

	// A folder that is gone fails the check, so the card says so.
	if err := os.RemoveAll(phone); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(ctx); err == nil || !strings.Contains(err.Error(), "the phone folder") {
		t.Errorf("poll without the folder = %v", err)
	}
}
