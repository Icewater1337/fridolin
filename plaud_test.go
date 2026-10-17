package main

import (
	"context"
	"encoding/json"
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

func (f *fakePlaudFixture) files(ids ...string) {
	var sb strings.Builder
	sb.WriteString("- Fetching files...\n\nFiles on this page: x\n\n  ID   NAME   DATE   DURATION\n  ────\n")
	for _, id := range ids {
		sb.WriteString("  " + id + "  Some name  2026-10-16  00:05:00\n")
	}
	f.write("files.txt", sb.String())
}

func (f *fakePlaudFixture) recording(id string, transcriptReady bool) {
	state := "pending"
	if transcriptReady {
		state = "available"
	}
	f.write("file_"+id+".txt", "- Fetching file...\n\nFile Details:\n\n  id:           "+id+"\n  name:         Team sync\n  created_at:   2026-10-16T18:00:00\n  start_at:   2026-10-16T18:00:00\n  duration:   00:05:00\n  transcript:   "+state+"\n  summary:      available\n")
	f.write("transcript_"+id+".txt", "- Fetching transcript...\n\nTranscript: Team sync\n\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n")
	f.write("summary_"+id+".txt", "- Fetching summary...\n\nSummary: Decided to ship on Friday.\n")
}

func (f *fakePlaudFixture) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	return string(b)
}

func plaudBridge(t *testing.T, extra string) (*testBridge, *fakePlaudFixture) {
	fp := newFakePlaud(t)
	tb := newTestBridge(t, `, "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}], "plaud": {"enabled": true, "cli_path": "`+os.Args[0]+`"`+extra+`}`)
	return tb, fp
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

func TestPlaudExpiredLoginNotifiesOncePerDay(t *testing.T) {
	tb, fp := plaudBridge(t, "")
	_ = os.MkdirAll(filepath.Join(tb.cfgs.Get().DataDir, "plaud", "home", ".plaud"), 0o700)
	_ = os.WriteFile(tb.plaud.tokenPath(), []byte("{}"), 0o600)
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
	var st map[string]any
	data, _ := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "plaud", "state.json"))
	_ = json.Unmarshal(data, &st)
	if st["auth_notified"] == float64(0) {
		t.Error("the notice time was not remembered")
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
