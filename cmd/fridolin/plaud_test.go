package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/plaud"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

const (
	plaudOld1 = "of_0000000000000000000001"
	plaudOld2 = "of_0000000000000000000002"
	plaudNew  = "of_0000000000000000000003"
)

// newFakePlaud configures the command integration fixture's child processes.
func newFakePlaud(t *testing.T) *testutil.PlaudFixture {
	fp := testutil.NewPlaudFixture(t)
	t.Setenv("FRIDOLIN_FAKE_PLAUD", "1")
	t.Setenv("FRIDOLIN_FAKE_PLAUD_DIR", fp.Dir)
	// Every CLI call is a run of this test binary; avoid race-detector exit waits.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return fp
}

// plaudDMBrain is brain p, which gets the DMs: the one brain of plaudBridge.
var plaudDMBrain = manifest.Manifest{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true}}

// plaudGroupBrain is a brain id that serves the group and no DMs.
func plaudGroupBrain(id string) manifest.Manifest {
	return manifest.Manifest{ID: id, Name: strings.ToUpper(id), Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}}
}

func plaudBridge(t *testing.T, extra string) (*testBridge, *testutil.PlaudFixture) {
	return plaudBridgeWith(t, "", extra, plaudDMBrain)
}

// plaudBridgeWith takes top-level settings (allowed numbers), extra plaud
// settings and the folder brains to add once the bridge runs.
func plaudBridgeWith(t *testing.T, top, plaudConfig string, brains ...manifest.Manifest) (*testBridge, *testutil.PlaudFixture) {
	fp := newFakePlaud(t)
	tb := newTestBridge(t, top+`, "plaud": {"enabled": true, "cli_path": "`+os.Args[0]+`"`+plaudConfig+`}`)
	for _, m := range brains {
		tb.addFolderBrain(t, m)
	}
	return tb, fp
}

// plaudDeliver starts from a finished sign-in and first sync (written
// directly; each fake CLI call is a process), then lets one finished
// recording arrive and polls once.
func plaudDeliver(t *testing.T, tb *testBridge, fp *testutil.PlaudFixture) error {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(tb.plaudTokenPath()), 0o700)
	_ = os.WriteFile(tb.plaudTokenPath(), []byte("{}"), 0o600)
	if err := tb.writePlaudState(plaudSeededState("", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(plaudNew, plaudOld1)
	fp.Recording(plaudNew, true)
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
	testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
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
	testutil.WaitFor(t, 10*time.Second, "the run to be noted", func() bool {
		last := tb.plaud.Status().LastForwarded
		return last != nil && last.Brain == "g" && last.Status == "done"
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
			testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
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
	if st, _ := tb.readPlaudState(); st.Pending[plaudNew] == nil || slices.Contains(st.Seen, plaudNew) {
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
	testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
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
	if st, _ := tb.readPlaudState(); st.Pending[plaudNew] == nil {
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
			fp.Write(fixture, "")
			err := plaudDeliver(t, tb, fp)
			if err == nil || !strings.Contains(err.Error(), "recording "+plaudNew) {
				t.Fatalf("poll error = %v, want the recording's error", err)
			}
			if st, _ := tb.readPlaudState(); st.Pending[plaudNew] == nil {
				t.Errorf("state = %+v, want the recording still pending", st)
			}
			var status map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &status)
			if e, _ := status["last_error"].(string); !strings.Contains(e, plaudNew) || !strings.Contains(e, "Failed to fetch") || strings.Contains(e, "- Fetching") {
				t.Errorf("status last_error = %q, want the recording's error without the spinner", e)
			}

			_ = os.Remove(filepath.Join(fp.Dir, fixture))
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
			var after map[string]any
			_ = json.Unmarshal(tb.do("GET", "/api/plaud/status", nil).Body.Bytes(), &after)
			if e, ok := after["last_error"]; ok {
				t.Errorf("status last_error = %q after it was delivered", e)
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

func (r *plaudRecorder) submit(br *plaud.Target, jid, sender, text string, media []string, done func(error)) error {
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
// A failed run is repeated later, up to 3 runs, then the
// recording is given up and the chat hears about it. A brain that does not
// take it (its queue is full) leaves it waiting, with the reason on the
// card. After a restart, one whose run never ended is handed over again.
func TestPlaudRecordingIsSeenOnlyOnceItsRunEnds(t *testing.T) {
	stop := []error{claudecli.ErrInterrupted, brainruntime.ErrDropped}
	for _, c := range []struct {
		name    string
		ends    []error // the runs' outcomes, one per check
		seen    bool
		status  string
		handed  int // hand-offs in all
		givenUp bool
	}{
		{"answered", []error{nil}, true, "done", 1, false},
		{"stopped while it ran", stop[:1], true, "stopped", 1, false},
		{"dropped before it ran", stop[1:], true, "stopped", 1, false},
		{"failed once", []error{errors.New("claude exited: exit status 1"), nil}, true, "done", 2, false},
		{"failed every time", []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}, true, "failed", 3, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudBridge(t, "")
			rec := &plaudRecorder{}
			tb.usePlaudRecorder(t, rec)
			plaudSignIn(t, tb)
			if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
				t.Fatal(err)
			}
			fp.Files(plaudNew, plaudOld1)
			fp.Recording(plaudNew, true)
			ctx := context.Background()
			for i, end := range c.ends {
				if err := tb.updatePlaudState(func(st *plaudFixtureState) error {
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
				testutil.WaitFor(t, 5*time.Second, "the notice", func() bool { return len(tb.dev.Outbox()) == 1 })
				if m := tb.dev.Outbox()[0]; !strings.Contains(m.Text, "the brain's run failed 3 times (e3)") {
					t.Errorf("notice = %q", m.Text)
				}
			}
			if c.status == "stopped" || c.givenUp {
				if w := tb.do("POST", "/api/plaud/resend", map[string]string{"id": plaudNew}); w.Code != 202 {
					t.Fatalf("send again: %d %s", w.Code, w.Body.String())
				}
				testutil.WaitFor(t, 5*time.Second, "the recording to be handed over again", func() bool { return rec.count() == c.handed+1 })
			}
		})
	}
	t.Run("not taken", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		rec := &plaudRecorder{refuse: brainruntime.ErrQueueFull}
		tb.usePlaudRecorder(t, rec)
		plaudSignIn(t, tb)
		if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.Files(plaudNew, plaudOld1)
		fp.Recording(plaudNew, true)
		err := tb.plaud.Poll(context.Background())
		if err == nil || !strings.Contains(err.Error(), "brain p did not take it: brain queue is full") || !strings.Contains(err.Error(), "will retry") {
			t.Errorf("poll = %v, want the full queue named", err)
		}
		st, _ := tb.readPlaudState()
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
		tb.usePlaudRecorder(t, rec)
		plaudSignIn(t, tb)
		if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.Files(plaudNew, plaudOld1)
		fp.Recording(plaudNew, true)
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Fridolin stops, which stops the run.
		tb.plaud.Stop()
		rec.end(plaudNew, claudecli.ErrInterrupted)
		tb.usePlaudRecorder(t, rec)
		restarted := tb.plaud
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
		tb.usePlaudRecorder(t, rec)
		plaudSignIn(t, tb)
		if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
			t.Fatal(err)
		}
		fp.Files(plaudNew, plaudOld1)
		fp.Recording(plaudNew, true)
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Ten other recordings were handed over since.
		if err := tb.updatePlaudState(func(st *plaudFixtureState) error { st.Forwarded = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		rec.end(plaudNew, nil)
		if fw := tb.plaud.Status().LastForwarded; fw == nil || fw.ID != plaudNew || fw.Brain != "p" || fw.Status != "done" {
			t.Errorf("last forwarded = %+v, want it answered by brain p", fw)
		}
	})
}

// plaudSeenAll reports whether every recording in ids is seen and none
// waits.
func (tb *testBridge) plaudSeenAll(ids ...string) bool {
	st, _ := tb.readPlaudState()
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
	testutil.WaitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
	if err := tb.submitHandOff(br, dm, "plaud", "second", nil, done("second")); err != nil {
		t.Fatal(err)
	}
	if err := tb.submitHandOff(br, dm, "plaud", "third", nil, done("third")); !errors.Is(err, brainruntime.ErrQueueFull) {
		t.Errorf("a hand-off to a full queue = %v, want brainruntime.ErrQueueFull", err)
	}
	tb.handleInbound(context.Background(), brainruntime.Inbound{Inbound: chat.Inbound{JID: dm, Sender: "15551234567", Text: "!stop", Source: "whatsapp"}})
	testutil.WaitFor(t, 10*time.Second, "both to end", func() bool { mu.Lock(); defer mu.Unlock(); return len(ended) == 2 })
	if !errors.Is(ended["first"], claudecli.ErrInterrupted) || !errors.Is(ended["second"], brainruntime.ErrDropped) {
		t.Errorf("ended = %v, want the first stopped and the second dropped", ended)
	}
	for _, m := range tb.dev.Outbox() {
		if strings.Contains(m.Text, "queue is full") {
			t.Errorf("the chat was told to try again: %q", m.Text)
		}
	}
}

// With nothing waiting, a missing private chat blocks nothing: the poll must
// not fail, and log the same error, on every interval. The card still says
// what is missing, from the status call.
func TestPlaudWithoutAPrivateChatIsQuietWhileNothingWaits(t *testing.T) {
	tb, fp := plaudBridgeWith(t, `, "allowed_numbers": []`, "", plaudGroupBrain("f"))
	_ = os.MkdirAll(filepath.Dir(tb.plaudTokenPath()), 0o700)
	_ = os.WriteFile(tb.plaudTokenPath(), []byte("{}"), 0o600)
	if err := tb.writePlaudState(plaudSeededState("", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(plaudOld1)
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
			_ = os.MkdirAll(filepath.Dir(tb.plaudTokenPath()), 0o700)
			_ = os.WriteFile(tb.plaudTokenPath(), []byte("{}"), 0o600)
			fp.Write("expired", "")
			if err := tb.plaud.Poll(context.Background()); err == nil || err.Error() != "the Plaud login expired" {
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
	testutil.WaitFor(t, 5*time.Second, "login", func() bool { return tb.plaud.Status().Connected })

	// The first sync only remembers what exists.
	fp.Files(plaudOld1, plaudOld2)
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := tb.readPlaudState()
	if len(st.Seen) != 2 || len(tb.dev.Outbox()) != 0 {
		t.Fatalf("seed: state=%+v outbox=%v", st, tb.dev.Outbox())
	}

	// A new recording waits until Plaud has transcribed it.
	fp.Files(plaudNew, plaudOld1, plaudOld2)
	fp.Recording(plaudNew, false)
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := tb.readPlaudState(); len(st.Pending) != 1 {
		t.Fatalf("pending = %v", st.Pending)
	}

	fp.Recording(plaudNew, true)
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
	testutil.WaitFor(t, 5*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
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
	if !strings.Contains(fp.Calls(), "HOME="+filepath.Join(tb.cfgs.Get().DataDir, "plaud", "home")) {
		t.Errorf("CLI not run with the private home:\n%s", fp.Calls())
	}
	token, err := os.ReadFile(tb.plaudTokenPath())
	if err != nil || !strings.Contains(string(token), "test-access") {
		t.Fatalf("the sign-in left no login: %q, %v", token, err)
	}
	body := tb.do("GET", "/api/plaud/status", nil).Body.String()
	if strings.Contains(body, "test-access") || !strings.Contains(body, `"account":"you@example.com"`) {
		t.Errorf("status = %s", body)
	}
}

// plaudSignIn leaves a login in place, as a finished sign-in does.
func plaudSignIn(t *testing.T, tb *testBridge) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tb.plaudTokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.plaudTokenPath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitPlaudSeen waits until recording id is seen and no longer waits, as
// once the brain's run answered it.
func (tb *testBridge) waitPlaudSeen(t *testing.T, id string) {
	t.Helper()
	testutil.WaitFor(t, 10*time.Second, id+" to be seen", func() bool {
		st, _ := tb.readPlaudState()
		return st != nil && st.Pending[id] == nil && slices.Contains(st.Seen, id)
	})
}

// plaudSeeded reports whether the first sync ran.
func (tb *testBridge) plaudSeeded() bool {
	st, _ := tb.readPlaudState()
	return st != nil && st.Seeded
}

// plaudSeededState is the state after the first sync for account ("" for
// one from before accounts were recorded), which saw the recordings seen.
func plaudSeededState(account string, seen ...string) *plaudFixtureState {
	st := newPlaudFixtureState()
	st.Seeded, st.Account, st.Seen = true, account, seen
	return st
}

// plaudWaiting adds recording id, first seen at, to the recordings st
// waits to hand over.
func plaudWaiting(st *plaudFixtureState, id string, at time.Time) *plaudFixtureState {
	st.Pending[id] = &plaudFixturePending{FirstSeen: at}
	return st
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
		testutil.WaitFor(t, 10*time.Second, "the sign-in", func() bool { c := tb.plaud.Status().Connect; return c != nil && c.State == "done" })
		for range 5 {
			if got := account(tb); got != "you@example.com" {
				t.Fatalf("account = %q", got)
			}
		}
		if n := strings.Count(fp.Calls(), " me\n"); n != 1 {
			t.Errorf("`plaud me` ran %d times, want once, after the sign-in", n)
		}
	})
	t.Run("an older login", func(t *testing.T) {
		tb, fp := plaudBridge(t, "")
		plaudSignIn(t, tb)
		testutil.WaitFor(t, 10*time.Second, "the account", func() bool { return account(tb) == "you@example.com" })
		for range 5 {
			account(tb)
		}
		if n := strings.Count(fp.Calls(), " me\n"); n != 1 {
			t.Errorf("`plaud me` ran %d times, want once", n)
		}
	})
}

// plaudRecordLogin records Fridolin's sign-in in st, as a finished sign-in
// does.
func plaudRecordLogin(tb *testBridge, st *plaudFixtureState) *plaudFixtureState {
	st.Login = &plaudFixtureLogin{Dir: tb.plaudFixtureHome(), Since: time.Now()}
	return st
}

// plaudRevokeLogin is plaudRefuseLogin up to the check that gets the
// refusal.
func plaudRevokeLogin(t *testing.T, tb *testBridge, fp *testutil.PlaudFixture) {
	t.Helper()
	plaudSignIn(t, tb)
	st := plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))
	// Who signed in was asked just now, so that the status does not ask
	// again.
	st.Login.Checked = time.Now()
	if err := tb.writePlaudState(st); err != nil {
		t.Fatal(err)
	}
	fp.Files(plaudOld1)
	fp.Write("revoked", "")
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
			fp.Files(plaudOld1, plaudOld2)
			fp.Recording(plaudOld1, true)
			fp.Recording(plaudOld2, true)
			fp.Write(c.fixture, "")
			err := tb.plaud.Poll(context.Background())
			if err == nil || (err.Error() == "the Plaud login expired") != c.auth {
				t.Fatalf("first poll: err = %v, want a failure (auth=%v)", err, c.auth)
			}
			if tb.plaudSeeded() {
				t.Fatal("a failed first sync counted as done")
			}

			// Plaud's CLI deletes a login that expired: you sign in again.
			_ = os.Remove(filepath.Join(fp.Dir, c.fixture))
			plaudSignIn(t, tb)
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if st, _ := tb.readPlaudState(); st == nil || len(st.Seen) != 2 || len(st.Pending) != 0 {
				t.Fatalf("state after the sync = %+v, want both recordings seen", st)
			}
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if files, _ := filepath.Glob(filepath.Join(tb.cfgs.Get().MediaDir, "*", "plaud", "*")); len(files) != 0 {
				t.Errorf("old recordings were written out: %v", files)
			}
			if strings.Contains(fp.Calls(), " file ") {
				t.Errorf("old recordings were fetched:\n%s", fp.Calls())
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
		state   plaudFixtureState
		account string // who `me` says is signed in
		forward bool   // whether plaudNew is sent
		logs    string // why the log says it starts over, if it does
	}{
		{"another account", *plaudSeededState("u_1", plaudOld1), "u_2", false, "signed in to another Plaud account"},
		{"the same account again", *plaudSeededState("u_1", plaudOld1), "u_1", true, ""},
		{"a state from before accounts were recorded", *plaudSeededState("", plaudOld1), "u_1", true, ""},
		{"a state only the expiry notice wrote", plaudFixtureState{AuthNotified: time.Unix(1, 0)}, "u_1", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLog(t)
			tb, fp := plaudBridge(t, "")
			plaudSignIn(t, tb)
			c.state.Pending = map[string]*plaudFixturePending{}
			if err := tb.writePlaudState(&c.state); err != nil {
				t.Fatal(err)
			}
			fp.Write("account", c.account)
			fp.Files(plaudNew, plaudOld1)
			fp.Recording(plaudNew, true)
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := tb.plaudForwarded(); got != c.forward {
				t.Errorf("forwarded = %v, want %v", got, c.forward)
			}
			if c.forward {
				testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
				tb.waitPlaudSeen(t, plaudNew)
			}
			if st, _ := tb.readPlaudState(); st.Account != c.account || !slices.Contains(st.Seen, plaudNew) {
				t.Errorf("state = %+v, want account %s with the recording seen", st, c.account)
			}
			if got := strings.Join(logs(), "\n"); strings.Contains(got, "starting over") != (c.logs != "") || !strings.Contains(got, c.logs) {
				t.Errorf("log = %q, want it to say %q", got, c.logs)
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

// With the real bridge and a slow brain, a backlog never fills the brain's
// queue, however many checks come while it works: none of them finds the
// queue full, and your own message to that brain still gets in.
func TestPlaudBacklogLeavesRoomInTheBrainsQueue(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "60000") // the cleanup stops the runs
	plaudSignIn(t, tb)
	if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudIDs("c", 15)
	fp.Files(append(slices.Clone(backlog), plaudOld1)...)
	for _, id := range backlog {
		fp.Recording(id, true)
	}
	limit := 5 // the contract for the default queue leaves room for chat messages
	for i := range 4 {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
		if s := tb.plaud.Status(); *s.Handed != limit || *s.Pending != len(backlog)-limit {
			t.Fatalf("check %d: %d with the brain and %d waiting, want %d and %d", i+1, *s.Handed, *s.Pending, limit, len(backlog)-limit)
		}
	}
	if _, _, _, err := tb.brainMgr.Submit(brainruntime.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}}); err != nil {
		t.Errorf("your own message to the brain = %v, want it taken", err)
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
	testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })
	// Delivery precedes the completion callback. Let it finish saving state
	// before replacing that state to simulate a retry after a crash.
	testutil.WaitFor(t, 10*time.Second, "the first run to be noted", func() bool {
		last := tb.plaud.Status().LastForwarded
		return last != nil && last.ID == plaudNew && last.Status == "done"
	})

	renamed := fp.TeamSync(plaudNew, true)
	renamed.Name = "Weekly sync"
	fp.Put(renamed)
	if err := tb.writePlaudState(plaudWaiting(plaudSeededState("", plaudOld1), plaudNew, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "second brain reply", func() bool { return len(tb.dev.Outbox()) == 2 })

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
	testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })

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

// "Check now" hands new recordings to a brain, so with sending turned off
// it is refused, and a check asked for anyway does nothing: one from the
// poller, or one Check now started just before the switch went off.
func TestPlaudCheckNowNeedsSendingOn(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.writePlaudState(plaudSeededState("u_1", plaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(plaudNew, plaudOld1)
	fp.Recording(plaudNew, true)
	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"enabled": false}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 409 {
		t.Errorf("check now with sending off = %d %s, want 409", w.Code, w.Body.String())
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if calls := fp.Calls(); calls != "" || tb.plaudForwarded() {
		t.Fatalf("Plaud was checked with sending off:\n%s", calls)
	}

	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"enabled": true}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 202 {
		t.Fatalf("check now = %d %s, want 202", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 10*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) > 0 })
}

// While a real accepted check is waiting in the runner, the HTTP adapter
// reports "already checking"; after it completes the same route starts a new one.
func TestPlaudCheckNowWhileChecking(t *testing.T) {
	tb, _ := plaudBridge(t, "")
	plaudSignIn(t, tb)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	tb.replacePlaud(t, func(o *plaud.Options) {
		o.Runner = func(ctx context.Context, _, _ string, _ io.Writer, args ...string) (string, error) {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
				return "", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	})
	completed := make(chan error, 1)
	go func() { completed <- tb.plaud.Poll(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("check did not enter runner")
	}
	for _, c := range []struct {
		busy bool
		code int
		want string
	}{{true, 200, "already checking"}, {false, 202, "syncing"}} {
		if !c.busy {
			releaseOnce.Do(func() { close(release) })
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
		}
		w := tb.do("POST", "/api/plaud/sync", nil)
		var got struct{ Status string }
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != c.code || got.Status != c.want {
			t.Errorf("check now (busy=%v) = %d %q, want %d %q", c.busy, w.Code, got.Status, c.code, c.want)
		}
	}
	tb.plaud.Stop()
	if !tb.plaudSeeded() {
		t.Error("check did not finish the first sync")
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
		hold    func(t *testing.T, tb *testBridge, fp *testutil.PlaudFixture, held func(context.Context) error)
		lastErr error // what the check then ends with
	}{
		{"in a command", func(t *testing.T, tb *testBridge, fp *testutil.PlaudFixture, held func(context.Context) error) {
			plaudSignIn(t, tb)
			fp.Files(plaudOld1)
			tb.replacePlaud(t, func(o *plaud.Options) {
				o.Runner = func(ctx context.Context, _, _ string, _ io.Writer, args ...string) (string, error) {
					return "", held(ctx)
				}
			})
		}, context.Canceled},
		{"sending the expiry notice", func(t *testing.T, tb *testBridge, fp *testutil.PlaudFixture, held func(context.Context) error) {
			plaudRevokeLogin(t, tb, fp)
			tb.replacePlaud(t, func(o *plaud.Options) { o.Notify = func(ctx context.Context, _, _ string) error { return held(ctx) } })
		}, errors.New("the Plaud login expired")},
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
			lastErr := tb.plaud.Status().LastError
			if !strings.Contains(lastErr, c.lastErr.Error()) {
				t.Errorf("after Stop: last error %q; want %q", lastErr, c.lastErr)
			}

			if w := tb.do("POST", "/api/plaud/sync", nil); w.Code != 503 {
				t.Errorf("check now while stopping = %d %s, want 503", w.Code, w.Body.String())
			}
		})
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
	if err := tb.writePlaudState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
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
	fp.Write("account", "u_2")
	fp.Files(plaudNew, plaudOld1)
	fp.Recording(plaudNew, true)

	if w := tb.do("PUT", "/api/plaud/settings", map[string]any{"brain_access": true}); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	if s := tb.plaud.Status(); s.Connected || !s.LoginMoved || s.Expired {
		t.Errorf("after turning brain_access on: connected=%v moved=%v expired=%v, want only moved", s.Connected, s.LoginMoved, s.Expired)
	}
	_, _ = tb.plaud.CheckNow()
	if calls := fp.Calls(); calls != "" {
		t.Fatalf("the CLI ran with a login Fridolin did not make:\n%s", calls)
	}

	// Connect again: the CLI is already signed in there, and that is now
	// Fridolin's login. Its account is another one, so nothing old is sent.
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the sign-in", func() bool { return tb.plaud.Status().Connected })
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if tb.plaudForwarded() {
		t.Error("the other account's recording was sent")
	}
	if st, _ := tb.readPlaudState(); st.Account != "u_2" || st.Login == nil || st.Login.Dir != tb.home {
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

// Disconnect fails, rather than claim it signed out, when the login is
// still there afterwards (brains and your terminal would go on using it).
// Fridolin itself no longer does.
func TestPlaudDisconnectReportsALoginThatStays(t *testing.T) {
	tb, fp := plaudBridge(t, `, "brain_access": true`)
	plaudSignIn(t, tb)
	fp.Write("logout_keeps", "")
	w := tb.do("POST", "/api/plaud/disconnect", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "still there") {
		t.Errorf("disconnect = %d %s, want 502 saying the login is still there", w.Code, w.Body.String())
	}
	if s := tb.plaud.Status(); s.Connected || s.Expired {
		t.Errorf("after disconnecting: connected=%v expired=%v, want neither: Fridolin signed out of it", s.Connected, s.Expired)
	}
	_ = os.Remove(filepath.Join(fp.Dir, "logout_keeps"))
	if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 || tb.plaud.Status().Connected {
		t.Errorf("disconnect = %d %s, connected = %v", w.Code, w.Body.String(), tb.plaud.Status().Connected)
	}
	// Your terminal signs in to ~/.plaud again: not Fridolin's login until
	// you connect, nor Fridolin's to sign out.
	plaudSignIn(t, tb)
	if tb.plaud.Status().Connected {
		t.Error("a login made after disconnecting counts without connecting")
	}
	if w := tb.do("POST", "/api/plaud/disconnect", nil); w.Code != 200 || !fsx.Exists(tb.plaudTokenPath()) {
		t.Errorf("disconnect again = %d %s, terminal's login there = %v; want it left alone", w.Code, w.Body.String(), fsx.Exists(tb.plaudTokenPath()))
	}
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the sign-in", func() bool { return tb.plaud.Status().Connected })
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
			if err := tb.writePlaudState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
				t.Fatal(err)
			}
			private := filepath.Join(filepath.Join(tb.cfgs.Boot().PlaudDir(), "home"), ".plaud", "tokens.json")
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
			if calls := fp.Calls(); strings.Count(calls, " logout") != 1 || !strings.Contains(calls, "HOME="+filepath.Join(tb.cfgs.Boot().PlaudDir(), "home")+" ") {
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
	tb.handleInbound(context.Background(), brainruntime.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
	testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return len(tb.dev.Outbox()) > 0 })
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
			tb.handleInbound(context.Background(), brainruntime.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
			testutil.WaitFor(t, 10*time.Second, "the run", func() bool { return len(tb.dev.Outbox()) > 0 })
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
	fp.Files()
	_ = tb.plaud.Poll(context.Background())
	if !strings.Contains(fp.Calls(), "HOME="+tb.home+" ") {
		t.Errorf("with brain_access the CLI must use the real home:\n%s", fp.Calls())
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

func TestPlaudConnectShowsTheSignInLinkWhileWaiting(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	fp.Write("login_wait", "")
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
	waitStatus := func(what string, ok func(status) bool) {
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
	waitStatus("the sign-in link", func(s status) bool {
		return s.Connect.State == "running" && s.Connect.AuthURL == "https://web.plaud.ai/platform/oauth?client_id=x&state=y"
	})
	fp.Write("login_go", "")
	waitStatus("the sign-in to finish", func(s status) bool { return s.Connect.State == "done" && s.Connected })
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
				if err := os.MkdirAll(filepath.Dir(tb.plaudTokenPath()), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tb.plaudTokenPath(), []byte(`{"old":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if c.fixture != "" {
				fp.Write(c.fixture, "")
			}
			if rec := tb.do("POST", "/api/plaud/connect", nil); rec.Code != 202 {
				t.Fatalf("connect: %d %s", rec.Code, rec.Body)
			}
			var job plaud.ConnectJob
			testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				job = *tb.plaud.Status().Connect
				return job.State != "running"
			})
			if job.State != c.want {
				t.Errorf("sign-in %s (%s), want %s", job.State, job.Error, c.want)
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
		{"0.3.15", ""},
		{"0.3.16", ""},
		{"0.3.14", "older than " + "0.3.15"},
		{"0.2.9", "older than"},
		{"0.4.0", "newer than " + "0.3.15"},
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
			covered := func(dir string) bool { return slices.Contains(progs.Path, dir) || withinAny(dir, progs.Dirs) }
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
	bin, err := findPlaudCLI(tb.cfgs.Get().Plaud.CLIPath)
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
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"the sign-in", "login", func(t *testing.T) {
			_ = os.Remove(tb.plaudTokenPath()) // signed out again, so the sign-in runs
			if w := tb.do("POST", "/api/plaud/connect", nil); w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			testutil.WaitFor(t, 5*time.Second, "login", func() bool { return tb.plaud.Status().Connected })
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t)
			// The sign-in asks who signed in right after, which must not
			// stand in for it.
			data, err := os.ReadFile(filepath.Join(fp.Dir, "env_"+tc.cmd+".log"))
			if err != nil {
				t.Fatal(err)
			}
			got := map[string][]string{}
			for _, kv := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
				k, v, _ := strings.Cut(kv, "=")
				got[k] = append(got[k], v)
			}
			for k, want := range map[string]string{
				"HOME":                     tb.plaudFixtureHome(),
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
	if !tb.plaud.Status().Connected {
		t.Fatal("a phone folder needs no sign-in")
	}

	put("2026-10-18_09-00-00.txt", "[00:00 - 00:03] Speaker 1: Old.")
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := tb.readPlaudState(); !slices.Equal(st.Seen, []string{"phone_2026-10-18_09-00-00"}) || st.Login != nil {
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
	if st, _ := tb.readPlaudState(); len(st.Seen) != 3 || len(st.Pending) != 0 {
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
	testutil.WaitFor(t, 5*time.Second, "brain replies", func() bool { return len(tb.dev.Outbox()) == 2 })
	if out := tb.dev.Outbox(); !slices.ContainsFunc(out, func(m devchat.Message) bool {
		return strings.Contains(m.Text, `[Plaud] New recording "Phone recording" (2026-10-18 10:00, 01:05)`)
	}) {
		t.Errorf("replies = %+v", out)
	}
	if calls := fp.Calls(); calls != "" {
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

// HTTP disconnect still signs out when its caller has already left. The
// service's command-serialization test separately exercises concurrent runs.
func TestPlaudDisconnectAfterTheRequestEnds(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	w := tb.do("POST", "/api/plaud/disconnect", nil, func(r *http.Request) { *r = *r.WithContext(gone) })
	if w.Code != 200 || !strings.Contains(fp.Calls(), " logout\n") {
		t.Errorf("disconnect = %d %s, calls:\n%s", w.Code, w.Body.String(), fp.Calls())
	}
}

// The dashboard's disconnect clears a remembered expiry as well as a login
// still present on disk; daily-notice scheduling is exercised with service ticks.
func TestPlaudDashboardDisconnectClearsExpiredLogin(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	plaudSignIn(t, tb)
	if err := tb.writePlaudState(plaudRecordLogin(tb, plaudSeededState("u_1", plaudOld1))); err != nil {
		t.Fatal(err)
	}
	fp.Write("expired", "")
	if err := tb.plaud.Poll(context.Background()); err == nil || err.Error() != "the Plaud login expired" {
		t.Fatalf("expiry = %v", err)
	}
	if s := tb.plaud.Status(); !s.Expired {
		t.Fatal("login was not marked expired")
	}
	w := tb.do("POST", "/api/plaud/disconnect", nil)
	if w.Code != 200 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	if s := tb.plaud.Status(); s.Expired || s.Connected {
		t.Errorf("after disconnect: %+v", s)
	}
}
