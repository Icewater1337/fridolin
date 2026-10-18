package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBridge struct {
	*Bridge
	h    http.Handler
	home string
}

// newTestBridge starts a dev-mode bridge in a temp home. extra is appended to
// the config's JSON object.
func newTestBridge(t *testing.T, extra string) *testBridge {
	t.Helper()
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	claude := fakeClaudeEnv(t, 10)
	path := writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`",
		"allowed_numbers": ["15551234567"],
		"allowed_groups": ["120363000000000000@g.us"],
		"debounce": {"quiet_ms": 0}`+extra+`
	}`)
	cfgs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())
	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	return &testBridge{Bridge: b, h: b.newHTTPHandler("127.0.0.1:8765"), home: home}
}

// do sends a request the way the dashboard does, then lets mutate adjust it.
func (tb *testBridge) do(method, path string, body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, rd)
	r.Host = "127.0.0.1:8765"
	r.Header.Set("X-Bridge-Token", tb.token.Value())
	if method != http.MethodGet {
		r.Header.Set("X-Bridge-Dashboard", "1")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	return w
}

// ingest posts to /ingest the way a local script does: no dashboard token,
// the scripts token.
func (tb *testBridge) ingest(body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8765/ingest", strings.NewReader(body))
	r.Host = "127.0.0.1:8765"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tb.scriptAuth.scripts.Value())
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	return w
}

// addFolderBrain adds a folder brain through the dashboard API, as the Add
// brain page does, and returns its folder.
func (tb *testBridge) addFolderBrain(t *testing.T, m BrainManifest) string {
	t.Helper()
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + m.ID, "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatalf("add brain %s: %d %s", m.ID, w.Code, w.Body.String())
	}
	return filepath.Join(tb.home, m.ID)
}

func TestDashboardGuards(t *testing.T) {
	tb := newTestBridge(t, "")
	cases := []struct {
		name   string
		method string
		path   string
		mutate func(*http.Request)
		want   int
	}{
		{"ok", "GET", "/api/status", nil, 200},
		{"dns rebinding", "GET", "/api/status", func(r *http.Request) { r.Host = "evil.example.com:8765" }, 421},
		{"no token", "GET", "/api/status", func(r *http.Request) { r.Header.Del("X-Bridge-Token") }, 401},
		{"wrong token", "GET", "/api/status", func(r *http.Request) { r.Header.Set("X-Bridge-Token", "nope") }, 401},
		{"cross-site origin", "POST", "/api/brains/inspect", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, 403},
		{"cross-site fetch", "POST", "/api/brains/inspect", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"no dashboard header", "POST", "/api/brains/inspect", func(r *http.Request) { r.Header.Del("X-Bridge-Dashboard") }, 403},
		{"form post", "POST", "/api/brains/inspect", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"same origin", "POST", "/api/brains/inspect", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8765") }, 200},
	}
	for _, c := range cases {
		var body any
		if c.method == "POST" {
			body = map[string]string{"path": "~/new"}
		}
		var muts []func(*http.Request)
		if c.mutate != nil {
			muts = append(muts, c.mutate)
		}
		if w := tb.do(c.method, c.path, body, muts...); w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}

	// The page itself loads with the strict CSP.
	w := tb.do("GET", "/", nil, func(r *http.Request) { r.Header.Del("X-Bridge-Token") })
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("index: %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
}

// The page's modules are served as JavaScript, which browsers require of
// module scripts. The JavaScript tests and the package.json that marks the
// modules for node sit in web/ too, but are not part of the page.
func TestDashboardServesOnlyThePage(t *testing.T) {
	tb := newTestBridge(t, "")
	cases := []struct {
		path string
		want int
	}{
		{"/", 200},
		{"/favicon.svg", 200},
		{"/assets/app/main.js", 200},
		{"/assets/lib/format.js", 200},
		{"/package.json", 404},
		{"/test/format.test.js", 404},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := tb.do("GET", c.path, nil)
			if w.Code != c.want {
				t.Fatalf("GET %s: %d, want %d", c.path, w.Code, c.want)
			}
			if ct := w.Header().Get("Content-Type"); c.want == 200 && strings.HasSuffix(c.path, ".js") && !strings.Contains(ct, "javascript") {
				t.Errorf("GET %s: Content-Type %q, want JavaScript", c.path, ct)
			}
			// Browsers take an SVG icon only as image/svg+xml.
			if ct := w.Header().Get("Content-Type"); c.want == 200 && strings.HasSuffix(c.path, ".svg") && ct != "image/svg+xml" {
				t.Errorf("GET %s: Content-Type %q, want image/svg+xml", c.path, ct)
			}
		})
	}
}

// The page's files carry a validator, so a browser asks whether each
// changed instead of downloading all of them on every visit, and a
// folder is not listed.
func TestDashboardStaticFilesHaveValidatorsAndNoListings(t *testing.T) {
	tb := newTestBridge(t, "")
	get := func(path string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		return tb.do("GET", path, nil, append([]func(*http.Request){func(r *http.Request) { r.Header.Del("X-Bridge-Token") }}, mutate...)...)
	}
	for _, path := range []string{"/assets/", "/assets", "/assets/app/", "/assets/css/", "/nope.js"} {
		t.Run("no listing "+path, func(t *testing.T) {
			w := get(path)
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<a href") {
				t.Errorf("GET %s: %d %q, want 404 without a listing", path, w.Code, w.Body.String())
			}
		})
	}
	etags := map[string]string{}
	for _, path := range []string{"/", "/assets/app.css", "/assets/app/main.js"} {
		t.Run("validator "+path, func(t *testing.T) {
			w := get(path)
			etag := w.Header().Get("ETag")
			if w.Code != 200 || !strings.HasPrefix(etag, `"`) || w.Header().Get("Cache-Control") != "no-cache" {
				t.Fatalf("GET %s: %d, ETag %q, Cache-Control %q; want 200, an ETag and no-cache", path, w.Code, etag, w.Header().Get("Cache-Control"))
			}
			etags[path] = etag
			if w := get(path, func(r *http.Request) { r.Header.Set("If-None-Match", etag) }); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
				t.Errorf("GET %s with its ETag: %d with %d bytes, want 304 and no body", path, w.Code, w.Body.Len())
			}
			if w := get(path, func(r *http.Request) { r.Header.Set("If-None-Match", `"stale"`) }); w.Code != 200 || w.Body.Len() == 0 {
				t.Errorf("GET %s with another ETag: %d, want the file", path, w.Code)
			}
		})
	}
	if etags["/"] == etags["/assets/app.css"] {
		t.Errorf("files with different content share the ETag %s", etags["/"])
	}
	// A redirect to the file is not the file, so it carries no validator.
	for _, c := range []struct{ path, to string }{{"/index.html", "./"}, {"/assets/app.css/", "../app.css"}} {
		t.Run("redirect "+c.path, func(t *testing.T) {
			w := get(c.path)
			if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != c.to {
				t.Fatalf("GET %s: %d to %q, want a redirect to %s", c.path, w.Code, w.Header().Get("Location"), c.to)
			}
			if etag := w.Header().Get("ETag"); etag != "" {
				t.Errorf("GET %s: the redirect carries the ETag %s", c.path, etag)
			}
		})
	}
}

// TestStatusWarnsWhenBrainsInheritYourClaudeSetup: inherit_claude_user_config
// has no dashboard field, so the status says when it is on.
func TestStatusWarnsWhenBrainsInheritYourClaudeSetup(t *testing.T) {
	cases := []struct {
		name, extra string
		want        bool
	}{
		{"off", "", false},
		{"on", `, "inherit_claude_user_config": true`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestBridge(t, tc.extra)
			w := tb.do("GET", "/api/status", nil)
			var st statusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, warning := range st.Warnings {
				if strings.Contains(warning, "inherit_claude_user_config") {
					got = append(got, warning)
				}
			}
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Errorf("warnings about it = %q, want one: %v", got, tc.want)
			}
		})
	}
}

func TestSendKeepsScriptContractButBlocksBrowsers(t *testing.T) {
	tb := newTestBridge(t, "")
	send := func(mutate func(*http.Request)) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"15551234567@s.whatsapp.net","text":"hello"}`))
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Content-Type", "application/json")
		if mutate != nil {
			mutate(r)
		}
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		return w.Code
	}
	if code := send(nil); code != 200 {
		t.Errorf("script /send: %d, want 200 without any token", code)
	}
	if code := send(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }); code != 403 {
		t.Errorf("cross-site /send: %d, want 403", code)
	}
	if out := tb.dev.Outbox(); len(out) != 1 || out[0].Text != "hello" {
		t.Errorf("outbox = %+v", out)
	}
}

// POST /send refuses Status, broadcast lists and channels with a JSON
// error: a script must not be able to post a Status in the bridge's name.
func TestHTTPSendRefusesStatusBroadcastAndChannels(t *testing.T) {
	tb := newTestBridge(t, "")
	for _, jid := range []string{"status@broadcast", "1700000000@broadcast", "120363000000000000@newsletter"} {
		t.Run(jid, func(t *testing.T) {
			body, _ := json.Marshal(sendRequest{JID: jid, Text: "hello"})
			r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", bytes.NewReader(body))
			r.Host = "127.0.0.1:8765"
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			tb.h.ServeHTTP(w, r)

			var resp map[string]string
			if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") != "application/json" ||
				json.Unmarshal(w.Body.Bytes(), &resp) != nil || !strings.Contains(resp["error"], "refusing to send") {
				t.Errorf("status %d, %s %q; want 400 with a JSON error", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		})
	}
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("sent anyway: %+v", out)
	}
}

func TestSendRefusesMissingJIDOrText(t *testing.T) {
	// Blank text gives no chunks, so it used to send nothing while still
	// answering "sent" and writing the blank text to the brain's history.
	tb := newTestBridge(t, "")
	dir := tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
	cases := []struct {
		name string
		body string
	}{
		{"no jid", `{"text":"hello"}`},
		{"no text", `{"jid":"15551234567@s.whatsapp.net"}`},
		{"empty text", `{"jid":"15551234567@s.whatsapp.net","text":""}`},
		{"blank text", `{"jid":"15551234567@s.whatsapp.net","text":"  \n\t "}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(c.body))
			r.Host = "127.0.0.1:8765"
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			tb.h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("outbox = %+v, want nothing sent", out)
	}
	if hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl")); len(hist) != 0 {
		t.Errorf("history = %s, want nothing recorded", hist)
	}
}

// stateMessenger is the dev messenger with a WhatsApp state of the test's
// choosing.
type stateMessenger struct {
	*devMessenger
	state WAState
}

func (m stateMessenger) State() WAState { return m.state }

func TestReadyReportsWhetherMessagesCanGoOut(t *testing.T) {
	// /health only says the process is up. /ready tells a script (or
	// setup) whether /send can deliver, without the dashboard token.
	cases := []struct {
		name  string
		dev   bool
		mode  string
		ready bool
	}{
		{"connected", false, "connected", true},
		{"not paired yet", false, "pairing", false},
		{"connecting", false, "connecting", false},
		{"logged out", false, "logged-out", false},
		{"replaced by another session", false, "replaced", false},
		{"dev mode simulates sending", true, "disabled", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			if !c.dev {
				tb.wa = stateMessenger{devMessenger: tb.dev, state: WAState{Mode: c.mode, OwnNumber: "15551234567"}}
				tb.dev = nil
			}
			r := httptest.NewRequest("GET", "http://127.0.0.1:8765/ready", nil)
			r.Host = "127.0.0.1:8765"
			w := httptest.NewRecorder()
			tb.h.ServeHTTP(w, r)

			want := http.StatusServiceUnavailable
			if c.ready {
				want = http.StatusOK
			}
			var resp readyResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != want {
				t.Fatalf("status %d (%v) %s, want %d with JSON", w.Code, err, w.Body.String(), want)
			}
			if resp.Ready != c.ready || resp.WhatsApp != c.mode || resp.Version != bridgeVersion() || !resp.StartedAt.Equal(tb.started) {
				t.Errorf("body %+v, want ready=%v whatsapp=%q version=%q started_at=%v", resp, c.ready, c.mode, bridgeVersion(), tb.started)
			}
			if strings.Contains(w.Body.String(), "15551234567") {
				t.Errorf("/ready names the account: %s", w.Body.String())
			}
		})
	}

	t.Run("refused under another host name", func(t *testing.T) {
		tb := newTestBridge(t, "")
		r := httptest.NewRequest("GET", "http://evil.example.com:8765/ready", nil)
		r.Host = "evil.example.com:8765"
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("status %d, want 421", w.Code)
		}
	})
}

func TestIngestConfinedToMediaDir(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	dir := tb.addFolderBrain(t, BrainManifest{ID: "personal", Name: "Personal", Routing: BrainRouting{HandlesDMs: true}})
	cfg := tb.cfgs.Get()
	inside := filepath.Join(cfg.MediaDir, "upload", "scan.pdf")
	_ = os.MkdirAll(filepath.Dir(inside), 0o700)
	_ = os.WriteFile(inside, []byte("%PDF"), 0o600)
	outside := filepath.Join(tb.home, "secret.txt")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	link := filepath.Join(cfg.MediaDir, "link.txt")
	_ = os.Symlink(outside, link)

	ingest := tb.ingest
	w := ingest(`{"brain":"personal","paths":["` + inside + `"],"text":"file this"}`)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"accepted"`) || !strings.Contains(w.Body.String(), "15551234567@s.whatsapp.net") {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"outside media": `{"brain":"personal","paths":["` + outside + `"]}`,
		"symlink out":   `{"brain":"personal","paths":["` + link + `"]}`,
		"relative":      `{"brain":"personal","paths":["upload/scan.pdf"]}`,
		"unknown brain": `{"brain":"nobody","paths":["` + inside + `"]}`,
	} {
		if w := ingest(body); w.Code != 400 {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	waitFor(t, 5*time.Second, "ingest reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
	if !strings.Contains(string(hist), "[attachment: "+canonicalDir(inside)+"]") {
		t.Errorf("history does not record the ingested file:\n%s", hist)
	}
}

// An unknown brain is named as it was sent, in Go quotes.
func TestUnknownBrainIsQuotedAsSent(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	const want = `unknown brain "<a&b>"`
	var body struct{ Error string }
	w := tb.ingest(`{"brain":"<a&b>","text":"x"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || body.Error != want {
		t.Errorf("ingest: %d %q, want 400 saying %s", w.Code, body.Error, want)
	}
	w = tb.do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "brain": "<a&b>", "text": "x"})
	body.Error = ""
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || body.Error != want {
		t.Errorf("dev inbound: %d %q, want 400 saying %s", w.Code, body.Error, want)
	}
}

// scope was the name of the brain field. A script that still sends it is
// told the field to use, even when it names a brain that exists, and
// nothing is handed to any brain.
func TestIngestRefusesScope(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	dir := tb.addFolderBrain(t, BrainManifest{ID: "personal", Name: "Personal", Routing: BrainRouting{HandlesDMs: true}})
	for name, body := range map[string]string{
		"scope only":         `{"scope":"personal","text":"file this"}`,
		"scope and brain":    `{"scope":"personal","brain":"personal","text":"file this"}`,
		"scope, other brain": `{"scope":"nobody","brain":"personal","text":"file this"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := tb.ingest(body)
			var got struct{ Error string }
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != http.StatusBadRequest || !strings.Contains(got.Error, `name the brain in "brain"`) {
				t.Errorf("%d %q, want 400 naming the brain field", w.Code, w.Body.String())
			}
		})
	}
	if w := tb.ingest(`{"brain":"personal","text":"file this"}`); w.Code != http.StatusAccepted {
		t.Fatalf("with brain: %d %s, want 202", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "the reply to the one accepted request", func() bool { return len(tb.dev.Outbox()) > 0 })
	hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
	if n := strings.Count(string(hist), `"type":"in"`); n != 1 {
		t.Errorf("history has %d ingested messages, want only the accepted one:\n%s", n, hist)
	}
}

// A brain without chats of its own (an upload or recorder brain) answers in
// its reply_chat, here the DM that belongs to another brain. The files must
// still be handled by the brain the request names: its prompt, memory,
// sandbox and history.
func TestIngestRunsInTheNamedBrain(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	dmDir := tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	upDir := tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads", ReplyChat: dm})

	w := tb.ingest(`{"brain":"up","text":"file this"}`)
	if w.Code != 202 || !strings.Contains(w.Body.String(), dm) {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "ingest reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	out := tb.dev.Outbox()[0]
	if out.JID != dm || !strings.Contains(out.Text, "echo:"+upDir+":upload says: file this") {
		t.Errorf("reply = %+v, want it from %s in %s", out, upDir, dm)
	}
	upHist, _ := os.ReadFile(filepath.Join(upDir, "conversation-history.jsonl"))
	if !strings.Contains(string(upHist), "file this") {
		t.Errorf("up's history lacks the message:\n%s", upHist)
	}
	if dmHist, _ := os.ReadFile(filepath.Join(dmDir, "conversation-history.jsonl")); strings.Contains(string(dmHist), "file this") {
		t.Errorf("the DM brain saw a message meant for up:\n%s", dmHist)
	}
}

// The OTP guard checks the brain the hand-off names, not the brain that owns
// its reply chat: up asked for the code, so up's guard must catch it.
func TestIngestOTPGuardChecksTheNamedBrain(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, `, "ingest": {"enabled": true}, "otp_guard": true, "otp_markers": ["Examplebank 2FA"]`)
	tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	upDir := tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads", ReplyChat: dm})
	tb.brainMgr.Get("up").appendHistory(dm, "up", "Examplebank 2FA: reply with the code", historyOut)

	if w := tb.ingest(`{"brain":"up","text":"483920"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	otp := filepath.Join(tb.cfgs.Get().DataDir, "otp", "up.json")
	waitFor(t, 3*time.Second, "otp file", func() bool { _, err := os.Stat(otp); return err == nil })
	time.Sleep(200 * time.Millisecond)
	if hist, _ := os.ReadFile(filepath.Join(upDir, "conversation-history.jsonl")); strings.Contains(string(hist), "483920") {
		t.Error("the code reached up's history; the guard checked the wrong brain")
	}
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("outbox = %+v, want nothing: a caught code must not start a run", out)
	}
}

// !stop in a chat also stops a hand-off that answers there although another
// brain runs it: up answers /ingest in the DM, which belongs to dm, so dm has
// nothing to stop. WhatsApp may address the DM by the sender's LID, while the
// hand-off names the phone number; it is the same chat.
func TestStopReachesAHandOffInItsReplyChat(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	for _, c := range []struct {
		name string
		stop map[string]any // the dev message that sends !stop
	}{
		{"from the chat", map[string]any{"jid": dm, "text": "!stop"}},
		{"from the chat's LID", map[string]any{"jid": "15559876543@lid", "sender": "15551234567", "text": "!stop"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
			tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
			tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads", ReplyChat: dm})
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
			up := tb.brainMgr.Get("up")

			if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
			waitFor(t, 5*time.Second, "up's run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
			if w := tb.ingest(`{"brain":"up","text":"and this"}`); w.Code != 202 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
			waitFor(t, 5*time.Second, "the second file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

			if w := tb.do("POST", "/api/dev/inbound", c.stop); w.Code != 202 || !strings.Contains(w.Body.String(), "accepted") {
				t.Fatalf("!stop: %d %s", w.Code, w.Body.String())
			}
			answer := func() string {
				for _, m := range tb.dev.Outbox() {
					if strings.Contains(m.Text, "Stopped") || strings.Contains(m.Text, "Nothing was running") {
						return m.Text
					}
				}
				return ""
			}
			waitFor(t, 15*time.Second, "the answer to !stop", func() bool { return answer() != "" })
			if got := answer(); !strings.HasPrefix(got, "🛑 Stopped") {
				t.Errorf("!stop answered %q, want that it stopped up's run", got)
			}
			if rt := up.Snapshot(); rt.Running != nil || len(rt.Queue) != 0 {
				t.Errorf("up still has work after !stop: running %+v, queue %+v", rt.Running, rt.Queue)
			}
		})
	}
}

// !reset stops what !stop stops, so it reaches a hand-off that another brain
// answers in the chat: once it says the chat has nothing running or waiting,
// up must have neither the file it is on nor the one waiting.
func TestResetReachesAHandOffInItsReplyChat(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads", ReplyChat: dm})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	up := tb.brainMgr.Get("up")

	if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "up's run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
	if w := tb.ingest(`{"brain":"up","text":"and this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "the second file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

	tb.handleInbound(context.Background(), Inbound{JID: dm, Sender: "15551234567", Text: "!reset", Source: sourceWhatsApp})
	if !tb.sentPrefix(dm, "✅ Cleared anything this chat had running or waiting") {
		t.Errorf("!reset answered %q, want that it cleared the chat", tb.sentTo(dm))
	}
	if rt := up.Snapshot(); rt.Running != nil || len(rt.Queue) != 0 {
		t.Errorf("up still has work after !reset: running %+v, queue %+v", rt.Running, rt.Queue)
	}
}

// !stop that finds no run for the chat but drops hand-offs waiting to answer
// there says it cleared them: up is busy with its own group, and a file
// handed to it for the DM waits behind that run.
func TestStopCountsWaitingHandOffs(t *testing.T) {
	const (
		dm    = "15551234567@s.whatsapp.net"
		group = "120363000000000000@g.us"
	)
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads", ReplyChat: dm, Routing: BrainRouting{Groups: []string{group}}})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	up := tb.brainMgr.Get("up")

	if _, _, _, err := tb.brainMgr.Submit(Inbound{JID: group, Sender: "15551234567", Text: "group work", Source: sourceWhatsApp}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "up's group run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
	if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, "the file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

	tb.handleInbound(context.Background(), Inbound{JID: dm, Sender: "15551234567", Text: "!stop", Source: sourceWhatsApp})
	if !tb.sentPrefix(dm, "🛑 Nothing was running for this chat; cleared") {
		t.Errorf("!stop answered %q, want that it cleared the waiting file", tb.sentTo(dm))
	}
	rt := up.Snapshot()
	if len(rt.Queue) != 0 {
		t.Errorf("up still has %+v waiting", rt.Queue)
	}
	if rt.Running == nil || rt.Running.Chat != group {
		t.Errorf("up's own group run was stopped: running %+v", rt.Running)
	}
}

func TestReplyChatFor(t *testing.T) {
	const (
		dm = "15551234567@s.whatsapp.net"
		g1 = "120363000000000001@g.us"
		g2 = "120363000000000002@g.us"
	)
	dmBrain := BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}}
	for _, c := range []struct {
		name   string
		extra  string
		brains []BrainManifest
		brain  string
		want   string
	}{
		{"DM brain with a group answers in the DM", "",
			[]BrainManifest{{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true, Groups: []string{g1}}}}, "p", dm},
		{"fallback brain gets the DMs, so it answers there before its group", "",
			[]BrainManifest{{ID: "f", Name: "F", Routing: BrainRouting{Default: true, Groups: []string{g1}}}}, "f", dm},
		{"group brain answers in its group", "",
			[]BrainManifest{dmBrain, {ID: "g", Name: "G", Routing: BrainRouting{Groups: []string{g2}}}}, "g", g2},
		{"brain without chats has none", "",
			[]BrainManifest{dmBrain, {ID: "n", Name: "N"}}, "n", ""},
		{"no allowed number: the fallback's group", `, "allowed_numbers": []`,
			[]BrainManifest{{ID: "f", Name: "F", Routing: BrainRouting{Groups: []string{g1}}}}, "f", g1},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, c.extra)
			for _, m := range c.brains {
				tb.addFolderBrain(t, m)
			}
			if got := tb.replyChatFor(tb.brainMgr.Get(c.brain), tb.cfgs.Get()); got != c.want {
				t.Errorf("replyChatFor(%s) = %q, want %q", c.brain, got, c.want)
			}
		})
	}
}

// The dev page can hand a message to a named brain, the way /ingest does, to
// try out hand-offs without WhatsApp. Like a hand-off it carries no commands:
// "!help" is text for the named brain, not a command for the chat's owner.
func TestDevInboundCanNameABrain(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	upDir := tb.addFolderBrain(t, BrainManifest{ID: "up", Name: "Uploads"})

	if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": dm, "brain": "nobody", "text": "x"}); w.Code != 400 {
		t.Errorf("unknown brain: %d, want 400", w.Code)
	}
	for i, text := range []string{"hello up", "!help"} {
		t.Run(text, func(t *testing.T) {
			if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": dm, "brain": " Up ", "text": text}); w.Code != 202 {
				t.Fatalf("dev inbound: %d %s", w.Code, w.Body.String())
			}
			waitFor(t, 5*time.Second, "reply", func() bool { return len(tb.dev.Outbox()) > i })
			if out := tb.dev.Outbox()[0]; out.JID != dm || !strings.Contains(out.Text, "echo:"+upDir+":") || !strings.Contains(out.Text, text) {
				t.Errorf("reply = %+v, want an echo of %q from %s", out, text, upDir)
			}
		})
	}
}

func TestAddBrainAllowChatAndRoute(t *testing.T) {
	tb := newTestBridge(t, "")
	dir := filepath.Join(tb.home, "family")

	w := tb.do("POST", "/api/brains/inspect", map[string]string{"path": "~/family"})
	var insp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &insp)
	if w.Code != 200 || insp["exists"] != false {
		t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
	}

	m := BrainManifest{ID: "family", Name: "Family", Routing: BrainRouting{HandlesDMs: true}}
	w = tb.do("POST", "/api/brains", map[string]any{"path": "~/family", "manifest": m, "create_dir": true})
	if w.Code != 200 {
		t.Fatalf("add brain: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		t.Error("brain.json not written")
	}
	if br := tb.brainMgr.BrainFor("15551234567@s.whatsapp.net", false); br == nil || br.ID() != "family" {
		t.Fatal("the new brain must take DMs immediately, without a restart")
	}
	if f := tb.cfgs.FileCopy(); len(f.BrainDirs) != 1 || f.BrainDirs[0].Path != "~/family" {
		t.Errorf("brain_dirs = %+v", f.BrainDirs)
	}

	// A second DM brain is refused.
	m2 := BrainManifest{ID: "work", Name: "Work", Routing: BrainRouting{HandlesDMs: true}}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/work", "manifest": m2, "create_dir": true}); w.Code != 422 {
		t.Errorf("second DM brain: %d, want 422", w.Code)
	}

	// A group that wrote while not allowed can be allowed and assigned in one go.
	_ = tb.store.RecordIgnored("group", "120363000000000001@g.us", "Book club", "15559876543", "")
	w = tb.do("POST", "/api/chats/allow", map[string]string{"jid": "120363000000000001@g.us", "kind": "group", "brain": "family"})
	if w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(tb.cfgs.Get().AllowedGroups, "120363000000000001@g.us") {
		t.Error("group not allowed")
	}
	if br := tb.brainMgr.BrainFor("120363000000000001@g.us", true); br == nil || br.ID() != "family" {
		t.Error("group not routed to the chosen brain")
	}
	if disk, _ := ReadManifest(dir); !slices.Contains(disk.Routing.Groups, "120363000000000001@g.us") {
		t.Error("the group must be saved in the brain's own brain.json")
	}
	if ign, _ := tb.store.ListIgnored(); len(ign) != 0 {
		t.Errorf("allowed chat still listed as ignored: %+v", ign)
	}

	// Message round trip in dev mode.
	w = tb.do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "text": "hello brain"})
	if w.Code != 202 {
		t.Fatalf("dev inbound: %d", w.Code)
	}
	waitFor(t, 5*time.Second, "reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	if out := tb.dev.Outbox()[0].Text; !strings.Contains(out, dir) || !strings.Contains(out, "hello brain") {
		t.Errorf("reply %q not from the family brain", out)
	}
	// The answer reaches the history, and the feed, once it went out.
	waitFor(t, 5*time.Second, "the answer in the activity feed", func() bool { return len(tb.activity.Recent(10, "family")) >= 2 })

	// Removing a brain unregisters it and leaves the folder alone.
	if w := tb.do("DELETE", "/api/brains/family", nil); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if tb.brainMgr.Get("family") != nil {
		t.Error("brain still active")
	}
	if _, err := os.Stat(filepath.Join(dir, "brain-prompt.md")); err != nil {
		t.Error("removing a brain must not delete its files")
	}
}

func TestApproveDrift(t *testing.T) {
	tb := newTestBridge(t, "")
	m := BrainManifest{ID: "notes", Name: "Notes"}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/notes", "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	edited := m
	edited.Name, edited.Routing.Default = "Notes 2", true
	_ = WriteManifest(filepath.Join(tb.home, "notes"), &edited, true)

	var views []brainView
	_ = json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views)
	if len(views) != 1 || views[0].Status != "drift" || views[0].Disk == nil {
		t.Fatalf("views = %+v", views)
	}
	if tb.brainMgr.Get("notes").Name() != "Notes" {
		t.Error("drift must not apply before approval")
	}
	if w := tb.do("POST", "/api/brains/notes/approve", map[string]any{"manifest": views[0].Disk}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if tb.brainMgr.Get("notes").Name() != "Notes 2" {
		t.Error("approval did not apply")
	}
}

// Saving a brain's settings writes its brain.json and the approved copy
// together, so the edit runs at once and shows no drift. An edit that
// changes the id, names no brain, is not valid on its own or clashes with
// another brain is refused and leaves config.json and every brain.json as
// they were.
func TestUpdateBrain(t *testing.T) {
	const group = "120363000000000000@g.us"
	tb := newTestBridge(t, "")
	dir := tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	dirB := tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B"})

	edited := BrainManifest{ID: "a", Name: "Assistant", Model: "opus", Routing: BrainRouting{HandlesDMs: true, Groups: []string{group}}}
	if w := tb.do("PUT", "/api/brains/a", map[string]any{"manifest": edited}); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	disk, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Name != "Assistant" || disk.Model != "opus" || !slices.Contains(disk.Routing.Groups, group) {
		t.Errorf("brain.json = %+v, want the edit", disk)
	}
	var approved *BrainManifest
	for _, ref := range tb.cfgs.Get().BrainDirs {
		if ref.Approved != nil && ref.Approved.ID == "a" {
			approved = ref.Approved
		}
	}
	if approved == nil || !approved.Equal(*disk) {
		t.Errorf("approved = %+v, want what brain.json has", approved)
	}
	if br := tb.brainMgr.BrainFor(group, true); br == nil || br.ID() != "a" || br.Name() != "Assistant" {
		t.Errorf("the group goes to %v, want the edited brain a", br)
	}
	if issues := tb.brainMgr.Issues(); len(issues) != 0 {
		t.Errorf("issues = %+v, want none", issues)
	}

	for _, c := range []struct {
		name, id string
		m        BrainManifest
		want     string
	}{
		{"id changed", "a", BrainManifest{ID: "x", Name: "X"}, "id cannot change"},
		{"no such brain", "nobody", BrainManifest{ID: "nobody", Name: "N"}, `no brain \"nobody\"`},
		{"not valid", "b", BrainManifest{ID: "b", Name: "B", Effort: "huge"}, "effort"},
		{"second DM brain", "b", BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{HandlesDMs: true}}, "both handle DMs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			etag := tb.cfgs.ETag()
			before := manifestFiles(t, dir, dirB)
			w := tb.do("PUT", "/api/brains/"+c.id, map[string]any{"manifest": c.m})
			if w.Code != 422 || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("%d %s, want 422 saying %s", w.Code, w.Body, c.want)
			}
			if tb.cfgs.ETag() != etag {
				t.Error("the refused edit changed config.json")
			}
			if !reflect.DeepEqual(manifestFiles(t, dir, dirB), before) {
				t.Error("the refused edit changed a brain.json")
			}
		})
	}
}

// manifestFiles returns the brain.json of each folder, as written.
func manifestFiles(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(d, ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(data))
	}
	return out
}

// Assigning a group to a brain, as Chats does when it allows one, moves it
// there: out of the brain.json and the approved copy of the brain that had
// it, into the chosen brain's, so the routing changes at once and neither
// shows drift. A brain that is not there is refused before any brain.json
// is written.
func TestAssignGroup(t *testing.T) {
	const group = "120363000000000000@g.us"
	cases := []struct {
		name, to string
		want     map[string][]string // brain → its groups after the request
		code     int
	}{
		{"to another brain", "b", map[string][]string{"a": {}, "b": {group}}, 200},
		{"to the brain that has it", "a", map[string][]string{"a": {group}, "b": {}}, 200},
		{"to no brain", "nobody", map[string][]string{"a": {group}, "b": {}}, 422},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			dirs := map[string]string{
				"a": tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{Groups: []string{group}}}),
				"b": tb.addFolderBrain(t, BrainManifest{ID: "b", Name: "B"}),
			}
			before := manifestFiles(t, dirs["a"], dirs["b"])
			w := tb.do("POST", "/api/chats/allow", map[string]string{"jid": group, "kind": "group", "brain": c.to})
			if w.Code != c.code {
				t.Fatalf("allow: %d %s, want %d", w.Code, w.Body, c.code)
			}
			if c.code != 200 {
				if !reflect.DeepEqual(manifestFiles(t, dirs["a"], dirs["b"]), before) {
					t.Error("the refused request changed a brain.json")
				}
				return
			}
			for id, want := range c.want {
				disk, err := ReadManifest(dirs[id])
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(disk.Routing.Groups, want) {
					t.Errorf("%s's brain.json has groups %q, want %q", id, disk.Routing.Groups, want)
				}
			}
			for _, ref := range tb.cfgs.Get().BrainDirs {
				if want := c.want[ref.Approved.ID]; !reflect.DeepEqual(ref.Approved.Routing.Groups, want) {
					t.Errorf("%s's approved groups are %q, want %q", ref.Approved.ID, ref.Approved.Routing.Groups, want)
				}
			}
			if br := tb.brainMgr.BrainFor(group, true); br == nil || br.ID() != c.to {
				t.Errorf("the group goes to %v, want %s", br, c.to)
			}
			if issues := tb.brainMgr.Issues(); len(issues) != 0 {
				t.Errorf("issues = %+v, want none", issues)
			}
		})
	}
}

func TestAllowListEntries(t *testing.T) {
	numbers := []struct {
		in, want string // want "" = refused
	}{
		{"15551234567", "15551234567"},
		{"+1 555 123 4567", "15551234567"},
		{"+1 (555) 123-4567", "15551234567"},
		{"1.555.123.4567", "15551234567"},
		{" 15551234567\u00a0", "15551234567"},
		{"15551234567@s.whatsapp.net", "15551234567"},
		{"12345", ""},
		{"1555123456789012", ""},
		{"1555abc4567", ""},
		{"15551234567:12@s.whatsapp.net", ""},
		{"15551234567@lid", ""}, // WhatsApp's id for the sender, not a number
		{"120363000000000000@g.us", ""},
		{"", ""},
		{"+", ""},
	}
	for _, c := range numbers {
		t.Run("number "+c.in, func(t *testing.T) {
			got, err := phoneNumber(c.in)
			if c.want == "" {
				if err == nil {
					t.Errorf("phoneNumber(%q) = %q, want an error", c.in, got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("phoneNumber(%q) = %q, %v; want %q", c.in, got, err, c.want)
			}
		})
	}
	groups := []struct {
		in, want string
	}{
		{"120363000000000000@g.us", "120363000000000000@g.us"},
		{" 120363000000000000@g.us ", "120363000000000000@g.us"},
		{"15551234567-1400000000@g.us", "15551234567-1400000000@g.us"}, // personal-data:ignore (an old-style group JID made of placeholders)
		{"120363000000000000@g.us/x", ""},
		{"x@g.us", ""},
		{"@g.us", ""},
		{"-1@g.us", ""},
		{"120363000000000000@s.whatsapp.net", ""},
		{"", ""},
	}
	for _, c := range groups {
		t.Run("group "+c.in, func(t *testing.T) {
			got, err := allowListGroup(c.in)
			if c.want == "" {
				if err == nil {
					t.Errorf("allowListGroup(%q) = %q, want an error", c.in, got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("allowListGroup(%q) = %q, %v; want %q", c.in, got, err, c.want)
			}
		})
	}
}

// FuzzPhoneNumber: whatever is typed into the dashboard's "Add number"
// box, what reaches allowed_numbers is 6 to 15 digits, all of them from
// the input, and it goes in unchanged a second time.
func FuzzPhoneNumber(f *testing.F) {
	for _, s := range []string{"15551234567", "+1 (555) 123-4567", "15551234567@s.whatsapp.net", "", "+", "1-2", "١٥٥٥١٢٣٤٥٦٧"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		n, err := phoneNumber(in)
		if err != nil {
			return
		}
		if !phoneNumberRE.MatchString(n) {
			t.Fatalf("phoneNumber(%q) = %q, not 6 to 15 digits", in, n)
		}
		digits := strings.Map(func(r rune) rune {
			if '0' <= r && r <= '9' {
				return r
			}
			return -1
		}, in)
		if !strings.Contains(digits, n) {
			t.Errorf("phoneNumber(%q) = %q, digits that are not in the input", in, n)
		}
		if again, err := phoneNumber(n); err != nil || again != n {
			t.Errorf("phoneNumber(%q) = %q, %v; want it unchanged", n, again, err)
		}
	})
}

func TestAllowAndRevokeChats(t *testing.T) {
	const group = "120363000000000001@g.us"
	cases := []struct {
		name    string
		extra   string // config fields that replace the test bridge's
		path    string
		body    map[string]string
		code    int
		numbers []string // allowed_numbers afterwards
		groups  []string // allowed_groups afterwards
	}{
		{"allow a typed number", "", "/api/chats/allow", map[string]string{"kind": "dm", "number": "+1 555 987-6543"}, 200,
			[]string{"15551234567", "15559876543"}, []string{"120363000000000000@g.us"}},
		{"allow a number twice", "", "/api/chats/allow", map[string]string{"kind": "dm", "number": "15551234567"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"refuse a number that is none", "", "/api/chats/allow", map[string]string{"kind": "dm", "number": "call me"}, 400,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"refuse a @lid JID without its number", "", "/api/chats/allow", map[string]string{"kind": "dm", "jid": "15559876543@lid"}, 400,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"allow a @lid JID's number", "", "/api/chats/allow", map[string]string{"kind": "dm", "jid": "15551234567@lid", "number": "15559876543"}, 200,
			[]string{"15551234567", "15559876543"}, []string{"120363000000000000@g.us"}},
		{"allow a typed group", "", "/api/chats/allow", map[string]string{"kind": "group", "jid": group}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us", group}},
		{"refuse a group that is none", "", "/api/chats/allow", map[string]string{"kind": "group", "jid": "120363000000000001@g.us/x"}, 400,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a number, written as typed", "", "/api/chats/revoke", map[string]string{"kind": "dm", "number": "+1 555 123 4567"}, 200,
			[]string{}, []string{"120363000000000000@g.us"}},
		{"revoke a group", "", "/api/chats/revoke", map[string]string{"kind": "group", "jid": "120363000000000000@g.us"}, 200,
			[]string{"15551234567"}, []string{}},
		{"revoke what is not there", "", "/api/chats/revoke", map[string]string{"kind": "group", "jid": group}, 404,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke what is not there and not a number", "", "/api/chats/revoke", map[string]string{"kind": "dm", "number": "call me"}, 404,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"refuse to revoke nothing", "", "/api/chats/revoke", map[string]string{"kind": "dm", "number": " "}, 400,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"refuse another kind", "", "/api/chats/revoke", map[string]string{"kind": "channel", "jid": group}, 400,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		// Entries written into config.json by hand, as the Chats page shows
		// and sends them.
		{"revoke a hand-written number as it is stored", `, "allowed_numbers": ["+15559876543", "15551234567"]`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "+15559876543"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a group stored with a space", `, "allowed_groups": [" ` + group + `", "120363000000000000@g.us"]`,
			"/api/chats/revoke", map[string]string{"kind": "group", "jid": " " + group}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a number the bridge would refuse", `, "allowed_numbers": ["12345", "15551234567"]`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "12345"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a group the bridge would refuse", `, "allowed_groups": ["family@g.us", "120363000000000000@g.us"]`,
			"/api/chats/revoke", map[string]string{"kind": "group", "jid": "family@g.us"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke every form of a number typed another way", `, "allowed_numbers": ["+15551234567", "15559876543", "1 555 123 4567"]`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "15551234567"}, 200,
			[]string{"15559876543"}, []string{"120363000000000000@g.us"}},
		{"revoke only the stored form that was sent", `, "allowed_numbers": ["+15551234567", "15551234567"]`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "+15551234567"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, c.extra)
			w := tb.do("POST", c.path, c.body)
			if w.Code != c.code {
				t.Fatalf("%s: %d %s, want %d", c.path, w.Code, w.Body, c.code)
			}
			f := tb.cfgs.FileCopy()
			if !slices.Equal(f.AllowedNumbers, c.numbers) {
				t.Errorf("allowed_numbers = %q, want %q", f.AllowedNumbers, c.numbers)
			}
			if !slices.Equal(f.AllowedGroups, c.groups) {
				t.Errorf("allowed_groups = %q, want %q", f.AllowedGroups, c.groups)
			}
		})
	}
}

func TestRevokedGroupKeepsItsBrain(t *testing.T) {
	const group = "120363000000000000@g.us"
	tb := newTestBridge(t, "")
	dir := tb.addFolderBrain(t, BrainManifest{ID: "family", Name: "Family", Routing: BrainRouting{Groups: []string{group}}})
	if w := tb.do("POST", "/api/chats/revoke", map[string]string{"kind": "group", "jid": group}); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if disk, _ := ReadManifest(dir); !slices.Contains(disk.Routing.Groups, group) {
		t.Error("revoking the group took it out of the brain's routing")
	}
	if w := tb.do("POST", "/api/chats/allow", map[string]string{"kind": "group", "jid": group}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body)
	}
	if br := tb.brainMgr.BrainFor(group, true); br == nil || br.ID() != "family" {
		t.Errorf("allowed again, the group goes to %v, want family", br)
	}
}

func TestAllowingATypedChatClearsItsWaitingChat(t *testing.T) {
	cases := []struct {
		name string
		kind string
		jid  string // the waiting chat
		body map[string]string
	}{
		{"a number", "dm", "15559876543@s.whatsapp.net", map[string]string{"kind": "dm", "number": "+1 555 987 6543"}},
		{"a group with spaces around it", "group", "120363000000000001@g.us", map[string]string{"kind": "group", "jid": " 120363000000000001@g.us "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			_ = tb.store.RecordIgnored(c.kind, c.jid, "", "15559876543", "")
			if w := tb.do("POST", "/api/chats/allow", c.body); w.Code != 200 {
				t.Fatalf("allow: %d %s", w.Code, w.Body)
			}
			if ign, _ := tb.store.ListIgnored(); len(ign) != 0 {
				t.Errorf("the allowed chat still waits: %+v", ign)
			}
		})
	}
}

// lockedBuffer collects log output that the bridge's goroutines may write
// while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestBridgeStartupWarnsWhenAnyoneCanDM(t *testing.T) {
	var logs lockedBuffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logs)
	// A later "allowed_numbers" overrides the helper's own.
	newTestBridge(t, `, "allowed_numbers": [], "allow_any_dm": true`)
	if !strings.Contains(logs.String(), "WARNING: anyone") {
		t.Errorf("startup log has no warning: %q", logs.String())
	}
}

func TestEmptyingTheAllowlistWarnsOnlyWithAllowAnyDM(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		open  bool
	}{
		{"flag unset", "", false},
		{"explicit false", `, "allow_any_dm": false`, false},
		{"explicit true", `, "allow_any_dm": true`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestBridge(t, tc.extra)
			var logs lockedBuffer
			defer log.SetOutput(log.Writer())
			log.SetOutput(&logs)
			// What the dashboard does when you remove the last number.
			if _, err := tb.cfgs.Update("", func(c *Config) error { c.AllowedNumbers = []string{}; return nil }); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "WARNING: anyone"); got != tc.open {
				t.Errorf("log warns = %v, want %v (log: %q)", got, tc.open, logs.String())
			}
			var st struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(strings.Join(st.Warnings, "\n"), "anyone"); got != tc.open {
				t.Errorf("dashboard warns = %v, want %v (warnings: %q)", got, tc.open, st.Warnings)
			}
		})
	}
}

func TestAPINeverReturnsSecrets(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-"+"ant-oat01-very-secret-value")
	tb := newTestBridge(t, "")
	for _, p := range []string{"/api/status", "/api/config", "/api/brains", "/api/logs", "/api/backup/status"} {
		body := tb.do("GET", p, nil).Body.String()
		if strings.Contains(body, "very-secret-value") || strings.Contains(body, tb.token.Value()) {
			t.Errorf("%s leaks a secret", p)
		}
	}
}

func TestGetConfigLocksNoSettings(t *testing.T) {
	// Environment variables used to override config.json, and the response
	// named the settings they locked, so the dashboard could disable them.
	// Now the file is all there is: what it says is what runs.
	t.Setenv("ALLOWED_GROUPS", "120363000000000001@g.us")
	tb := newTestBridge(t, "")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(got))
	if want := []string{"config", "effective", "etag", "path"}; !slices.Equal(keys, want) {
		t.Errorf("GET /api/config has %v, want %v", keys, want)
	}
	var eff Config
	if err := json.Unmarshal(got["effective"], &eff); err != nil {
		t.Fatal(err)
	}
	if want := []string{"120363000000000000@g.us"}; !slices.Equal(eff.AllowedGroups, want) {
		t.Errorf("effective allowed_groups = %v, want config.json's %v", eff.AllowedGroups, want)
	}
}

func TestPutConfigKeepsBrainsAndReportsRestart(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P"})
	var got struct {
		ETag   string          `json:"etag"`
		Config json.RawMessage `json:"config"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &got)
	var c map[string]any
	_ = json.Unmarshal(got.Config, &c)
	c["log_level"] = "debug"
	c["triggers"] = []any{"@claude", map[string]string{"text": "@c", "match": "start"}}
	w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": c})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "log_level") {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if len(tb.cfgs.FileCopy().BrainDirs) != 1 {
		t.Error("saving settings must not drop brains")
	}
	if !tb.cfgs.Get().Triggers.Matches("@c hi") || tb.cfgs.Get().Triggers.Matches("@carla") {
		t.Error("trigger change not applied live")
	}
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": c}); w.Code != 409 {
		t.Errorf("stale etag: %d, want 409", w.Code)
	}
}

// A setting read only at startup stays pending in /api/status until the
// restart, so the dashboard's restart banner survives a reload and shows in
// every tab. Set back to the value the bridge started with, it is not
// pending any more.
func TestStatusReportsSettingsWaitingForRestart(t *testing.T) {
	tb := newTestBridge(t, "")
	status := func(t *testing.T) []string {
		t.Helper()
		var st struct {
			RestartRequired []string `json:"restart_required"`
		}
		if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil || st.RestartRequired == nil {
			t.Fatalf("status restart_required = %v (%v), want a list", st.RestartRequired, err)
		}
		return st.RestartRequired
	}
	if got := status(t); len(got) != 0 {
		t.Fatalf("at start: restart_required = %v, want none", got)
	}
	steps := []struct {
		name, logLevel string
		want           []string
	}{
		{"changed", "debug", []string{"log_level"}},
		{"changed again", "warn", []string{"log_level"}},
		{"back to the startup value", "info", []string{}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var cur struct {
				ETag   string         `json:"etag"`
				Config map[string]any `json:"config"`
			}
			_ = json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &cur)
			cur.Config["log_level"] = s.logLevel
			w := tb.do("PUT", "/api/config", map[string]any{"etag": cur.ETag, "config": cur.Config})
			var put struct {
				RestartRequired []string `json:"restart_required"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &put); w.Code != 200 || err != nil {
				t.Fatalf("put: %d %s", w.Code, w.Body.String())
			}
			if !slices.Equal(put.RestartRequired, s.want) {
				t.Errorf("PUT restart_required = %v, want %v", put.RestartRequired, s.want)
			}
			if got := status(t); !slices.Equal(got, s.want) {
				t.Errorf("status restart_required = %v, want %v", got, s.want)
			}
		})
	}
}

// The settings page sends brain_mode back as it got it, true. A request
// that turns it off is refused, not saved: the next start would fail on it.
func TestPutConfigRefusesClassicMode(t *testing.T) {
	tb := newTestBridge(t, "")
	var got struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &got)
	if got.Config["brain_mode"] != true {
		t.Errorf("GET /api/config: brain_mode = %v, want true", got.Config["brain_mode"])
	}
	got.Config["brain_mode"] = false
	w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": got.Config})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "classic mode, which was removed") {
		t.Errorf("put brain_mode false: %d %s, want 400 saying classic mode was removed", w.Code, w.Body.String())
	}
	if tb.cfgs.ETag() != got.ETag {
		t.Error("the refused request changed config.json")
	}
}

// Inline brains were removed. A dashboard save that brings some back is
// refused like a config.json that still has them, and the config the
// dashboard loads has no brains key to send back.
func TestPutConfigRefusesInlineBrains(t *testing.T) {
	tb := newTestBridge(t, "")
	var got struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &got)
	if _, ok := got.Config["brains"]; ok {
		t.Errorf("GET /api/config has a brains key: %v", got.Config["brains"])
	}
	got.Config["brains"] = []map[string]any{{"name": "a", "work_dir": "~/a"}}
	w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": got.Config})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no longer runs") {
		t.Errorf("put inline brains: %d %s, want 400 saying they no longer run", w.Code, w.Body.String())
	}
	if tb.cfgs.ETag() != got.ETag {
		t.Error("the refused request changed config.json")
	}
}

// Allowing a group for a brain takes it away from the brain that had it,
// in both brain.json files, and leaves that brain's other groups alone.
func TestAllowGroupMovesItBetweenBrains(t *testing.T) {
	const moved, kept = "120363000000000001@g.us", "120363000000000002@g.us"
	tb := newTestBridge(t, `, "allowed_groups": ["`+moved+`", "`+kept+`"]`)
	fromDir := tb.addFolderBrain(t, BrainManifest{ID: "from", Name: "From", Routing: BrainRouting{Groups: []string{kept, moved}}})
	toDir := tb.addFolderBrain(t, BrainManifest{ID: "to", Name: "To"})

	if w := tb.do("POST", "/api/chats/allow", map[string]string{"jid": moved, "kind": "group", "brain": "to"}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	if from, _ := ReadManifest(fromDir); !slices.Equal(from.Routing.Groups, []string{kept}) {
		t.Errorf("from has groups %q, want only %q", from.Routing.Groups, kept)
	}
	if to, _ := ReadManifest(toDir); !slices.Equal(to.Routing.Groups, []string{moved}) {
		t.Errorf("to has groups %q, want %q", to.Routing.Groups, moved)
	}
	if br := tb.brainMgr.BrainFor(moved, true); br == nil || br.ID() != "to" {
		t.Errorf("the group routes to %v, want to", br)
	}
	if br := tb.brainMgr.BrainFor(kept, true); br == nil || br.ID() != "from" {
		t.Errorf("the other group routes to %v, want from", br)
	}
}

// The name suggested for a new brain's folder starts with a capital, also
// when its first letter takes more than one byte.
func TestPrettyName(t *testing.T) {
	for in, want := range map[string]string{
		"":             "Brain",
		"personal":     "Personal",
		"work_notes":   "Work notes",
		"éclair-notes": "Éclair notes",
		"ölkanne":      "Ölkanne",
		"日記":           "日記",
	} {
		if got := prettyName(in); got != want {
			t.Errorf("prettyName(%q) = %q, want %q", in, got, want)
		}
	}
}

// qrMessenger is the dev messenger with a pairing code to show.
type qrMessenger struct {
	*devMessenger
	code string
}

func (q qrMessenger) State() WAState { return WAState{Mode: "pairing", QRCode: q.code} }

// The pairing code is served as a PNG, square and big enough to scan once
// the dashboard scales it, and only while there is a code.
func TestQRCodeIsServedAsAPNG(t *testing.T) {
	tb := newTestBridge(t, "")
	if w := tb.do("GET", "/api/whatsapp/qr", nil); w.Code != http.StatusNotFound {
		t.Errorf("without a code: %d, want 404", w.Code)
	}
	// As long as the codes whatsmeow hands out.
	tb.wa = qrMessenger{tb.dev, "2@" + strings.Repeat("AbCdEfGh", 20) + ",x+y/z=,1234567890abcdef=="}
	w := tb.do("GET", "/api/whatsapp/qr", nil)
	var body struct {
		DataURL string `json:"data_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	b64, ok := strings.CutPrefix(body.DataURL, "data:image/png;base64,")
	if !ok {
		t.Fatalf("data_url %q is not a base64 PNG", truncate(body.DataURL, 40))
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != b.Dy() || b.Dx() < 200 {
		t.Errorf("the PNG is %dx%d, want a square of at least 200 pixels", b.Dx(), b.Dy())
	}
}

// The dashboard edits brain_model only. A config that still has the
// retired model key shows its value there, and a save from a tab opened
// before the upgrade, which still sends model, keeps it the same way.
func TestConfigPageFoldsTheRetiredModel(t *testing.T) {
	tb := newTestBridge(t, `, "model": "sonnet"`)
	var got struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &got)
	if _, ok := got.Config["model"]; ok {
		t.Errorf("GET /api/config has a model key: %v", got.Config["model"])
	}
	if got.Config["brain_model"] != "sonnet" {
		t.Errorf("GET /api/config: brain_model %v, want sonnet", got.Config["brain_model"])
	}

	// An old tab: brain_model as an older bridge served it, and model.
	got.Config["brain_model"], got.Config["model"] = "", "haiku"
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": got.Config}); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if m := tb.cfgs.Get().BrainModel; m != "haiku" {
		t.Errorf("brain_model after the save = %q, want haiku", m)
	}
	if raw, ok := readRawConfig(t, tb.cfgs.Path())["model"]; ok {
		t.Errorf("the save wrote model: %s", raw)
	}
}

func TestAddBrainBackupDefaults(t *testing.T) {
	tb := newTestBridge(t, "")
	// No backup settings given: backed up, with the default exclusions.
	_ = tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": map[string]any{"id": "a", "name": "A"}})
	// Explicitly off stays off.
	off := BrainManifest{ID: "b", Name: "B", Backup: BrainBackup{Enabled: false, Exclude: []string{"*.log"}}}
	_ = tb.do("POST", "/api/brains", map[string]any{"path": "~/b", "create_dir": true, "manifest": off})
	// Off without an exclude list stays off too, with the default
	// exclusions should it be turned on.
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/c", "create_dir": true, "manifest": map[string]any{"id": "c", "name": "C", "backup": map[string]any{"enabled": false}}}); w.Code != 200 {
		t.Fatalf("add c: %d %s", w.Code, w.Body.String())
	}
	a, _ := ReadManifest(filepath.Join(tb.home, "a"))
	b, _ := ReadManifest(filepath.Join(tb.home, "b"))
	c, _ := ReadManifest(filepath.Join(tb.home, "c"))
	if !a.Backup.Enabled || len(a.Backup.Exclude) == 0 {
		t.Errorf("a.backup = %+v, want on with defaults", a.Backup)
	}
	if b.Backup.Enabled || len(b.Backup.Exclude) != 1 {
		t.Errorf("b.backup = %+v, want off as requested", b.Backup)
	}
	if c.Backup.Enabled || !slices.Equal(c.Backup.Exclude, DefaultBackupExcludes) {
		t.Errorf("c.backup = %+v, want off with the default exclusions", c.Backup)
	}
}

func TestOTPGuardHandsCodeToLocalScripts(t *testing.T) {
	tb := newTestBridge(t, `, "otp_guard": true, "otp_markers": ["Examplebank 2FA"]`)
	dir := tb.addFolderBrain(t, BrainManifest{ID: "p", Name: "P", Routing: BrainRouting{HandlesDMs: true}})
	// A local script asks for a code through /send...
	r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"15551234567@s.whatsapp.net","text":"Examplebank 2FA: reply with the code"}`))
	r.Host = "127.0.0.1:8765"
	tb.h.ServeHTTP(httptest.NewRecorder(), r)
	// ...and the reply arrives.
	if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "text": " 483920 "}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	asked := time.Now().UnixMilli()
	otp := filepath.Join(tb.cfgs.Get().DataDir, "otp", "p.json")
	waitFor(t, 3*time.Second, "otp file", func() bool { _, err := os.Stat(otp); return err == nil })
	data, _ := os.ReadFile(otp)
	var got struct {
		Brain, Chat, Sender, Code, Received string
		ReceivedUnixMS                      int64 `json:"received_unix_ms"`
	}
	_ = json.Unmarshal(data, &got)
	if got.Code != "483920" || got.Chat != "15551234567@s.whatsapp.net" || got.Brain != "p" || got.Sender == "" || got.Received == "" {
		t.Errorf("otp file = %s", data)
	}
	// What a script compares with the time it asked: no time zone needed.
	if got.ReceivedUnixMS < asked-5000 || got.ReceivedUnixMS > time.Now().UnixMilli() {
		t.Errorf("received_unix_ms = %d, want a time around %d", got.ReceivedUnixMS, asked)
	}
	if st, _ := os.Stat(otp); st.Mode().Perm() != 0o600 {
		t.Errorf("otp file mode %v", st.Mode().Perm())
	}
	hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
	if strings.Contains(string(hist), "483920") {
		t.Error("the code must not be written to the conversation history")
	}
	time.Sleep(200 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 1 || !strings.Contains(out[0].Text, "Examplebank 2FA") {
		t.Errorf("outbox = %+v — only the script's own request may be there; the brain must not run on a caught code", out)
	}
}
