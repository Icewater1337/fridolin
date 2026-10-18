package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestDecodeJSONIsStrict(t *testing.T) {
	type body struct {
		DryRun bool   `json:"dry_run"`
		Name   string `json:"name"`
	}
	cases := []struct {
		name     string
		body     string
		chunked  bool
		optional bool
		want     string // "" = decodes
	}{
		{name: "one value", body: `{"dry_run": true, "name": "x"}`},
		{name: "trailing space", body: `{"dry_run": true}` + "\n  "},
		{name: "unknown key", body: `{"dry_run": true, "dryrun": false}`, want: `unknown field "dryrun"`},
		{name: "second value", body: `{"dry_run": true} {"garbage": 1}`, want: "single JSON value"},
		{name: "wrong type", body: `{"dry_run": "yes"}`, want: "dry_run"},
		{name: "cut short", body: `{"dry_run": tr`, want: "invalid JSON"},
		{name: "empty", body: ``, want: "a JSON body is required"},
		{name: "empty chunked", body: ``, chunked: true, want: "a JSON body is required"},
		{name: "empty optional", body: ``, optional: true},
		{name: "empty optional chunked", body: ``, chunked: true, optional: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			if c.chunked {
				r.ContentLength = -1
			}
			var v body
			var err error
			if c.optional {
				err = decodeOptionalJSON(r, &v)
			} else {
				err = decodeJSON(r, &v)
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("got %v, want it decoded", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
		})
	}
}

// Every request body the dashboard sends (web/assets) gets past the strict
// decoder. A handler may still refuse it for what it says, in this empty
// test bridge, but never because a key is unknown.
func TestDashboardRequestBodiesDecode(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "notes", Name: "Notes", Routing: BrainRouting{HandlesDMs: true, Default: true}})
	manifest := map[string]any{
		"version": 1, "id": "notes", "name": "Notes",
		"routing": map[string]any{"handles_dms": true, "default": true, "groups": []string{}},
		"model":   "", "effort": "", "prompt_file": "brain-prompt.md", "reply_chat": "",
		"backup":           map[string]any{"enabled": true, "exclude": []string{"*.log"}},
		"disallowed_tools": []string{"WebFetch"},
	}
	cases := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/brains/inspect", map[string]any{"path": "~/other"}},
		{"POST", "/api/brains", map[string]any{"path": "~/other", "manifest": map[string]any{
			"version": 1, "id": "other", "name": "Other",
			"routing": map[string]any{"handles_dms": false, "default": false, "groups": []string{}},
			"model":   "", "effort": "", "backup": map[string]any{"enabled": true, "exclude": []string{}},
		}, "create_dir": true, "use_existing": false}},
		{"PUT", "/api/brains/notes", map[string]any{"manifest": manifest}},
		{"POST", "/api/brains/notes/stop", map[string]any{"clear_queue": true}},
		{"POST", "/api/chats/allow", map[string]any{"jid": "15559876543@s.whatsapp.net", "kind": "dm", "number": "15559876543"}},
		{"POST", "/api/chats/allow", map[string]any{"jid": "120363000000000001@g.us", "kind": "group", "brain": "notes"}},
		{"POST", "/api/chats/dismiss", map[string]any{"jid": "120363000000000002@g.us"}},
		// The Chats page's add boxes and remove buttons.
		{"POST", "/api/chats/allow", map[string]any{"kind": "dm", "number": "+1 555 987 6543"}},
		{"POST", "/api/chats/allow", map[string]any{"kind": "group", "jid": "120363000000000003@g.us"}},
		{"POST", "/api/chats/revoke", map[string]any{"kind": "dm", "number": "15559876543"}},
		{"POST", "/api/chats/revoke", map[string]any{"kind": "group", "jid": "120363000000000003@g.us"}},
		{"PUT", "/api/backup/settings", map[string]any{"enabled": false, "daily_at": "03:30", "root": "fridolin-backup", "keep_versions_days": 30, "include_bridge_config": true}},
		{"PUT", "/api/brains/notes/backup", map[string]any{"enabled": true}},
		{"PUT", "/api/brains/notes/backup", map[string]any{"exclude": []string{"*.log"}}},
		{"POST", "/api/backup/run", map[string]any{}},
		{"POST", "/api/backup/restore", map[string]any{"brain": "notes", "version": "../refused", "target": "~/restored"}},
		{"POST", "/api/backup/run", map[string]any{"brains": []string{"notes"}, "include_bridge": false, "force": true}},
		{"PUT", "/api/plaud/settings", map[string]any{"enabled": false, "brain": "notes", "polished": false, "brain_access": false, "message": ""}},
		{"PUT", "/api/google/client", map[string]any{"json": "{}"}},
		{"PUT", "/api/brains/notes/calendar", map[string]any{"account": "", "calendars": []string{}, "default": ""}},
		{"POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "sender": "", "brain": "", "text": "hi", "silent": false}},
		{"PUT", "/api/site/settings", map[string]any{"domain": "", "title": "", "listen": ""}},
		{"PUT", "/api/site/settings", map[string]any{"tunnel": false}},
		{"PUT", "/api/site/roles", map[string]any{"roles": []string{"admin"}}},
		{"PUT", "/api/site/tunnel", map[string]any{"token": ""}},
		{"POST", "/api/site/users", map[string]any{"kind": "google", "email": "", "username": "", "name": "", "role": "nope", "password": ""}},
		{"PUT", "/api/site/users/u_none", map[string]any{"role": "admin"}},
		{"PUT", "/api/site/users/u_none", map[string]any{"password": "x"}},
		{"PUT", "/api/site/builders/notes", map[string]any{"on": false}},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := tb.do(c.method, c.path, c.body)
			body := w.Body.String()
			if strings.Contains(body, "invalid JSON") || strings.Contains(body, "JSON body is required") ||
				w.Code == http.StatusMethodNotAllowed || strings.Contains(body, "no such API endpoint") || strings.Contains(body, "404 page not found") {
				t.Fatalf("%d %s", w.Code, body)
			}
		})
	}
}

// A body the decoder refuses gets a 400 that says why.
func TestDashboardRefusesUnknownKeys(t *testing.T) {
	tb := newTestBridge(t, "")
	w := tb.do("POST", "/api/brains/inspect", map[string]any{"path": "~/x", "paht": "~/y"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `unknown field \"paht\"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = tb.do("POST", "/api/chats/dismiss", nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "JSON body is required") {
		t.Fatalf("dismiss without a body: %d %s", w.Code, w.Body.String())
	}
}

// The API answers like any net/http mux: 404 for a path it does not have,
// 405 with Allow for one asked with the wrong method. Outside dev mode the
// dev endpoints are not there at all.
func TestAPIRoutesAnswer404And405(t *testing.T) {
	tb := newTestBridge(t, "")
	cases := []struct {
		method, path string
		want         int
		allow        string
	}{
		{"GET", "/api/restart", http.StatusMethodNotAllowed, "POST"},
		{"POST", "/api/status", http.StatusMethodNotAllowed, "GET, HEAD"},
		{"GET", "/api/nothing-here", http.StatusNotFound, ""},
		{"GET", "/api/runs", http.StatusNotFound, ""},
		{"POST", "/api/runs/notes/stop", http.StatusNotFound, ""},
		{"GET", "/api/backup/versions?brain=notes", http.StatusNotFound, ""},
		{"GET", "/api/dev/outbox", http.StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := tb.do(c.method, c.path, nil)
			if w.Code != c.want || w.Header().Get("Allow") != c.allow {
				t.Fatalf("%d (Allow %q), want %d (Allow %q): %s", w.Code, w.Header().Get("Allow"), c.want, c.allow, w.Body.String())
			}
		})
	}

	tb.dev = nil
	tb.h = tb.newHTTPHandler("127.0.0.1:8765")
	for _, path := range []string{"/api/dev/outbox", "/api/dev/inbound"} {
		method := "GET"
		if path == "/api/dev/inbound" {
			method = "POST"
		}
		if w := tb.do(method, path, map[string]string{"jid": "x"}); w.Code != http.StatusNotFound {
			t.Errorf("%s %s outside dev mode: %d %s", method, path, w.Code, w.Body.String())
		}
	}
}

// A brain's backup settings have a route of their own, and the global
// settings no longer take a brain.
func TestBrainBackupRoute(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, BrainManifest{ID: "notes", Name: "Notes"})
	if w := tb.do("PUT", "/api/brains/notes/backup", map[string]any{"enabled": false, "exclude": []string{" *.tmp ", ""}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := tb.brainMgr.Get("notes").settings.Load().manifest
	if m.Backup.Enabled || !slices.Equal(m.Backup.Exclude, []string{"*.tmp"}) {
		t.Fatalf("backup = %+v", m.Backup)
	}
	if w := tb.do("PUT", "/api/brains/nobody/backup", map[string]any{"enabled": true}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown brain: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"brain": "notes", "brain_enabled": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("global settings with a brain: %d %s", w.Code, w.Body.String())
	}
}

func TestLoadDashboardToken(t *testing.T) {
	t.Run("made on first run, then kept", func(t *testing.T) {
		dir := t.TempDir()
		a, err := LoadDashboardToken(dir)
		if err != nil {
			t.Fatal(err)
		}
		b, err := LoadDashboardToken(dir)
		if err != nil || b.Value() != a.Value() || len(a.Value()) < minTokenLen {
			t.Fatalf("second load: %v, %q vs %q", err, b.Value(), a.Value())
		}
	})
	cases := []struct {
		name  string
		setup func(path string) error
		want  string
	}{
		{"too short", func(p string) error { return os.WriteFile(p, []byte("abc\n"), 0o600) }, "is not a token"},
		{"two words", func(p string) error {
			return os.WriteFile(p, []byte(strings.Repeat("a", 20)+" "+strings.Repeat("b", 20)), 0o600)
		}, "is not a token"},
		{"unreadable", func(p string) error { return os.Mkdir(p, 0o700) }, "reading dashboard token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dashboard.token")
			if err := c.setup(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if _, err := LoadDashboardToken(dir); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatal("the token file was replaced")
			}
		})
	}
}

func TestOriginGuardAndNoStore(t *testing.T) {
	tb := newTestBridge(t, "")
	cases := []struct {
		origin string
		want   int
	}{
		{"http://127.0.0.1:8765", 200},
		{"HTTP://LOCALHOST:8765", 200},
		{"http://[::1]:8765", 200},
		{"null", 403},
		{"https://127.0.0.1:8765", 403},
		{"http://127.0.0.1:8765.evil.example.com", 403},
		{"http://127.0.0.1:9999", 403},
	}
	for _, c := range cases {
		t.Run(c.origin, func(t *testing.T) {
			w := tb.do("POST", "/api/brains/inspect", map[string]string{"path": "~/x"}, func(r *http.Request) { r.Header.Set("Origin", c.origin) })
			if w.Code != c.want {
				t.Fatalf("%d, want %d: %s", w.Code, c.want, w.Body.String())
			}
		})
	}
	// A refusal is not cached either.
	w := tb.do("GET", "/api/status", nil, func(r *http.Request) { r.Header.Del("X-Bridge-Token") })
	if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 || s.MaxHeaderBytes > 1<<20 {
		t.Fatalf("read header %v, read %v, idle %v, max header %d", s.ReadHeaderTimeout, s.ReadTimeout, s.IdleTimeout, s.MaxHeaderBytes)
	}
	// Handlers that wait for a person or for rclone must not be cut off.
	if s.WriteTimeout != 0 {
		t.Fatalf("write timeout %v", s.WriteTimeout)
	}
}

func TestIsolationHeadersAndOAuthCallback(t *testing.T) {
	tb := newTestBridge(t, "")
	noToken := func(r *http.Request) { r.Header.Del("X-Bridge-Token") }
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/", 200},
		{"GET", "/assets/app.css", 200},
		// A HEAD does not reach the callback, which would use up the state.
		{"HEAD", "/?state=abc&code=x", 200},
		{"GET", "/?state=abc&code=x", 400},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := tb.do(c.method, c.path, nil, noToken)
			if w.Code != c.want {
				t.Fatalf("%d, want %d: %.200s", w.Code, c.want, w.Body.String())
			}
			if got := w.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
				t.Fatalf("Cross-Origin-Opener-Policy %q", got)
			}
		})
	}
}

// lockProbe is a ResponseWriter that, as the body is written, checks
// whether mu is free.
type lockProbe struct {
	*httptest.ResponseRecorder
	mu   *sync.Mutex
	free bool
}

func (p *lockProbe) Write(b []byte) (int, error) {
	if p.mu.TryLock() {
		p.free = true
		p.mu.Unlock()
	}
	return p.ResponseRecorder.Write(b)
}

// The backup status and cancel are written after the backup manager's lock
// is released: a client that reads slowly must not hold up the job.
func TestBackupStatusWritesWithoutTheLock(t *testing.T) {
	tb := newTestBridge(t, "")
	for _, c := range []struct {
		method, path string
		h            http.HandlerFunc
	}{
		{"GET", "/api/backup/status", tb.apiBackupStatus},
		{"POST", "/api/backup/cancel", tb.apiBackupCancel},
	} {
		w := &lockProbe{ResponseRecorder: httptest.NewRecorder(), mu: &tb.backup.mu}
		c.h(w, httptest.NewRequest(c.method, c.path, nil))
		if !w.free {
			t.Errorf("%s %s wrote its response holding the lock (%d)", c.method, c.path, w.Code)
		}
	}
}

// editConfig saves the settings page's config with edit applied, the way
// the page does: the whole file layer, with the etag it was read with.
func (tb *testBridge) editConfig(t *testing.T, edit func(c map[string]any)) *httptest.ResponseRecorder {
	t.Helper()
	var cur struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &cur); err != nil {
		t.Fatal(err)
	}
	edit(cur.Config)
	return tb.do("PUT", "/api/config", map[string]any{"etag": cur.ETag, "config": cur.Config})
}

// status returns the parts of /api/status these tests read.
func (tb *testBridge) status(t *testing.T) (warnings, restart []string) {
	t.Helper()
	var st struct {
		Warnings        []string `json:"warnings"`
		RestartRequired []string `json:"restart_required"`
	}
	if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st.Warnings, st.RestartRequired
}

func hasWarning(warnings []string, part string) bool {
	return slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, part) })
}

// Turning the dashboard token off is saved but waits for a restart, with a
// warning until then; turning it on applies at once.
func TestRequireTokenOffWaitsForARestart(t *testing.T) {
	noToken := func(r *http.Request) { r.Header.Del("X-Bridge-Token") }
	t.Run("off", func(t *testing.T) {
		tb := newTestBridge(t, "")
		if w := tb.editConfig(t, func(c map[string]any) { c["dashboard"] = map[string]any{"require_token": false} }); w.Code != 200 ||
			!strings.Contains(w.Body.String(), "dashboard.require_token") {
			t.Fatalf("put: %d %s", w.Code, w.Body.String())
		}
		if w := tb.do("GET", "/api/status", nil, noToken); w.Code != http.StatusUnauthorized {
			t.Fatalf("without the token after turning it off: %d, want 401 until a restart", w.Code)
		}
		warnings, restart := tb.status(t)
		if !slices.Contains(restart, "dashboard.require_token") || !hasWarning(warnings, "takes effect at the next restart") {
			t.Fatalf("restart_required %v, warnings %q", restart, warnings)
		}
	})
	t.Run("on", func(t *testing.T) {
		tb := newTestBridge(t, `, "dashboard": {"require_token": false}`)
		if w := tb.do("GET", "/api/status", nil, noToken); w.Code != 200 {
			t.Fatalf("started without the token: %d", w.Code)
		}
		if warnings, _ := tb.status(t); !hasWarning(warnings, "The dashboard token is off") {
			t.Fatalf("warnings %q", warnings)
		}
		if w := tb.editConfig(t, func(c map[string]any) { c["dashboard"] = map[string]any{"require_token": true} }); w.Code != 200 {
			t.Fatalf("put: %d %s", w.Code, w.Body.String())
		}
		if w := tb.do("GET", "/api/status", nil, noToken); w.Code != http.StatusUnauthorized {
			t.Fatalf("without the token after turning it on: %d, want 401 at once", w.Code)
		}
		if warnings, restart := tb.status(t); len(restart) != 0 || hasWarning(warnings, "dashboard token") {
			t.Fatalf("restart_required %v, warnings %q", restart, warnings)
		}
	})
}

// Brains with all permissions and no sandbox can read the token
// themselves, on every OS, and the status says so.
func TestTokenWarningsWithoutASandbox(t *testing.T) {
	tb := newTestBridge(t, "")
	cases := []struct {
		name    string
		goos    string
		skip    bool
		sandbox bool
		want    string
	}{
		{"macOS, sandbox off", "darwin", true, false, "Turn the sandbox on"},
		{"Linux", "linux", true, false, "there is no sandbox on linux"},
		{"macOS, sandbox on", "darwin", true, true, ""},
		{"no skip-permissions", "linux", false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := *tb.cfgs.Get()
			cfg.DangerouslySkipPermissions, cfg.Sandbox.Enabled = c.skip, c.sandbox
			got := tb.tokenWarnings(&cfg, c.goos)
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("warnings %q, want none", got)
				}
				return
			}
			if !hasWarning(got, c.want) || !hasWarning(got, "dashboard token") {
				t.Fatalf("warnings %q, want one with %q", got, c.want)
			}
		})
	}
}
