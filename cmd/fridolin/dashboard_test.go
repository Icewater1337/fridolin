package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/httpapi"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"github.com/Icewater1337/fridolin/internal/otp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/textx"
	"github.com/Icewater1337/fridolin/internal/version"
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
	home := fsx.Canonical(t.TempDir())
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
	cfgs, err := openConfigStore(path)
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
func (tb *testBridge) addFolderBrain(t *testing.T, m manifest.Manifest) string {
	t.Helper()
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/" + m.ID, "manifest": m, "create_dir": true}); w.Code != 200 {
		t.Fatalf("add brain %s: %d %s", m.ID, w.Code, w.Body.String())
	}
	return filepath.Join(tb.home, m.ID)
}

func TestDashboardGuards(t *testing.T) {
	h := newHarness(t, "")
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
		if w := h.Do(c.method, c.path, body, muts...); w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}

	// The page itself loads with the strict CSP.
	w := h.Do("GET", "/", nil, func(r *http.Request) { r.Header.Del("X-Bridge-Token") })
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("index: %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
}

// Every answer carries the dashboard's CSP: scripts and styles from the
// bridge only, no plugins, and Trusted Types without a policy, so no
// string is ever parsed into the page as HTML or run as script.
func TestDashboardCSP(t *testing.T) {
	tb := newTestBridge(t, "")
	want := map[string]string{
		"default-src":               "'self'",
		"script-src":                "'self'",
		"style-src":                 "'self'",
		"object-src":                "'none'",
		"base-uri":                  "'none'",
		"form-action":               "'none'",
		"frame-ancestors":           "'none'",
		"require-trusted-types-for": "'script'",
		"trusted-types":             "'none'",
	}
	for _, path := range []string{"/", "/assets/app/main.js", "/api/status", "/api/nothing-here"} {
		t.Run(path, func(t *testing.T) {
			csp := tb.do("GET", path, nil).Header().Get("Content-Security-Policy")
			got := map[string]string{}
			for _, d := range strings.Split(csp, ";") {
				name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
				got[name] = value
			}
			for name, value := range want {
				if got[name] != value {
					t.Errorf("%s is %q, want %q (CSP %q)", name, got[name], value, csp)
				}
			}
		})
	}
}

// The page's modules are served as JavaScript, which browsers require of
// module scripts. The JavaScript tests and the package.json that marks the
// modules for node sit in web/ too, but are not part of the page.
func TestDashboardServesOnlyThePage(t *testing.T) {
	h := newHarness(t, "")
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
			w := h.Do("GET", c.path, nil)
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
	h := newHarness(t, "")
	get := func(path string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		return h.Do("GET", path, nil, append([]func(*http.Request){func(r *http.Request) { r.Header.Del("X-Bridge-Token") }}, mutate...)...)
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
		name, config string
		want         bool
	}{
		{"off", "", false},
		{"on", `{"inherit_claude_user_config": true}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.config)
			w := h.Do("GET", "/api/status", nil)
			var st struct {
				Warnings []string `json:"warnings"`
			}
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
	h := newHarness(t, "")
	send := func(mutate ...func(*http.Request)) int {
		return h.Script("POST", "/send", `{"jid":"15551234567@s.whatsapp.net","text":"hello"}`, mutate...).Code
	}
	if code := send(); code != 200 {
		t.Errorf("script /send: %d, want 200 without any token", code)
	}
	if code := send(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }); code != 403 {
		t.Errorf("cross-site /send: %d, want 403", code)
	}
	if out := h.Outbox(); len(out) != 1 || out[0].Text != "hello" {
		t.Errorf("outbox = %+v", out)
	}
}

// POST /send refuses Status, broadcast lists and channels with a JSON
// error: a script must not be able to post a Status in the bridge's name.
func TestHTTPSendRefusesStatusBroadcastAndChannels(t *testing.T) {
	h := newHarness(t, "")
	for _, jid := range []string{"status@broadcast", "1700000000@broadcast", "120363000000000000@newsletter"} {
		t.Run(jid, func(t *testing.T) {
			body, _ := json.Marshal(sendRequest{JID: jid, Text: "hello"})
			w := h.Script("POST", "/send", string(body))
			var resp map[string]string
			if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") != "application/json" ||
				json.Unmarshal(w.Body.Bytes(), &resp) != nil || !strings.Contains(resp["error"], "refusing to send") {
				t.Errorf("status %d, %s %q; want 400 with a JSON error", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		})
	}
	if out := h.Outbox(); len(out) != 0 {
		t.Errorf("sent anyway: %+v", out)
	}
}

func TestSendRefusesMissingJIDOrText(t *testing.T) {
	// Blank text gives no chunks, so it used to send nothing while still
	// answering "sent" and writing the blank text to the brain's history.
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true}})
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
			if w := h.Script("POST", "/send", c.body); w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
	if out := h.Outbox(); len(out) != 0 {
		t.Errorf("outbox = %+v, want nothing sent", out)
	}
	if hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl")); len(hist) != 0 {
		t.Errorf("history = %s, want nothing recorded", hist)
	}
}

// stateMessenger is the dev messenger with a WhatsApp state of the test's
// choosing.
type stateMessenger struct {
	*devchat.Messenger
	state chat.LinkState
}

func (m stateMessenger) State() chat.LinkState { return m.state }

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
				tb.wa = stateMessenger{Messenger: tb.dev, state: chat.LinkState{Mode: c.mode, OwnNumber: "15551234567"}}
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
			var resp httpapi.ReadyResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != want {
				t.Fatalf("status %d (%v) %s, want %d with JSON", w.Code, err, w.Body.String(), want)
			}
			if resp.Ready != c.ready || resp.WhatsApp != c.mode || resp.Version != version.String() || !resp.StartedAt.Equal(tb.started) {
				t.Errorf("body %+v, want ready=%v whatsapp=%q version=%q started_at=%v", resp, c.ready, c.mode, version.String(), tb.started)
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
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	dir := h.AddBrain(manifest.Manifest{ID: "personal", Name: "Personal", Routing: manifest.Routing{HandlesDMs: true}})
	media := filepath.Join(h.DataDir(), "media")
	inside := filepath.Join(media, "scan.pdf")
	_ = os.MkdirAll(filepath.Dir(inside), 0o700)
	_ = os.WriteFile(inside, []byte("%PDF"), 0o600)
	outside := filepath.Join(h.Home(), "secret.txt")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	link := filepath.Join(media, "link.txt")
	_ = os.Symlink(outside, link)

	ingest := func(body string) *httptest.ResponseRecorder { return h.Script("POST", "/ingest", body) }
	w := ingest(`{"brain":"personal","paths":["` + inside + `"],"text":"file this"}`)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"accepted"`) || !strings.Contains(w.Body.String(), "15551234567@s.whatsapp.net") {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"outside media": `{"brain":"personal","paths":["` + outside + `"]}`,
		"symlink out":   `{"brain":"personal","paths":["` + link + `"]}`,
		"relative":      `{"brain":"personal","paths":["scan.pdf"]}`,
		"unknown brain": `{"brain":"nobody","paths":["` + inside + `"]}`,
	} {
		if w := ingest(body); w.Code != 400 {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	h.WaitFor("ingest reply", func() bool { return len(h.Outbox()) > 0 })
	// The file moved into the brain's inbox, where its sandbox lets it read.
	inbox := filepath.Join(fsx.Canonical(media), "personal", "scan.pdf")
	hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
	if !strings.Contains(string(hist), "[attachment: "+inbox+"]") {
		t.Errorf("history does not record the ingested file in the inbox:\n%s", hist)
	}
	if _, err := os.Stat(inbox); err != nil {
		t.Errorf("the file is not in the inbox: %v", err)
	}
}

// TestIngestKeepsOtherBrainsFiles: the brain /ingest names takes the files
// into its inbox, and a brain can ask for that itself (it can reach
// /ingest), so a file it may take must be one handed in for it: at the top
// of the media folder, or in its own inbox. It used to take a file from any
// folder but a running brain's inbox: the inbox a removed brain or one left
// out (its folder gone) had left, or media/plaud/, where older versions
// kept every brain's Plaud transcripts. Nor may it take another brain's
// files, or an attachment still on its way to the brain of its chat.
func TestIngestKeepsOtherBrainsFiles(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}`)
	tb.addFolderBrain(t, manifest.Manifest{ID: "personal", Name: "Personal", Routing: manifest.Routing{HandlesDMs: true}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "team", Name: "Team", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "old", Name: "Old"})
	left := tb.addFolderBrain(t, manifest.Manifest{ID: "left", Name: "Left"})
	media := tb.cfgs.Get().MediaDir
	for _, p := range []string{"old/dm.pdf", "left/dm.pdf"} {
		writeTestFile(t, filepath.Join(media, p), "x")
	}
	if w := tb.do("DELETE", "/api/brains/old", nil); w.Code != 200 {
		t.Fatalf("removing brain old: %d %s", w.Code, w.Body.String())
	}
	if err := os.RemoveAll(left); err != nil {
		t.Fatal(err)
	}
	if err := tb.brainMgr.Apply(tb.cfgs.Get()); err != nil {
		t.Fatal(err)
	}
	if tb.brainMgr.Get("old") != nil || tb.brainMgr.Get("left") != nil || tb.brainMgr.Get("team") == nil {
		t.Fatalf("brains %v, want old and left gone and team running", tb.brainMgr.Brains())
	}
	// team's own run token may hand in only what is in its inbox: it cannot
	// write the top of the media folder, so a file there, left by a script
	// for another brain, was never its own. It used to claim any of them.
	tok, revoke := tb.runTokens.mint("team")
	t.Cleanup(revoke)
	cases := []struct {
		name, file string // file relative to the media folder
		code       int    // with the scripts token
		brainCode  int    // with team's own run token
	}{
		{"another brain's inbox", "personal/dm.pdf", 400, 400},
		{"a removed brain's inbox", "old/dm.pdf", 400, 400},
		{"the inbox of a brain left out", "left/dm.pdf", 400, 400},
		{"Plaud transcripts older versions kept", "plaud/2026-01-15 standup.txt", 400, 400},
		{"a folder no brain owns", "upload/scan.pdf", 400, 400},
		{"an attachment not yet routed", mediafiles.IncomingDir + "/voice.ogg", 400, 400},
		{"its own inbox", "team/notes.pdf", 202, 202},
		{"a folder in its own inbox", "team/plaud/notes.txt", 202, 202},
		{"the top of the media folder", "scan.pdf", 202, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, by := range []struct {
				name string
				code int
				do   func(body string) *httptest.ResponseRecorder
			}{
				{"brain team", tc.brainCode, func(body string) *httptest.ResponseRecorder { return tb.script("POST", "/ingest", body, tok) }},
				{"a script", tc.code, tb.ingest},
			} {
				p := writeTestFile(t, filepath.Join(media, tc.file), "x")
				w := by.do(`{"brain":"team","paths":["` + p + `"]}`)
				if w.Code != by.code {
					t.Errorf("ingest of %s by %s: %d %s, want %d", tc.file, by.name, w.Code, w.Body.String(), by.code)
				}
				if by.code == 400 {
					if _, err := os.Stat(p); err != nil {
						t.Errorf("the file refused to %s moved: %v", by.name, err)
					}
				}
			}
		})
	}
	// Nor does the answer say whether a name it guessed is there.
	there := writeTestFile(t, filepath.Join(media, "payroll.pdf"), "x")
	answer := func(p string) string {
		w := tb.script("POST", "/ingest", `{"brain":"team","paths":["`+p+`"]}`, tok)
		return fmt.Sprint(w.Code, " ", w.Body.String())
	}
	if a, b := answer(there), answer(filepath.Join(media, "nothing.pdf")); a != b {
		t.Errorf("brain team is told %q for a file there and %q for none", a, b)
	}
	// Nor can it get past that with a symlink in its inbox: the sandbox
	// lets it make one to a file it cannot read, and /ingest used to
	// follow it, and move payroll.pdf into the inbox for it.
	inbox := filepath.Join(media, "team")
	writeTestFile(t, filepath.Join(inbox, "real.pdf"), "x")
	if err := os.MkdirAll(filepath.Join(inbox, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"there.pdf":     "../payroll.pdf",
		"none.pdf":      "../nothing.pdf",
		"abs-there.pdf": there,
		"abs-none.pdf":  filepath.Join(media, "nothing.pdf"),
		"other.pdf":     "../personal/dm.pdf",
		"own.pdf":       "real.pdf",
		"sub/up.pdf":    "../real.pdf",
	} {
		if err := os.Symlink(target, filepath.Join(inbox, name)); err != nil {
			t.Fatal(err)
		}
	}
	refused := answer(filepath.Join(inbox, "nothing.pdf"))
	for _, c := range []struct{ name, path, want string }{
		{"a link to a file at the top", filepath.Join(inbox, "there.pdf"), refused},
		{"a link to no file at the top", filepath.Join(inbox, "none.pdf"), refused},
		{"an absolute link to a file at the top", filepath.Join(inbox, "abs-there.pdf"), refused},
		{"an absolute link to no file at the top", filepath.Join(inbox, "abs-none.pdf"), refused},
		{"a link into another brain's inbox", filepath.Join(inbox, "other.pdf"), refused},
		{"a path through a folder that is there", inbox + "/../personal/../team/real.pdf", ""},
		{"a path through a folder that is not there", inbox + "/../nobody/../team/real.pdf", ""},
		{"a link to a file in its inbox", filepath.Join(inbox, "own.pdf"), ""},
		{"a link in a folder to a file in its inbox", filepath.Join(inbox, "sub", "up.pdf"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := answer(c.path)
			if c.want == "" {
				if !strings.HasPrefix(got, "202 ") {
					t.Errorf("brain team is told %q, want 202", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("brain team is told %q, want %q as for any file it may not hand in", got, c.want)
			}
		})
	}
	if _, err := os.Stat(there); err != nil {
		t.Errorf("payroll.pdf, which brain team may not take, moved: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(inbox, "payroll.pdf")); err == nil {
		t.Error("payroll.pdf reached team's inbox")
	}
}

// An unknown brain is named as it was sent, in Go quotes.
func TestUnknownBrainIsQuotedAsSent(t *testing.T) {
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	const want = `unknown brain "<a&b>"`
	var body struct{ Error string }
	w := h.Script("POST", "/ingest", `{"brain":"<a&b>","text":"x"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || body.Error != want {
		t.Errorf("ingest: %d %q, want 400 saying %s", w.Code, body.Error, want)
	}
	w = h.Do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "brain": "<a&b>", "text": "x"})
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
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	dir := h.AddBrain(manifest.Manifest{ID: "personal", Name: "Personal", Routing: manifest.Routing{HandlesDMs: true}})
	for name, body := range map[string]string{
		"scope only":         `{"scope":"personal","text":"file this"}`,
		"scope and brain":    `{"scope":"personal","brain":"personal","text":"file this"}`,
		"scope, other brain": `{"scope":"nobody","brain":"personal","text":"file this"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := h.Script("POST", "/ingest", body)
			var got struct{ Error string }
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != http.StatusBadRequest || !strings.Contains(got.Error, `name the brain in "brain"`) {
				t.Errorf("%d %q, want 400 naming the brain field", w.Code, w.Body.String())
			}
		})
	}
	if w := h.Script("POST", "/ingest", `{"brain":"personal","text":"file this"}`); w.Code != http.StatusAccepted {
		t.Fatalf("with brain: %d %s, want 202", w.Code, w.Body.String())
	}
	h.WaitFor("the reply to the one accepted request", func() bool { return len(h.Outbox()) > 0 })
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
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	dmDir := h.AddBrain(manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	upDir := h.AddBrain(manifest.Manifest{ID: "up", Name: "Uploads", ReplyChat: dm})

	w := h.Script("POST", "/ingest", `{"brain":"up","text":"file this"}`)
	if w.Code != 202 || !strings.Contains(w.Body.String(), dm) {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("ingest reply", func() bool { return len(h.Outbox()) > 0 })
	out := h.Outbox()[0]
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

// What /ingest hands a brain comes from a program, not from the chat that
// asked for a code: the OTP guard leaves it alone, and the brain reads it
// like any other message. Otherwise a brain could plant a code for the
// script that waits for one, through its own /ingest.
func TestIngestedCodeIsNotCaught(t *testing.T) {
	const dm = "15551234567@s.whatsapp.net"
	tb := newTestBridge(t, `, "ingest": {"enabled": true}, "otp_guard": true, "otp_markers": ["Examplebank 2FA"]`)
	tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	upDir := tb.addFolderBrain(t, manifest.Manifest{ID: "up", Name: "Uploads", ReplyChat: dm})
	tb.brainMgr.Get("up").AppendHistory(dm, "up", "Examplebank 2FA: reply with the code", history.Out)

	if w := tb.ingest(`{"brain":"up","text":"483920"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 5*time.Second, "up's answer", func() bool { return tb.sentPrefix(dm, "echo:") })
	if hist, _ := os.ReadFile(filepath.Join(upDir, "conversation-history.jsonl")); !strings.Contains(string(hist), `"text":"483920","type":"in"`) {
		t.Errorf("up's history lacks the ingested text:\n%s", hist)
	}
	if _, err := os.Stat(filepath.Join(tb.cfgs.Get().DataDir, "otp", "up.json")); !os.IsNotExist(err) {
		t.Errorf("the ingested text was handed over as a code (%v)", err)
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
			tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
			tb.addFolderBrain(t, manifest.Manifest{ID: "up", Name: "Uploads", ReplyChat: dm})
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
			up := tb.brainMgr.Get("up")

			if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
			testutil.WaitFor(t, 5*time.Second, "up's run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
			if w := tb.ingest(`{"brain":"up","text":"and this"}`); w.Code != 202 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
			testutil.WaitFor(t, 5*time.Second, "the second file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

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
			testutil.WaitFor(t, 15*time.Second, "the answer to !stop", func() bool { return answer() != "" })
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
	tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "up", Name: "Uploads", ReplyChat: dm})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	up := tb.brainMgr.Get("up")

	if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 5*time.Second, "up's run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
	if w := tb.ingest(`{"brain":"up","text":"and this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 5*time.Second, "the second file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

	tb.handleInbound(context.Background(), brainruntime.Inbound{Inbound: chat.Inbound{JID: dm, Sender: "15551234567", Text: "!reset", Source: chat.SourceWhatsApp}})
	if !tb.sentPrefix(dm, "✅ Reset. I stopped what this chat had running or waiting") {
		t.Errorf("!reset answered %q, want that it cleared the chat", tb.sentTo(dm))
	}
	if rt := up.Snapshot(); rt.Running != nil || len(rt.Queue) != 0 {
		t.Errorf("up still has work after !reset: running %+v, queue %+v", rt.Running, rt.Queue)
	}
	// up answers in the chat, so the chat starts afresh for it too: its
	// next file's run reads none of the earlier ones.
	if !slices.ContainsFunc(readHistory(t, up), func(e history.Entry) bool { return e.Type == history.Reset && e.Chat == dm }) {
		t.Errorf("up's history has no reset marker for the chat:\n%+v", readHistory(t, up))
	}
	if before := promptBlock(capturedPrompt(t, up, brainruntime.Inbound{Inbound: chat.Inbound{JID: dm, Brain: "up", Sender: "upload", Text: "next file", Source: chat.SourceIngest}}), "chat_history"); before != "" {
		t.Errorf("up's next run in the chat would read %+v", before)
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
	tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "up", Name: "Uploads", ReplyChat: dm, Routing: manifest.Routing{Groups: []string{group}}})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	up := tb.brainMgr.Get("up")

	if _, _, _, err := tb.brainMgr.Submit(brainruntime.Inbound{Inbound: chat.Inbound{JID: group, Sender: "15551234567", Text: "group work", Source: chat.SourceWhatsApp}}); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "up's group run", func() bool { r := up.Snapshot().Running; return r != nil && r.PID > 0 })
	if w := tb.ingest(`{"brain":"up","text":"file this"}`); w.Code != 202 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 5*time.Second, "the file to wait", func() bool { return len(up.Snapshot().Queue) == 1 })

	tb.handleInbound(context.Background(), brainruntime.Inbound{Inbound: chat.Inbound{JID: dm, Sender: "15551234567", Text: "!stop", Source: chat.SourceWhatsApp}})
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
	dmBrain := manifest.Manifest{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true}}
	for _, c := range []struct {
		name   string
		extra  string
		brains []manifest.Manifest
		brain  string
		want   string
	}{
		{"DM brain with a group answers in the DM", "",
			[]manifest.Manifest{{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{g1}}}}, "p", dm},
		{"fallback brain gets the DMs, so it answers there before its group", "",
			[]manifest.Manifest{{ID: "f", Name: "F", Routing: manifest.Routing{Default: true, Groups: []string{g1}}}}, "f", dm},
		{"group brain answers in its group", "",
			[]manifest.Manifest{dmBrain, {ID: "g", Name: "G", Routing: manifest.Routing{Groups: []string{g2}}}}, "g", g2},
		{"brain without chats has none", "",
			[]manifest.Manifest{dmBrain, {ID: "n", Name: "N"}}, "n", ""},
		{"no allowed number: the fallback's group", `, "allowed_numbers": []`,
			[]manifest.Manifest{{ID: "f", Name: "F", Routing: manifest.Routing{Groups: []string{g1}}}}, "f", g1},
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
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	upDir := h.AddBrain(manifest.Manifest{ID: "up", Name: "Uploads"})

	if w := h.Do("POST", "/api/dev/inbound", map[string]any{"jid": dm, "brain": "nobody", "text": "x"}); w.Code != 400 {
		t.Errorf("unknown brain: %d, want 400", w.Code)
	}
	for i, text := range []string{"hello up", "!help"} {
		t.Run(text, func(t *testing.T) {
			if w := h.Do("POST", "/api/dev/inbound", map[string]any{"jid": dm, "brain": " Up ", "text": text}); w.Code != 202 {
				t.Fatalf("dev inbound: %d %s", w.Code, w.Body.String())
			}
			h.WaitFor("reply", func() bool { return len(h.Outbox()) > i })
			out := h.Outbox()
			if last := out[len(out)-1]; last.JID != dm || !strings.Contains(last.Text, "echo:"+upDir+":") || !strings.Contains(last.Text, text) {
				t.Errorf("reply = %+v, want an echo of %q from %s", last, text, upDir)
			}
		})
	}
}

// The dev inbound hears in a group whom group_members admits, as WhatsApp
// messages are: the numbers an entry lists, written in any way a number
// is, everyone for ["*"], and without an entry whom allowed_numbers
// admits. A LID, or no sender at all (the group's own digits), is no
// number. A member it refuses reaches no brain, and the group, which is
// allowed, does not wait for access.
func TestDevInboundHearsWhomGroupMembersAdmits(t *testing.T) {
	const (
		listed   = `{"group_members": {"` + harnessGroup + `": ["15551234567"]}}`
		everyone = `{"group_members": {"` + harnessGroup + `": ["*"]}}`
		anyone   = `{"allowed_numbers": [], "allow_any_dm": true}`
	)
	cases := []struct {
		name   string
		cfg    string
		sender string
		heard  bool
	}{
		{"listed", listed, "15551234567", true},
		{"listed, written as people write it", listed, "+1 (555) 123-4567", true},
		{"listed, as a JID", listed, "15551234567@s.whatsapp.net", true},
		{"listed, by LID", listed, "15551234567@lid", false},
		{"not listed", listed, "15559876543", false},
		{"everyone, a stranger", everyone, "15559876543", true},
		{"everyone, by LID", everyone, personLID + "@lid", true},
		{"no entry, in allowed_numbers", "", "15551234567", true},
		{"no entry, a stranger", "", "15559876543", false},
		{"no entry, no sender", "", "", false},
		{"no entry, anyone may DM", anyone, "15559876543", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.cfg)
			dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
			w := h.Do("POST", "/api/dev/inbound", inbound{JID: harnessGroup, Sender: c.sender, Text: "untagged chatter"})
			var resp struct{ Status, Reason string }
			if err := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != 202 || err != nil {
				t.Fatalf("dev inbound: %d %s", w.Code, w.Body.String())
			}
			if !c.heard {
				if resp.Status != "ignored" || resp.Reason != "sender not admitted by group_members" {
					t.Errorf("answer = %+v, want it ignored for group_members", resp)
				}
				if ign := h.Do("GET", "/api/chats/ignored", nil).Body.String(); strings.TrimSpace(ign) != "[]" {
					t.Errorf("the allowed group waits for access: %s", ign)
				}
				return
			}
			if resp.Status != "accepted" {
				t.Fatalf("answer = %+v, want it accepted", resp)
			}
			h.WaitFor("the chatter in the history", func() bool {
				return slices.ContainsFunc(historyLines(t, dir), func(l string) bool { return strings.Contains(l, "untagged chatter") })
			})
		})
	}
}

func TestAddBrainAllowChatAndRoute(t *testing.T) {
	const club = "120363000000000001@g.us"
	h := newHarness(t, "")
	dir := filepath.Join(h.Home(), "family")
	brains := func() []string {
		var views []struct{ ID string }
		_ = json.Unmarshal(h.Do("GET", "/api/brains", nil).Body.Bytes(), &views)
		var out []string
		for _, v := range views {
			out = append(out, v.ID)
		}
		return out
	}

	w := h.Do("POST", "/api/brains/inspect", map[string]string{"path": "~/family"})
	var insp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &insp)
	if w.Code != 200 || insp["exists"] != false {
		t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
	}

	m := manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{HandlesDMs: true}}
	w = h.Do("POST", "/api/brains", map[string]any{"path": "~/family", "manifest": m, "create_dir": true})
	if w.Code != 200 {
		t.Fatalf("add brain: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.File)); err != nil {
		t.Error("brain.json not written")
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "hello brain"}); !strings.Contains(got, "echo:"+dir+":") || !strings.Contains(got, "hello brain") {
		t.Fatalf("reply %q not from the family brain: the new brain must take DMs immediately, without a restart", got)
	}
	// config.json keeps the folder as typed.
	var file struct {
		BrainDirs []config.BrainRef `json:"brain_dirs"`
	}
	if data, err := os.ReadFile(h.Config().Path); err != nil || json.Unmarshal(data, &file) != nil || len(file.BrainDirs) != 1 || file.BrainDirs[0].Path != "~/family" {
		t.Errorf("brain_dirs = %+v (%v)", file.BrainDirs, err)
	}

	// A second DM brain is refused.
	m2 := manifest.Manifest{ID: "work", Name: "Work", Routing: manifest.Routing{HandlesDMs: true}}
	if w := h.Do("POST", "/api/brains", map[string]any{"path": "~/work", "manifest": m2, "create_dir": true}); w.Code != 422 {
		t.Errorf("second DM brain: %d, want 422", w.Code)
	}

	// A group that wrote while not allowed can be allowed and assigned in one go.
	if w := h.Do("POST", "/api/dev/inbound", inbound{JID: club, Sender: "15559876543", Text: "hi"}); w.Code != 202 || !strings.Contains(w.Body.String(), "ignored") {
		t.Fatalf("a group that is not allowed: %d %s", w.Code, w.Body.String())
	}
	w = h.Do("POST", "/api/chats/allow", map[string]string{"jid": club, "kind": "group", "brain": "family"})
	if w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	var eff struct {
		AllowedGroups []string `json:"allowed_groups"`
	}
	_ = json.Unmarshal(h.Config().Effective, &eff)
	if !slices.Contains(eff.AllowedGroups, club) {
		t.Error("group not allowed")
	}
	if got := h.Answer(inbound{JID: club, Sender: "15551234567", Text: "@claude hello club"}); !strings.Contains(got, "echo:"+dir+":") {
		t.Errorf("group not routed to the chosen brain: %q", got)
	}
	if disk, _ := manifest.Read(dir); !slices.Contains(disk.Routing.Groups, club) {
		t.Error("the group must be saved in the brain's own brain.json")
	}
	if ign := h.Do("GET", "/api/chats/ignored", nil).Body.String(); strings.TrimSpace(ign) != "[]" {
		t.Errorf("allowed chat still listed as ignored: %s", ign)
	}
	// The answer reaches the history, and the feed, once it went out.
	h.WaitFor("the answer in the activity feed", func() bool {
		var acts []any
		return json.Unmarshal(h.Do("GET", "/api/activity?brain=family", nil).Body.Bytes(), &acts) == nil && len(acts) >= 2
	})

	// Removing a brain unregisters it and leaves the folder alone.
	if w := h.Do("DELETE", "/api/brains/family", nil); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if ids := brains(); len(ids) != 0 {
		t.Errorf("brains still listed: %v", ids)
	}
	if _, err := os.Stat(filepath.Join(dir, "brain-prompt.md")); err != nil {
		t.Error("removing a brain must not delete its files")
	}
}

func TestApproveDrift(t *testing.T) {
	h := newHarness(t, "")
	m := manifest.Manifest{ID: "notes", Name: "Notes"}
	dir := h.AddBrain(m)
	edited := m
	edited.Name, edited.Routing.Default = "Notes 2", true
	if err := manifest.Write(dir, &edited, true); err != nil {
		t.Fatal(err)
	}

	var views []httpapi.BrainView
	_ = json.Unmarshal(h.Do("GET", "/api/brains", nil).Body.Bytes(), &views)
	if len(views) != 1 || views[0].Status != "drift" || views[0].Disk == nil {
		t.Fatalf("views = %+v", views)
	}
	// The name the running brain has, as GET /api/status reports it.
	running := func() string {
		t.Helper()
		var st struct {
			Brains []struct{ ID, Name string } `json:"brains"`
		}
		if w := h.Do("GET", "/api/status", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil {
			t.Fatalf("GET /api/status: %d %s", w.Code, w.Body)
		}
		for _, b := range st.Brains {
			if b.ID == "notes" {
				return b.Name
			}
		}
		t.Fatalf("GET /api/status lists no brain notes")
		return ""
	}
	if got := running(); got != "Notes" {
		t.Errorf("the running brain is named %q before approval, want Notes: drift must not apply", got)
	}
	if w := h.Do("POST", "/api/brains/notes/approve", map[string]any{"manifest": views[0].Disk}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := running(); got != "Notes 2" {
		t.Errorf("the running brain is named %q after approval, want Notes 2", got)
	}
}

// TestStopRunFromTheDashboard: the dashboard's Stop ends the run in
// progress, and the chat hears nothing about it. The message waiting behind
// it runs next, or with clear_queue is dropped without a word to the chat
// that was told it is queued. With nothing running, Stop stops nothing. A
// third message, sent once the run has ended, shows that nothing else came.
func TestStopRunFromTheDashboard(t *testing.T) {
	for _, c := range []struct {
		name   string
		clear  bool
		golden string
	}{
		{"keep the queue", false, "run/stop-keeps-the-queue.golden"},
		{"clear the queue", true, "run/stop-clears-the-queue.golden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "")
			h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
			pids := filepath.Join(t.TempDir(), "pids")
			t.Setenv("FRIDOLIN_FAKE_PID_LOG", pids)
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
			h.Inject(inbound{JID: harnessDM, Text: "first"})
			// The CLI writes its PID once it runs, with the sleep in its
			// environment; the runs after it answer at once.
			h.WaitFor("the first run's CLI", func() bool {
				data, _ := os.ReadFile(pids)
				return len(data) > 0
			})
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "0")
			h.Inject(inbound{JID: harnessDM, Text: "second"})
			h.WaitSent(harnessDM, "Queued")

			stop := func() bool {
				t.Helper()
				var resp struct {
					Stopped bool `json:"stopped"`
				}
				w := h.Do("POST", "/api/brains/main/stop", map[string]any{"clear_queue": c.clear})
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
					t.Fatalf("stop: %d %s", w.Code, w.Body)
				}
				return resp.Stopped
			}
			if !stop() {
				t.Error("Stop stopped nothing while a run was in progress")
			}
			if !c.clear {
				h.WaitSent(harnessDM, "says: second")
			}
			h.WaitFor("the brain to be idle", func() bool { return !h.Running("main") })
			if stop() {
				t.Error("Stop stopped something with nothing running")
			}
			h.Inject(inbound{JID: harnessDM, Text: "third"})
			h.WaitSent(harnessDM, "says: third")

			s := h.scrubber()
			var sb strings.Builder
			for _, m := range h.Outbox() {
				sb.WriteString(m.JID + " " + strconv.Quote(s.text(m.Text)) + "\n")
			}
			assertGolden(t, c.golden, []byte(sb.String()))
		})
	}
}

// Saving a brain's settings writes its brain.json and the approved copy
// together, so the edit runs at once and shows no drift. An edit that
// changes the id, names no brain, is not valid on its own or clashes with
// another brain is refused and leaves config.json and every brain.json as
// they were.
func TestUpdateBrain(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	dirB := h.AddBrain(manifest.Manifest{ID: "b", Name: "B"})

	edited := manifest.Manifest{ID: "a", Name: "Assistant", Model: "opus", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{harnessGroup}}}
	if w := h.Do("PUT", "/api/brains/a", map[string]any{"manifest": edited}); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	disk, err := manifest.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Name != "Assistant" || disk.Model != "opus" || !slices.Contains(disk.Routing.Groups, harnessGroup) {
		t.Errorf("brain.json = %+v, want the edit", disk)
	}
	if approved, ok := h.Approved()["a"]; !ok || !approved.Equal(*disk) {
		t.Errorf("approved = %+v, want what brain.json has", approved)
	}
	if got := h.Answer(inbound{JID: harnessGroup, Sender: chat.NormalizeJID(harnessDM), Text: "@claude hello"}); !strings.Contains(got, "echo:"+dir+":") {
		t.Errorf("the group got %q, want the answer of the edited brain a", got)
	}
	var views []struct{ ID, Name string }
	if err := json.Unmarshal(h.Do("GET", "/api/brains", nil).Body.Bytes(), &views); err != nil || !slices.Contains(views, struct{ ID, Name string }{"a", "Assistant"}) {
		t.Errorf("brains = %+v (%v), want a named Assistant", views, err)
	}
	if issues := h.Issues(); len(issues) != 0 {
		t.Errorf("issues = %+v, want none", issues)
	}

	for _, c := range []struct {
		name, id string
		m        manifest.Manifest
		want     string
	}{
		{"id changed", "a", manifest.Manifest{ID: "x", Name: "X"}, "id cannot change"},
		{"no such brain", "nobody", manifest.Manifest{ID: "nobody", Name: "N"}, `no brain \"nobody\"`},
		{"not valid", "b", manifest.Manifest{ID: "b", Name: "B", Effort: "huge"}, "effort"},
		{"second DM brain", "b", manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{HandlesDMs: true}}, "both handle DMs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			etag := h.Config().ETag
			before := manifestFiles(t, dir, dirB)
			w := h.Do("PUT", "/api/brains/"+c.id, map[string]any{"manifest": c.m})
			if w.Code != 422 || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("%d %s, want 422 saying %s", w.Code, w.Body, c.want)
			}
			if h.Config().ETag != etag {
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
		data, err := os.ReadFile(filepath.Join(d, manifest.File))
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
	cases := []struct {
		name, to string
		want     map[string][]string // brain → its groups after the request
		code     int
	}{
		{"to another brain", "b", map[string][]string{"a": {}, "b": {harnessGroup}}, 200},
		{"to the brain that has it", "a", map[string][]string{"a": {harnessGroup}, "b": {}}, 200},
		{"to no brain", "nobody", map[string][]string{"a": {harnessGroup}, "b": {}}, 422},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "")
			dirs := map[string]string{
				"a": h.AddBrain(manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{Groups: []string{harnessGroup}}}),
				"b": h.AddBrain(manifest.Manifest{ID: "b", Name: "B"}),
			}
			before := manifestFiles(t, dirs["a"], dirs["b"])
			w := h.Do("POST", "/api/chats/allow", map[string]string{"jid": harnessGroup, "kind": "group", "brain": c.to})
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
				disk, err := manifest.Read(dirs[id])
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(disk.Routing.Groups, want) {
					t.Errorf("%s's brain.json has groups %q, want %q", id, disk.Routing.Groups, want)
				}
			}
			for id, approved := range h.Approved() {
				if want := c.want[id]; !reflect.DeepEqual(approved.Routing.Groups, want) {
					t.Errorf("%s's approved groups are %q, want %q", id, approved.Routing.Groups, want)
				}
			}
			if got := h.Answer(inbound{JID: harnessGroup, Sender: chat.NormalizeJID(harnessDM), Text: "@claude hello"}); !strings.Contains(got, "echo:"+dirs[c.to]+":") {
				t.Errorf("the group got %q, want the answer of %s", got, c.to)
			}
			if issues := h.Issues(); len(issues) != 0 {
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
			got, err := chat.PhoneNumber(c.in)
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
			got, err := config.AllowListGroup(c.in)
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

func TestAllowAndRevokeChats(t *testing.T) {
	const group = "120363000000000001@g.us"
	cases := []struct {
		name    string
		config  string // settings that replace the harness's
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
		{"revoke a hand-written number as it is stored", `{"allowed_numbers": ["+15559876543", "15551234567"]}`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "+15559876543"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a group stored with a space", `{"allowed_groups": [" ` + group + `", "120363000000000000@g.us"]}`,
			"/api/chats/revoke", map[string]string{"kind": "group", "jid": " " + group}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a number the bridge would refuse", `{"allowed_numbers": ["12345", "15551234567"]}`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "12345"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke a group the bridge would refuse", `{"allowed_groups": ["family@g.us", "120363000000000000@g.us"]}`,
			"/api/chats/revoke", map[string]string{"kind": "group", "jid": "family@g.us"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
		{"revoke every form of a number typed another way", `{"allowed_numbers": ["+15551234567", "15559876543", "1 555 123 4567"]}`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "15551234567"}, 200,
			[]string{"15559876543"}, []string{"120363000000000000@g.us"}},
		{"revoke only the stored form that was sent", `{"allowed_numbers": ["+15551234567", "15551234567"]}`,
			"/api/chats/revoke", map[string]string{"kind": "dm", "number": "+15551234567"}, 200,
			[]string{"15551234567"}, []string{"120363000000000000@g.us"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.config)
			w := h.Do("POST", c.path, c.body)
			if w.Code != c.code {
				t.Fatalf("%s: %d %s, want %d", c.path, w.Code, w.Body, c.code)
			}
			var f struct {
				AllowedNumbers []string `json:"allowed_numbers"`
				AllowedGroups  []string `json:"allowed_groups"`
			}
			if err := json.Unmarshal(h.Config().Config, &f); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.AllowedNumbers, c.numbers) {
				t.Errorf("allowed_numbers = %q, want %q", f.AllowedNumbers, c.numbers)
			}
			if !slices.Equal(f.AllowedGroups, c.groups) {
				t.Errorf("allowed_groups = %q, want %q", f.AllowedGroups, c.groups)
			}
		})
	}
}

// Revoking a group drops its group_members entry with it, which the
// config would otherwise refuse, and leaves the other groups' alone.
func TestRevokedGroupTakesItsMembers(t *testing.T) {
	const other = "120363000000000001@g.us"
	tb := newTestBridge(t, `, "allowed_groups": ["120363000000000000@g.us", "`+other+`"],
		"group_members": {"120363000000000000@g.us": ["15559876543"], "`+other+`": ["*"]}`)
	if w := tb.do("POST", "/api/chats/revoke", map[string]string{"kind": "group", "jid": testGroup}); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	f := tb.cfgs.FileCopy()
	if !slices.Equal(f.AllowedGroups, []string{other}) {
		t.Errorf("allowed_groups = %q, want %q", f.AllowedGroups, other)
	}
	if len(f.GroupMembers) != 1 || !slices.Equal(f.GroupMembers[other], []string{anyMember}) {
		t.Errorf("group_members = %q, want only %s's entry", f.GroupMembers, other)
	}
}

// The Chats page sets who in an allowed group is heard through
// /api/chats/members: numbers as digits, each once, ["*"] or no one, and
// null takes the entry away. What the config would refuse is refused,
// with config.json left as it was, and the other groups' entries stay.
func TestGroupMembersEditedOnTheChatsPage(t *testing.T) {
	const other, unknown = "120363000000000001@g.us", "120363000000000009@g.us"
	start := map[string][]string{other: {anyMember}}
	cases := []struct {
		name string
		body map[string]any
		code int
		want map[string][]string // group_members afterwards
		err  string
	}{
		{"numbers, as people write them", map[string]any{"jid": testGroup, "members": []string{"+1 555 987 6543", "15551234567", "15559876543@s.whatsapp.net"}}, 200,
			map[string][]string{testGroup: {"15559876543", "15551234567"}, other: {anyMember}}, ""},
		{"every member", map[string]any{"jid": " " + testGroup, "members": []string{"*"}}, 200,
			map[string][]string{testGroup: {anyMember}, other: {anyMember}}, ""},
		{"no one", map[string]any{"jid": testGroup, "members": []string{}}, 200,
			map[string][]string{testGroup: {}, other: {anyMember}}, ""},
		{"no entry", map[string]any{"jid": other, "members": nil}, 200, map[string][]string{}, ""},
		{"no entry, left out", map[string]any{"jid": other}, 200, map[string][]string{}, ""},
		{"a number that is none", map[string]any{"jid": testGroup, "members": []string{"15551234567", "call me"}}, 400, start, "entry 2 is not a phone number"},
		{"a LID", map[string]any{"jid": testGroup, "members": []string{"15551234567@lid"}}, 400, start, "entry 1 is not a phone number"},
		{"everyone with numbers", map[string]any{"jid": testGroup, "members": []string{"*", "15551234567"}}, 400, start, "with numbers: use"},
		{"a chat that is no group", map[string]any{"jid": "15551234567@s.whatsapp.net", "members": []string{"*"}}, 400, start, "is not a group JID"},
		{"a group that is not allowed", map[string]any{"jid": unknown, "members": []string{"*"}}, 422, start, unknown + " is not in allowed_groups"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, `, "allowed_groups": ["`+testGroup+`", "`+other+`"], "group_members": {"`+other+`": ["*"]}`)
			before, _ := os.ReadFile(tb.cfgs.Path())
			w := tb.do("POST", "/api/chats/members", c.body)
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.err) {
				t.Fatalf("members: %d %s, want %d and %q", w.Code, w.Body, c.code, c.err)
			}
			got := tb.cfgs.FileCopy().GroupMembers
			if got == nil {
				got = map[string][]string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("group_members = %q, want %q", got, c.want)
			}
			if after, _ := os.ReadFile(tb.cfgs.Path()); c.code != 200 && !bytes.Equal(after, before) {
				t.Errorf("a refusal rewrote config.json:\n%s", after)
			}
		})
	}

	// It applies at once: a member left off the list is no longer heard.
	tb := newTestBridge(t, "")
	if w := tb.do("POST", "/api/chats/members", map[string]any{"jid": testGroup, "members": []string{"15559876543"}}); w.Code != 200 {
		t.Fatalf("members: %d %s", w.Code, w.Body)
	}
	for sender, want := range map[string]string{"15559876543": "accepted", "15551234567": "ignored"} {
		if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": testGroup, "sender": sender, "text": "hi"}); !strings.Contains(w.Body.String(), want) {
			t.Errorf("a message from %s: %d %s, want it %s", sender, w.Code, w.Body, want)
		}
	}
}

func TestRevokedGroupKeepsItsBrain(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	if w := h.Do("POST", "/api/chats/revoke", map[string]string{"kind": "group", "jid": harnessGroup}); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if disk, _ := manifest.Read(dir); !slices.Contains(disk.Routing.Groups, harnessGroup) {
		t.Error("revoking the group took it out of the brain's routing")
	}
	if w := h.Do("POST", "/api/chats/allow", map[string]string{"kind": "group", "jid": harnessGroup}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body)
	}
	if got := h.Answer(inbound{JID: harnessGroup, Sender: chat.NormalizeJID(harnessDM), Text: "@claude hello"}); !strings.Contains(got, "echo:"+dir+":") {
		t.Errorf("allowed again, the group got %q, want family's answer", got)
	}
}

func TestAllowingATypedChatClearsItsWaitingChat(t *testing.T) {
	cases := []struct {
		name  string
		wrote inbound // what the chat sent while it was not allowed
		body  map[string]string
	}{
		{"a number", inbound{JID: "15559876543@s.whatsapp.net", Text: "hi"}, map[string]string{"kind": "dm", "number": "+1 555 987 6543"}},
		{"a group with spaces around it", inbound{JID: "120363000000000001@g.us", Sender: "15559876543", Text: "hi"}, map[string]string{"kind": "group", "jid": " 120363000000000001@g.us "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "")
			if w := h.Do("POST", "/api/dev/inbound", c.wrote); w.Code != 202 || !strings.Contains(w.Body.String(), "ignored") {
				t.Fatalf("a chat that is not allowed: %d %s", w.Code, w.Body.String())
			}
			if ign := h.Do("GET", "/api/chats/ignored", nil).Body.String(); !strings.Contains(ign, c.wrote.JID) {
				t.Fatalf("the chat does not wait: %s", ign)
			}
			if w := h.Do("POST", "/api/chats/allow", c.body); w.Code != 200 {
				t.Fatalf("allow: %d %s", w.Code, w.Body)
			}
			if ign := h.Do("GET", "/api/chats/ignored", nil).Body.String(); strings.TrimSpace(ign) != "[]" {
				t.Errorf("the allowed chat still waits: %s", ign)
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
	newHarness(t, `{"allowed_numbers": [], "allow_any_dm": true}`)
	if !strings.Contains(logs.String(), "WARNING: anyone") {
		t.Errorf("startup log has no warning: %q", logs.String())
	}
}

func TestEmptyingTheAllowlistWarnsOnlyWithAllowAnyDM(t *testing.T) {
	tests := []struct {
		name   string
		config string
		open   bool
	}{
		{"flag unset", "", false},
		{"explicit false", `{"allow_any_dm": false}`, false},
		{"explicit true", `{"allow_any_dm": true}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.config)
			var logs lockedBuffer
			defer log.SetOutput(log.Writer())
			log.SetOutput(&logs)
			// Removing the last number on the Chats page.
			if w := h.Do("POST", "/api/chats/revoke", map[string]string{"kind": "dm", "number": chat.NormalizeJID(harnessDM)}); w.Code != http.StatusOK {
				t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
			}
			if got := strings.Contains(logs.String(), "WARNING: anyone"); got != tc.open {
				t.Errorf("log warns = %v, want %v (log: %q)", got, tc.open, logs.String())
			}
			var st struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(h.Do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
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
	h := newHarness(t, "")
	for _, p := range []string{"/api/status", "/api/config", "/api/brains", "/api/logs", "/api/backup/status"} {
		body := h.Do("GET", p, nil).Body.String()
		if strings.Contains(body, "very-secret-value") || strings.Contains(body, h.token()) {
			t.Errorf("%s leaks a secret", p)
		}
	}
}

func TestGetConfigLocksNoSettings(t *testing.T) {
	// Environment variables used to override config.json, and the response
	// named the settings they locked, so the dashboard could disable them.
	// Now the file is all there is: what it says is what runs.
	t.Setenv("ALLOWED_GROUPS", "120363000000000001@g.us")
	h := newHarness(t, "")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(h.Do("GET", "/api/config", nil).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(got))
	if want := []string{"config", "effective", "etag", "path"}; !slices.Equal(keys, want) {
		t.Errorf("GET /api/config has %v, want %v", keys, want)
	}
	var eff config.Config
	if err := json.Unmarshal(got["effective"], &eff); err != nil {
		t.Fatal(err)
	}
	if want := []string{harnessGroup}; !slices.Equal(eff.AllowedGroups, want) {
		t.Errorf("effective allowed_groups = %v, want config.json's %v", eff.AllowedGroups, want)
	}
}

func TestPutConfigKeepsBrainsAndReportsRestart(t *testing.T) {
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "p", Name: "P"})
	got := h.Config()
	var c map[string]any
	_ = json.Unmarshal(got.Config, &c)
	c["log_level"] = "debug"
	c["triggers"] = []any{"@claude", map[string]string{"text": "@c", "match": "start"}}
	w := h.Do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": c})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "log_level") {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if len(h.Approved()) != 1 {
		t.Error("saving settings must not drop brains")
	}
	var eff config.Config
	if err := json.Unmarshal(h.Config().Effective, &eff); err != nil {
		t.Fatal(err)
	}
	if !eff.Triggers.Matches("@c hi") || eff.Triggers.Matches("@carla") {
		t.Error("trigger change not applied live")
	}
	if w := h.Do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": c}); w.Code != 409 {
		t.Errorf("stale etag: %d, want 409", w.Code)
	}
}

// A setting read only at startup stays pending in /api/status until the
// restart, so the dashboard's restart banner survives a reload and shows in
// every tab. Set back to the value the bridge started with, it is not
// pending any more.
func TestStatusReportsSettingsWaitingForRestart(t *testing.T) {
	h := newHarness(t, "")
	status := func(t *testing.T) []string {
		t.Helper()
		var st struct {
			RestartRequired []string `json:"restart_required"`
		}
		if err := json.Unmarshal(h.Do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil || st.RestartRequired == nil {
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
			_ = json.Unmarshal(h.Do("GET", "/api/config", nil).Body.Bytes(), &cur)
			cur.Config["log_level"] = s.logLevel
			w := h.Do("PUT", "/api/config", map[string]any{"etag": cur.ETag, "config": cur.Config})
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
	h := newHarness(t, "")
	got := h.Config()
	var configValue map[string]any
	if err := json.Unmarshal(got.Config, &configValue); err != nil {
		t.Fatal(err)
	}
	if configValue["brain_mode"] != true {
		t.Errorf("GET /api/config: brain_mode = %v, want true", configValue["brain_mode"])
	}
	configValue["brain_mode"] = false
	w := h.Do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": configValue})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "classic mode, which was removed") {
		t.Errorf("put brain_mode false: %d %s, want 400 saying classic mode was removed", w.Code, w.Body.String())
	}
	if h.Config().ETag != got.ETag {
		t.Error("the refused request changed config.json")
	}
}

// Inline brains were removed. A dashboard save that brings some back is
// refused like a config.json that still has them, and the config the
// dashboard loads has no brains key to send back. From a tab opened before
// the upgrade, which sends no version, the save is migrated like a version
// 1 file and says why; in a version 2 save, brains is an unknown key.
func TestPutConfigRefusesInlineBrains(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version any // the save's version, nil for none
		want    string
	}{
		{"from a tab opened before the upgrade", nil, "no longer runs"},
		{"version 2", 2, `unknown field \"brains\"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			got := h.Config()
			var configValue map[string]any
			if err := json.Unmarshal(got.Config, &configValue); err != nil {
				t.Fatal(err)
			}
			if _, ok := configValue["brains"]; ok {
				t.Errorf("GET /api/config has a brains key: %v", configValue["brains"])
			}
			delete(configValue, "version")
			if tc.version != nil {
				configValue["version"] = tc.version
			}
			configValue["brains"] = []map[string]any{{"name": "a", "work_dir": "~/a"}}
			w := h.Do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": configValue})
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("put inline brains: %d %s, want 400 saying %s", w.Code, w.Body.String(), tc.want)
			}
			if h.Config().ETag != got.ETag {
				t.Error("the refused request changed config.json")
			}
		})
	}
}

// Allowing a group for a brain takes it away from the brain that had it,
// in both brain.json files, and leaves that brain's other groups alone.
func TestAllowGroupMovesItBetweenBrains(t *testing.T) {
	const moved, kept = "120363000000000001@g.us", "120363000000000002@g.us"
	h := newHarness(t, `{"allowed_groups": ["`+moved+`", "`+kept+`"]}`)
	fromDir := h.AddBrain(manifest.Manifest{ID: "from", Name: "From", Routing: manifest.Routing{Groups: []string{kept, moved}}})
	toDir := h.AddBrain(manifest.Manifest{ID: "to", Name: "To"})

	if w := h.Do("POST", "/api/chats/allow", map[string]string{"jid": moved, "kind": "group", "brain": "to"}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	if from, _ := manifest.Read(fromDir); !slices.Equal(from.Routing.Groups, []string{kept}) {
		t.Errorf("from has groups %q, want only %q", from.Routing.Groups, kept)
	}
	if to, _ := manifest.Read(toDir); !slices.Equal(to.Routing.Groups, []string{moved}) {
		t.Errorf("to has groups %q, want %q", to.Routing.Groups, moved)
	}
	if got := h.Answer(inbound{JID: moved, Sender: chat.NormalizeJID(harnessDM), Text: "@claude hello"}); !strings.Contains(got, "echo:"+toDir+":") {
		t.Errorf("the group got %q, want to's answer", got)
	}
	if got := h.Answer(inbound{JID: kept, Sender: chat.NormalizeJID(harnessDM), Text: "@claude hello"}); !strings.Contains(got, "echo:"+fromDir+":") {
		t.Errorf("the other group got %q, want from's answer", got)
	}
}

// qrMessenger is the dev messenger with a pairing code to show.
type qrMessenger struct {
	*devchat.Messenger
	code string
}

func (q qrMessenger) State() chat.LinkState { return chat.LinkState{Mode: "pairing", QRCode: q.code} }

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
		t.Fatalf("data_url %q is not a base64 PNG", textx.Truncate(body.DataURL, 40))
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
	h := newHarness(t, `{"model": "sonnet"}`)
	got := h.Config()
	var configValue map[string]any
	if err := json.Unmarshal(got.Config, &configValue); err != nil {
		t.Fatal(err)
	}
	if _, ok := configValue["model"]; ok {
		t.Errorf("GET /api/config has a model key: %v", configValue["model"])
	}
	if configValue["brain_model"] != "sonnet" {
		t.Errorf("GET /api/config: brain_model %v, want sonnet", configValue["brain_model"])
	}

	// An old tab: brain_model as an older bridge served it, and model,
	// with no version, which older bridges did not have.
	configValue["brain_model"], configValue["model"] = "", "haiku"
	delete(configValue, "version")
	if w := h.Do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": configValue}); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	var eff config.Config
	if err := json.Unmarshal(h.Config().Effective, &eff); err != nil {
		t.Fatal(err)
	}
	if eff.BrainModel != "haiku" {
		t.Errorf("brain_model after the save = %q, want haiku", eff.BrainModel)
	}
	if raw, ok := readRawConfig(t, h.Config().Path)["model"]; ok {
		t.Errorf("the save wrote model: %s", raw)
	}
}

func TestAddBrainBackupDefaults(t *testing.T) {
	h := newHarness(t, "")
	// No backup settings given: backed up, with the default exclusions.
	_ = h.Do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": map[string]any{"id": "a", "name": "A"}})
	// Explicitly off stays off.
	off := manifest.Manifest{ID: "b", Name: "B", Backup: manifest.Backup{Enabled: false, Exclude: []string{"*.log"}}}
	_ = h.Do("POST", "/api/brains", map[string]any{"path": "~/b", "create_dir": true, "manifest": off})
	// Off without an exclude list stays off too, with the default
	// exclusions should it be turned on.
	if w := h.Do("POST", "/api/brains", map[string]any{"path": "~/c", "create_dir": true, "manifest": map[string]any{"id": "c", "name": "C", "backup": map[string]any{"enabled": false}}}); w.Code != 200 {
		t.Fatalf("add c: %d %s", w.Code, w.Body.String())
	}
	a, _ := manifest.Read(filepath.Join(h.Home(), "a"))
	b, _ := manifest.Read(filepath.Join(h.Home(), "b"))
	c, _ := manifest.Read(filepath.Join(h.Home(), "c"))
	if !a.Backup.Enabled || len(a.Backup.Exclude) == 0 {
		t.Errorf("a.backup = %+v, want on with defaults", a.Backup)
	}
	if b.Backup.Enabled || len(b.Backup.Exclude) != 1 {
		t.Errorf("b.backup = %+v, want off as requested", b.Backup)
	}
	if c.Backup.Enabled || !slices.Equal(c.Backup.Exclude, manifest.DefaultBackupExcludes()) {
		t.Errorf("c.backup = %+v, want off with the default exclusions", c.Backup)
	}
}

func TestOTPGuardHandsCodeToLocalScripts(t *testing.T) {
	h := newHarness(t, `{"otp_guard": true, "otp_markers": ["Examplebank 2FA"]}`)
	dir := h.AddBrain(manifest.Manifest{ID: "p", Name: "P", Routing: manifest.Routing{HandlesDMs: true}})
	// A local script asks for a code through /send, which names the
	// request...
	asked := time.Now().UnixMilli()
	sent := h.Script("POST", "/send", `{"jid":"15551234567@s.whatsapp.net","text":"Examplebank 2FA: reply with the code"}`)
	var answer struct {
		Status     string `json:"status"`
		OTPRequest string `json:"otp_request"`
	}
	if err := json.Unmarshal(sent.Body.Bytes(), &answer); err != nil || sent.Code != http.StatusOK || answer.Status != "sent" || len(answer.OTPRequest) != 16 {
		t.Fatalf("/send = %d %s, want it sent with the request's id", sent.Code, sent.Body.String())
	}
	// ...and the reply arrives.
	if w := h.Do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "text": " 483920 "}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	otpPath := filepath.Join(h.DataDir(), "otp", "p.json")
	testutil.WaitFor(t, 3*time.Second, "otp file", func() bool { _, err := os.Stat(otpPath); return err == nil })
	data, _ := os.ReadFile(otpPath)
	var got otp.Record
	_ = json.Unmarshal(data, &got)
	if got.Code != "483920" || got.Chat != "15551234567@s.whatsapp.net" || got.Brain != "p" || got.Sender == "" || got.Received == "" {
		t.Errorf("otp file = %s", data)
	}
	// What a script compares with the time it asked: no time zone needed.
	if got.ReceivedUnixMS < asked-5000 || got.ReceivedUnixMS > time.Now().UnixMilli() {
		t.Errorf("received_unix_ms = %d, want a time around %d", got.ReceivedUnixMS, asked)
	}
	// The code names the request it answers, and when the file goes.
	if got.Request != answer.OTPRequest || got.RequestedUnixMS < asked-5000 || got.RequestedUnixMS > got.ReceivedUnixMS {
		t.Errorf("request %q at %d, want %q around %d", got.Request, got.RequestedUnixMS, answer.OTPRequest, asked)
	}
	if want := got.ReceivedUnixMS + otp.HandoffTTL.Milliseconds(); got.ExpiresUnixMS != want {
		t.Errorf("expires_unix_ms = %d, want %d", got.ExpiresUnixMS, want)
	}
	if st, _ := os.Stat(otpPath); st.Mode().Perm() != 0o600 {
		t.Errorf("otp file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(otpPath)); st.Mode().Perm() != 0o700 {
		t.Errorf("otp folder mode %v", st.Mode().Perm())
	}
	hist, _ := os.ReadFile(filepath.Join(dir, "conversation-history.jsonl"))
	if strings.Contains(string(hist), "483920") {
		t.Error("the code must not be written to the conversation history")
	}
	time.Sleep(200 * time.Millisecond)
	// The sender is told what became of the code; nothing else is sent.
	var texts []string
	for _, m := range h.Outbox() {
		texts = append(texts, m.Text)
	}
	slices.Sort(texts)
	if len(texts) != 2 || !strings.Contains(texts[0], "Examplebank 2FA") || texts[1] != outboundmsg.OtpCaughtNotice {
		t.Errorf("sent %q — only the script's own request and the notice may be there; the brain must not run on a caught code", texts)
	}
}

// /send names a message that asks for a one-time code, which the guard
// would catch the answer to, and records the name with its history line.
func TestSendNamesACodeRequest(t *testing.T) {
	cases := []struct {
		name, extra, text string
		brain             bool // a brain answers the chat
		named             bool
	}{
		{"a request", `, "otp_guard": true`, "🔐 Please reply with the code", true, true},
		{"no marker", `, "otp_guard": true`, "Deploy finished", true, false},
		{"the guard off", `, "otp_guard": false`, "🔐 Please reply with the code", true, false},
		{"a chat no brain answers", `, "otp_guard": true`, "🔐 Please reply with the code", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, c.extra)
			if c.brain {
				tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
			}
			body, _ := json.Marshal(map[string]string{"jid": testDMChat, "text": c.text})
			r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", bytes.NewReader(body))
			r.Host = "127.0.0.1:8765"
			w := httptest.NewRecorder()
			tb.h.ServeHTTP(w, r)
			var answer map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || w.Code != 200 {
				t.Fatalf("/send = %d %s", w.Code, w.Body.String())
			}
			id, named := answer["otp_request"]
			if named != c.named {
				t.Errorf("/send = %v, named %v, want %v", answer, named, c.named)
			}
			if br := tb.brainMgr.Get("dm"); br != nil {
				h := readHistory(t, br)
				if len(h) != 1 || h[0].OTPRequest != id {
					t.Errorf("history = %+v, want the line with the request's id %q", h, id)
				}
			}
		})
	}
}
