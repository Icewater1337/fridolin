package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/registry"

	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// Every request body the dashboard sends (web/assets) gets past the strict
// decoder. A handler may still refuse it for what it says, in this empty
// test bridge, but never because a key is unknown.
func TestDashboardRequestBodiesDecode(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "notes", Name: "Notes", Routing: manifest.Routing{HandlesDMs: true, Default: true}})
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
		{"POST", "/api/brains/notes/approve", map[string]any{"manifest": manifest}},
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
		{"POST", "/api/login", map[string]any{"code": "used"}},
		{"POST", "/api/login", map[string]any{"token": "wrong"}},
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
	tb.addFolderBrain(t, manifest.Manifest{ID: "notes", Name: "Notes"})
	if w := tb.do("PUT", "/api/brains/notes/backup", map[string]any{"enabled": false, "exclude": []string{" *.tmp ", ""}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := tb.brainMgr.Get("notes").Manifest()
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

// PUT /api/config replaces the whole file layer, so it takes only a whole
// config, and only with the etag it was read with. Every refused request
// leaves config.json as it was.
func TestPutConfigRefusesBlindAndPartialWrites(t *testing.T) {
	tb := newTestBridge(t, `, "dangerously_skip_permissions": true`)
	var cur struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &cur); err != nil {
		t.Fatal(err)
	}
	whole := func(edit func(c map[string]any)) map[string]any {
		data, _ := json.Marshal(cur.Config)
		var c map[string]any
		_ = json.Unmarshal(data, &c)
		edit(c)
		return c
	}
	cases := []struct {
		name string
		body any
		want int
		msg  string
	}{
		{"no etag", map[string]any{"config": cur.Config}, http.StatusPreconditionRequired, "etag required"},
		{"stale etag", map[string]any{"etag": "0000", "config": cur.Config}, http.StatusConflict, "changed since"},
		{"no config", map[string]any{"etag": cur.ETag}, http.StatusBadRequest, "config required"},
		{"one setting", map[string]any{"etag": cur.ETag, "config": map[string]any{"log_level": "INFO"}}, http.StatusBadRequest, "allowed_numbers"},
		{"a section left half out", map[string]any{"etag": cur.ETag, "config": whole(func(c map[string]any) { c["sandbox"] = map[string]any{} })}, http.StatusBadRequest, "sandbox.enabled"},
		{"a typo", map[string]any{"etag": cur.ETag, "config": whole(func(c map[string]any) { c["log_levle"] = "debug" })}, http.StatusBadRequest, `unknown field \"log_levle\"`},
		{"a typo in a section", map[string]any{"etag": cur.ETag, "config": whole(func(c map[string]any) { c["queue"] = map[string]any{"mode": "queue", "max_pendng": 3} })}, http.StatusBadRequest, "max_pendng"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := tb.do("PUT", "/api/config", c.body)
			if w.Code != c.want || !strings.Contains(w.Body.String(), c.msg) {
				t.Fatalf("%d %s, want %d with %q", w.Code, w.Body.String(), c.want, c.msg)
			}
			if tb.cfgs.ETag() != cur.ETag {
				t.Fatal("the refused request changed config.json")
			}
		})
	}
	cfg := tb.cfgs.Get()
	if !cfg.DangerouslySkipPermissions || !slices.Equal(cfg.AllowedNumbers, []string{"15551234567"}) {
		t.Fatalf("settings changed: skip %v, allowed %v", cfg.DangerouslySkipPermissions, cfg.AllowedNumbers)
	}

	// The whole config, as read, saves.
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": cur.ETag, "config": whole(func(map[string]any) {})}); w.Code != 200 {
		t.Fatalf("whole config: %d %s", w.Code, w.Body.String())
	}
}

// Every change to a security setting is logged in one line.
func TestConfigUpdateLogsSecurityChanges(t *testing.T) {
	tb := newTestBridge(t, "")
	logged := captureLog(t)
	w := tb.editConfig(t, func(c map[string]any) {
		c["allowed_numbers"] = []string{"15559876543"}
		c["dangerously_skip_permissions"] = true
		c["log_level"] = "debug"
	})
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	got := strings.Join(logged(), "\n")
	for _, want := range []string{"security settings changed", "allowed_numbers +15559876543 -15551234567", `dangerously_skip_permissions "false" → "true"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "log_level") {
		t.Errorf("log names a setting that is not a security one:\n%s", got)
	}
}

// Approving a drift approves the brain.json the dashboard showed: if the
// brain rewrote it since, the approval is refused and nothing changes.
func TestApproveBindsToTheReviewedManifest(t *testing.T) {
	tb := newTestBridge(t, "")
	m := manifest.Manifest{ID: "notes", Name: "Notes"}
	dir := tb.addFolderBrain(t, m)
	harmless := m
	harmless.Name = "Notes 2"
	if err := manifest.Write(dir, &harmless, true); err != nil {
		t.Fatal(err)
	}
	var views []brainView
	_ = json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views)
	if len(views) != 1 || views[0].Disk == nil {
		t.Fatalf("views = %+v", views)
	}
	reviewed := views[0].Disk

	// Between the page load and the click, the brain claims your DMs.
	grab := harmless
	grab.Routing.HandlesDMs = true
	if err := manifest.Write(dir, &grab, true); err != nil {
		t.Fatal(err)
	}
	before := tb.cfgs.ETag()
	if w := tb.do("POST", "/api/brains/notes/approve", map[string]any{"manifest": reviewed}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "changed again") {
		t.Fatalf("approve the stale review: %d %s", w.Code, w.Body.String())
	}
	if tb.cfgs.ETag() != before || tb.brainMgr.Get("notes").Manifest().Routing.HandlesDMs {
		t.Fatal("the refused approval changed the approved manifest")
	}
	if w := tb.do("POST", "/api/brains/notes/approve", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("approve without a manifest: %d %s", w.Code, w.Body.String())
	}

	// Approving what is on disk now works.
	if w := tb.do("POST", "/api/brains/notes/approve", map[string]any{"manifest": grab}); w.Code != 200 {
		t.Fatalf("approve the current file: %d %s", w.Code, w.Body.String())
	}
	if !tb.brainMgr.Get("notes").Manifest().Routing.HandlesDMs {
		t.Fatal("approval did not apply")
	}
}

// TestBrainsMarkTheFallback: the chats no brain claims, and the DMs while
// no brain handles them, go to the default brain, else to the first one,
// which no brain.json names. /api/brains marks that brain, and no other,
// so the dashboard can say whose those chats are: not a second folder that
// claims its id, which does not run.
func TestBrainsMarkTheFallback(t *testing.T) {
	cases := []struct {
		name   string
		brains []manifest.Manifest
		want   string // the fallback's folder
	}{
		{"the first brain, without a default", []manifest.Manifest{
			{ID: "family", Routing: manifest.Routing{Groups: []string{testGroup}}},
			{ID: "work"},
		}, "family"},
		{"the default brain", []manifest.Manifest{
			{ID: "family", Routing: manifest.Routing{HandlesDMs: true}},
			{ID: "work", Routing: manifest.Routing{Default: true}},
		}, "work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, "")
			for _, m := range c.brains {
				m.Name = m.ID
				tb.addFolderBrain(t, m)
			}
			// A second folder with the fallback's brain.json, left out.
			m := manifest.Manifest{ID: c.want, Name: c.want}
			copyDir := filepath.Join(tb.home, "copy")
			if err := os.MkdirAll(copyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Write(copyDir, &m, false); err != nil {
				t.Fatal(err)
			}
			if _, err := tb.cfgs.Update("", func(c *config.Config) error {
				c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: "~/copy", Approved: &m})
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			var views []struct {
				Path     string `json:"path"`
				Fallback bool   `json:"fallback"`
			}
			if err := json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views); err != nil || len(views) != len(c.brains)+1 {
				t.Fatalf("GET /api/brains: %d entries, %v", len(views), err)
			}
			var marked []string
			for _, v := range views {
				if v.Fallback {
					marked = append(marked, filepath.Base(v.Path))
				}
			}
			if !slices.Equal(marked, []string{c.want}) {
				t.Errorf("marked as the fallback: %q, want %q only", marked, c.want)
			}
			if fb := tb.brainMgr.Fallback(); fb == nil || fb.ID() != c.want {
				t.Errorf("Fallback() = %v, want %s", fb, c.want)
			}
		})
	}
}

// GET /api/brains/{id} is that brain's entry of /api/brains, whatever
// state it is in, so the Brain page that follows it shows what the list
// showed; with two folders claiming the id, the first one's.
func TestAPIBrainIsItsEntryOfTheList(t *testing.T) {
	tb := newTestBridge(t, "")
	m := manifest.Manifest{ID: "notes", Name: "Notes"}
	dir := tb.addFolderBrain(t, m)
	tb.addFolderBrain(t, manifest.Manifest{ID: "work", Name: "Work"})
	edited := m
	edited.Name = "Notes 2"
	if err := manifest.Write(dir, &edited, true); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(tb.home, "copy")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(copyDir, &m, false); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.cfgs.Update("", func(c *config.Config) error {
		c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: "~/copy", Approved: &m})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var list []json.RawMessage
	if err := json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &list); err != nil || len(list) != 3 {
		t.Fatalf("GET /api/brains: %d entries, %v", len(list), err)
	}
	cases := []struct {
		id   string
		want int // the entry of the list, or -1 for none
	}{
		{"notes", 0}, // drifted, and claimed by ~/copy too
		{"work", 1},
		{"nobody", -1},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			w := tb.do("GET", "/api/brains/"+c.id, nil)
			if c.want < 0 {
				if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"error"`) {
					t.Fatalf("%d %s, want 404 with an error", w.Code, w.Body.String())
				}
				return
			}
			if got := strings.TrimSpace(w.Body.String()); w.Code != 200 || got != string(list[c.want]) {
				t.Fatalf("%d %s\nwant the list's entry %s", w.Code, got, list[c.want])
			}
		})
	}
	var notes brainView
	_ = json.Unmarshal(list[0], &notes)
	if filepath.Base(notes.Path) != "notes" || notes.Disk == nil {
		t.Fatalf("the notes entry is %+v; want the folder notes, with its edit on disk", notes)
	}
}

// An entry of brain_dirs without an approved manifest still holds its
// folder: a new brain or a restore cannot be put in it, around it or on it.
// Its issues stay on that entry, and it can be removed through the API.
func TestUnapprovedBrainEntries(t *testing.T) {
	tb := newTestBridge(t, `, "brain_dirs": [{"path": "~/a", "approved": null}]`)
	if err := os.MkdirAll(filepath.Join(tb.home, "a", "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"~/a", "~/a/inner"} {
		var got struct{ Problems []string }
		_ = json.Unmarshal(tb.do("POST", "/api/brains/inspect", map[string]string{"path": p}).Body.Bytes(), &got)
		if len(got.Problems) == 0 {
			t.Errorf("inspect %s: no problems", p)
		}
		w := tb.do("POST", "/api/brains", map[string]any{"path": p, "manifest": manifest.Manifest{ID: "x", Name: "X"}})
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("add %s: %d %s", p, w.Code, w.Body.String())
		}
	}
	connectAlias(t, tb)
	if w := tb.do("POST", "/api/backup/restore", map[string]string{"brain": "a", "version": "current", "target": "~/a/restored"}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "inside another brain") {
		t.Errorf("restore into the unapproved folder: %d %s", w.Code, w.Body.String())
	}

	// A hand-edited duplicate of a brain's folder: the issue is the
	// duplicate's, and the brain itself is fine.
	tb.addFolderBrain(t, manifest.Manifest{ID: "notes", Name: "Notes"})
	if _, err := tb.cfgs.Update("", func(c *config.Config) error {
		c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: "~/notes"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var views []brainView
	_ = json.Unmarshal(tb.do("GET", "/api/brains", nil).Body.Bytes(), &views)
	if len(views) != 3 || views[1].ID != "notes" || views[1].Status != "ok" || views[2].Status != "unapproved" {
		t.Fatalf("views = %+v", views)
	}

	// The old way, a placeholder id with ?path=, removes nothing now.
	if w := tb.do("DELETE", "/api/brains/_?path=~/a", nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("remove by placeholder id: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("DELETE", "/api/brain-dirs?path=~/notes", nil); w.Code != 200 {
		t.Fatalf("remove the unapproved duplicate: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("DELETE", "/api/brain-dirs?path=~/notes", nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("remove the approved brain by path: %d %s", w.Code, w.Body.String())
	}
	if w := tb.do("DELETE", "/api/brain-dirs?path="+filepath.Join(tb.home, "a"), nil); w.Code != 200 {
		t.Fatalf("remove ~/a: %d %s", w.Code, w.Body.String())
	}
	refs := tb.cfgs.FileCopy().BrainDirs
	if len(refs) != 1 || refs[0].Approved == nil || refs[0].Approved.ID != "notes" {
		t.Fatalf("brain_dirs = %+v", refs)
	}
	if !dirExists(filepath.Join(tb.home, "a", "inner")) {
		t.Error("removing the entry touched its folder")
	}
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// /api/status and /api/brains report the same issues: a brain.json that
// changed on disk shows in both at once, not only after the next reload.
func TestStatusAndBrainsShareTheIssues(t *testing.T) {
	tb := newTestBridge(t, "")
	dir := tb.addFolderBrain(t, manifest.Manifest{ID: "notes", Name: "Notes"})
	if err := manifest.Write(dir, &manifest.Manifest{ID: "notes", Name: "Changed"}, true); err != nil {
		t.Fatal(err)
	}
	var st struct{ Issues []registry.Issue }
	_ = json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st)
	if len(st.Issues) != 1 || st.Issues[0].Kind != "drift" {
		t.Fatalf("status issues = %+v", st.Issues)
	}
}

// A folder that cannot be a brain is not looked into.
func TestInspectDoesNotProbeRefusedFolders(t *testing.T) {
	tb := newTestBridge(t, "")
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(tb.do("POST", "/api/brains/inspect", map[string]string{"path": outside}).Body.Bytes(), &got)
	if probs, _ := got["problems"].([]any); len(probs) == 0 {
		t.Fatalf("no problems for a folder outside home: %v", got)
	}
	for _, k := range []string{"exists", "is_dir", "entries", "is_git", "manifest", "suggested"} {
		if _, ok := got[k]; ok {
			t.Errorf("response has %q for a refused folder: %v", k, got)
		}
	}
}

// Two adds at once that clash with each other cannot both be registered:
// addMu runs them one after the other, so the second one's checks see the
// first and refuse it.
func TestConcurrentAddsCannotBothClaimAGroup(t *testing.T) {
	tb := newTestBridge(t, "")
	for round := range 8 {
		group := fmt.Sprintf("1203630000000000%02d@g.us", round+10)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("r%d-%d", round, i)
				m := manifest.Manifest{ID: id, Name: id, Routing: manifest.Routing{Groups: []string{group}}}
				codes[i] = tb.do("POST", "/api/brains", map[string]any{"path": "~/" + id, "manifest": m, "create_dir": true}).Code
			}()
		}
		wg.Wait()
		slices.Sort(codes)
		if !slices.Equal(codes, []int{200, http.StatusUnprocessableEntity}) {
			t.Fatalf("round %d: codes %v, want one 200 and one 422", round, codes)
		}
		// The refused brain's folder is not left scaffolded.
		for i := range 2 {
			id := fmt.Sprintf("r%d-%d", round, i)
			if tb.brainMgr.Get(id) == nil && dirExists(filepath.Join(tb.home, id)) {
				t.Fatalf("round %d: the refused brain's folder %s was left behind", round, id)
			}
		}
	}
	for _, is := range tb.brainMgr.Issues() {
		t.Errorf("issue: %+v", is)
	}
}

// addMu keeps adds apart, not the other config writers. A brain saved
// while an add runs, after its first check, that claims the group the new
// brain claims refuses the add: it is checked again inside its config
// update, and its folder is not left scaffolded.
func TestAddBrainRechecksAgainstOtherWriters(t *testing.T) {
	const group = "120363000000000000@g.us"
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	// The other writer, as PUT /api/brains/a is, holds the config update
	// until the add has passed its first check, then gives a the group.
	inside, release, saved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := tb.cfgs.Update("", func(c *config.Config) error {
			close(inside)
			<-release
			for _, ref := range c.BrainDirs {
				if ref.Approved != nil && ref.Approved.ID == "a" {
					ref.Approved.Routing.Groups = []string{group}
				}
			}
			return nil
		})
		saved <- err
	}()
	<-inside
	added := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		m := manifest.Manifest{ID: "b", Name: "B", Routing: manifest.Routing{Groups: []string{group}}}
		added <- tb.do("POST", "/api/brains", map[string]any{"path": "~/b", "manifest": m, "create_dir": true})
	}()
	// It scaffolds only after its first check passed; brain.json waits for
	// the update that registers the brain.
	testutil.WaitFor(t, 5*time.Second, "the add's scaffold", func() bool { return fsx.Exists(filepath.Join(tb.home, "b", manifest.DefaultPromptFile)) })
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if w := <-added; w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("add: %d %s, want 422", w.Code, w.Body.String())
	}
	for _, ref := range tb.cfgs.Get().BrainDirs {
		if ref.Approved != nil && ref.Approved.ID == "a" && !slices.Contains(ref.Approved.Routing.Groups, group) {
			t.Error("brain a lost the group")
		}
	}
	if tb.brainMgr.Get("b") != nil || dirExists(filepath.Join(tb.home, "b")) {
		t.Error("the refused brain was registered or its folder left behind")
	}
}

// Adds of the same new folder at once register one brain, and its folder
// keeps what the scaffold made: an add that lost must not take back files
// it happened to create first, such as brain.json, that the winner counts
// on.
func TestConcurrentAddsOfOneFolder(t *testing.T) {
	tb := newTestBridge(t, "")
	for round := range 8 {
		path := fmt.Sprintf("~/race-%d", round)
		type result struct {
			code int
			id   string
		}
		results := make([]result, 6)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := manifest.Manifest{ID: fmt.Sprintf("race-%d-%d", round, i), Name: "Race"}
				w := tb.do("POST", "/api/brains", map[string]any{"path": path, "manifest": m, "create_dir": true})
				var resp struct{ ID string }
				_ = json.Unmarshal(w.Body.Bytes(), &resp)
				results[i] = result{w.Code, resp.ID}
			}()
		}
		wg.Wait()
		var won []string
		for _, r := range results {
			if r.code == http.StatusOK {
				won = append(won, r.id)
			}
		}
		if len(won) != 1 {
			t.Fatalf("round %d: %d adds succeeded (%v), want 1: %v", round, len(won), won, results)
		}
		dir := filepath.Join(tb.home, fmt.Sprintf("race-%d", round))
		m, err := manifest.Read(dir)
		if err != nil {
			t.Fatalf("round %d: the added brain's folder has no brain.json: %v", round, err)
		}
		if m.ID != won[0] {
			t.Fatalf("round %d: brain.json is brain %q's, but %q was added", round, m.ID, won[0])
		}
		if _, err := os.Stat(filepath.Join(dir, m.Normalized().PromptFile)); err != nil {
			t.Fatalf("round %d: the added brain's prompt file is gone: %v", round, err)
		}
	}
}

// A brain that could not be registered leaves no scaffold behind, so the
// same folder can be added again.
func TestFailedAddRemovesTheScaffold(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	tb := newTestBridge(t, "")
	existing := filepath.Join(tb.home, "existing")
	if err := os.MkdirAll(filepath.Join(existing, "memories"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "memories", "mine.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Dir(tb.cfgs.Path())
	if err := os.Chmod(cfgDir, 0o500); err != nil { // config.json cannot be written
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })
	// create_dir makes the folders above the brain's too.
	for _, p := range []string{"~/fresh", "~/existing", "~/new/deeper/fresh", "~/existing/sub/fresh"} {
		w := tb.do("POST", "/api/brains", map[string]any{"path": p, "manifest": manifest.Manifest{ID: "x", Name: "X"}, "create_dir": true})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("add %s with config.json read-only: %d %s", p, w.Code, w.Body.String())
		}
	}
	for _, d := range []string{"fresh", "new"} {
		if dirExists(filepath.Join(tb.home, d)) {
			t.Errorf("the folder ~/%s made for the brain was left behind", d)
		}
	}
	entries, _ := os.ReadDir(existing)
	if len(entries) != 1 || entries[0].Name() != "memories" {
		t.Errorf("existing folder has %v, want only its own memories", entries)
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "memories", "mine.md")); string(data) != "keep" {
		t.Error("a file that was there before was touched")
	}
	if err := os.Chmod(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := tb.do("POST", "/api/brains", map[string]any{"path": "~/existing", "manifest": manifest.Manifest{ID: "x", Name: "X"}}); w.Code != 200 {
		t.Fatalf("add again: %d %s", w.Code, w.Body.String())
	}
}

// The Chats page polls the groups and reloads them after each change on
// it: those take the list the WhatsApp client keeps, and only its Refresh
// button, with ?fresh=1, has the client ask WhatsApp again.
func TestGroupsAskWhatsAppOnlyWhenFresh(t *testing.T) {
	tb := newTestBridge(t, "")
	fl := &fakeLister{}
	tw := newTestWhatsApp(t, func(opts *whatsapp.Options) { opts.Groups = fl })
	tb.wa = tw.Client
	steps := []struct {
		path    string
		queries int32 // asked of WhatsApp so far
	}{
		{"/api/whatsapp/groups", 1},
		{"/api/whatsapp/groups", 1},
		{"/api/whatsapp/groups?fresh=0", 1},
		{"/api/whatsapp/groups?fresh=1", 2},
		{"/api/whatsapp/groups", 2},
	}
	for i, s := range steps {
		w := tb.do("GET", s.path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("step %d: GET %s = %d %s", i+1, s.path, w.Code, w.Body)
		}
		var rows []struct {
			JID     string `json:"jid"`
			Allowed bool   `json:"allowed"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].JID != "120363000000000000@g.us" || !rows[0].Allowed {
			t.Fatalf("step %d: GET %s = %s (%v), want the allowed group", i+1, s.path, w.Body, err)
		}
		if n := fl.calls.Load(); n != s.queries {
			t.Errorf("step %d: after GET %s WhatsApp was asked %d times, want %d", i+1, s.path, n, s.queries)
		}
	}
}

// What the dashboard polls every few seconds carries an ETag, and a poll
// that sends it back while nothing changed gets a 304 without a body; once
// something changed, the new answer with a new ETag.
func TestPolledEndpointsAnswer304WhileUnchanged(t *testing.T) {
	tb := newTestBridge(t, "")
	dir := tb.addFolderBrain(t, manifest.Manifest{ID: "notes", Name: "Notes"})
	say := func(text string) func() {
		return func() {
			tb.activity.Add("notes", history.Entry{TS: "2026-10-18T10:00:00Z", From: "15551234567", Chat: "15551234567@s.whatsapp.net", Text: text, Type: history.In})
		}
	}
	say("hello")()
	cases := []struct {
		path   string
		change func()
	}{
		{"/api/activity?limit=30", say("again")},
		{"/api/activity?brain=notes", say("once more")},
		{"/api/brains", func() {
			if err := manifest.Write(dir, &manifest.Manifest{ID: "notes", Name: "Notes 2"}, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"/api/brains/notes", func() {
			if err := manifest.Write(dir, &manifest.Manifest{ID: "notes", Name: "Notes 3"}, true); err != nil {
				t.Fatal(err)
			}
		}},
	}
	ifNoneMatch := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("If-None-Match", v) }
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			first := tb.do("GET", c.path, nil)
			etag := first.Header().Get("ETag")
			if first.Code != 200 || !strings.HasPrefix(etag, `"`) || first.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("first poll: %d, ETag %q, Content-Type %q", first.Code, etag, first.Header().Get("Content-Type"))
			}
			for _, sent := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
				w := tb.do("GET", c.path, nil, ifNoneMatch(sent))
				if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != etag {
					t.Errorf("If-None-Match %s: %d, ETag %q, body %q; want 304, the same ETag, no body", sent, w.Code, w.Header().Get("ETag"), w.Body.String())
				}
			}
			if w := tb.do("GET", c.path, nil, ifNoneMatch(`"other"`)); w.Code != 200 || w.Body.String() != first.Body.String() {
				t.Errorf("another ETag: %d %s; want the answer", w.Code, w.Body.String())
			}

			c.change()
			w := tb.do("GET", c.path, nil, ifNoneMatch(etag))
			if w.Code != 200 || w.Header().Get("ETag") == etag || w.Body.String() == first.Body.String() {
				t.Fatalf("after a change: %d, ETag %q (was %q); want the new answer with a new ETag", w.Code, w.Header().Get("ETag"), etag)
			}
		})
	}
}

// A limit above the maximum gets the maximum, not the default.
func TestListLimitsClampToTheMaximum(t *testing.T) {
	tb := newTestBridge(t, "")
	for i := range 600 {
		fmt.Fprintf(tb.logs, "line %d\n", i)
		tb.activity.Add("a", history.Entry{TS: "2026-10-18T10:00:00Z", From: "a", Chat: "15551234567@s.whatsapp.net", Text: fmt.Sprint(i), Type: history.In})
	}
	count := map[string]func([]byte) int{
		"/api/logs": func(b []byte) int {
			var logs struct{ Lines []logx.Line }
			_ = json.Unmarshal(b, &logs)
			return len(logs.Lines)
		},
		"/api/activity": func(b []byte) int {
			var entries []activity.Entry
			_ = json.Unmarshal(b, &entries)
			return len(entries)
		},
	}
	for _, c := range []struct {
		path string
		want int // at least
	}{
		{"/api/logs", 600},     // every line: the maximum is 2000
		{"/api/activity", 500}, // the maximum, not the default of 100
	} {
		t.Run(c.path, func(t *testing.T) {
			if n := count[c.path](tb.do("GET", c.path+"?limit=100000", nil).Body.Bytes()); n < c.want {
				t.Fatalf("%s?limit=100000 gave %d, want at least %d", c.path, n, c.want)
			}
		})
	}
}

// A config that lists a number or group that can match no chat still
// loads, with a warning that names the entries. A number written with a
// "+" and spaces matches its chat, and is not named.
func TestConfigWarnsAboutOddChats(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logged := captureLog(t)
	path := writeConfig(t, `{"data_dir": "`+t.TempDir()+`", "allowed_numbers": ["15551234567", "+1 555 987 6543", "call me"], "allowed_groups": ["120363000000000000@g.us", " 120363000000000001@g.us", "@g.us"]}`)
	if _, err := openConfigStore(path); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(logged(), "\n")
	for _, want := range []string{
		`allowed_numbers "call me" (digits`,
		`allowed_groups " 120363000000000001@g.us" (write it as 120363000000000001@g.us)`,
		`allowed_groups "@g.us" (digits@g.us)`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"15551234567"`) || strings.Contains(got, `"+1 555 987 6543"`) || strings.Contains(got, `"120363000000000000@g.us"`) {
		t.Errorf("log names a valid entry:\n%s", got)
	}
}

// A folder dialog that was cancelled is a cancel; one that failed is an
// error, so a dialog that cannot open does not pass for a cancel.
func TestRunPickerTellsCancelFromFailure(t *testing.T) {
	cases := []struct {
		name, tool, script string
		want               string // a path, "cancel", or part of the error
	}{
		{"chosen", "osascript", `echo "/Users/you/brains/notes/"`, "/Users/you/brains/notes"},
		{"osascript cancel", "osascript", `echo "execution error: User canceled. (-128)" >&2; exit 1`, "cancel"},
		{"osascript refused", "osascript", `echo "Not authorized to send Apple events to System Events. (-1743)" >&2; exit 1`, "Not authorized"},
		{"zenity cancel", "zenity", `exit 1`, "cancel"},
		{"zenity failure", "zenity", `echo "cannot open display" >&2; exit 255`, "cannot open display"},
		{"kdialog cancel", "kdialog", `exit 1`, "cancel"},
		{"nothing printed", "kdialog", `exit 0`, "cancel"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := filepath.Join(t.TempDir(), c.tool)
			if err := os.WriteFile(tool, []byte("#!/bin/sh\n"+c.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			got, err := runPicker(context.Background(), []string{tool})
			switch {
			case c.want == "cancel":
				if !errors.Is(err, errPickerCancelled) {
					t.Fatalf("got %q, %v; want a cancel", got, err)
				}
			case strings.HasPrefix(c.want, "/"):
				if err != nil || got != c.want {
					t.Fatalf("got %q, %v; want %q", got, err, c.want)
				}
			default:
				if err == nil || errors.Is(err, errPickerCancelled) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("got %q, %v; want an error with %q", got, err, c.want)
				}
			}
		})
	}
}

// A sign-in finishes with the client and redirect URI it started with:
// an http_listen saved since waits for a restart, and a client replaced
// meanwhile did not issue the code.
func TestSignInKeepsItsClientAndRedirect(t *testing.T) {
	srv, got := fakeGoogle(t)
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "fam", Name: "Fam"})
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var start struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(tb.do("POST", "/api/brains/fam/calendar/connect", nil).Body.Bytes(), &start)
	u, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	const served = "http://127.0.0.1:8765/"
	if r := u.Query().Get("redirect_uri"); r != served {
		t.Fatalf("redirect_uri %q, want the address served on, %q", r, served)
	}

	// Before Google sends the browser back: a new listen address (for
	// the next start) and another client.
	if w := tb.editConfig(t, func(c map[string]any) { c["http_listen"] = "127.0.0.1:9999" }); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	other := strings.Replace(fmt.Sprintf(testClientJSON, srv.URL), "123-abc", "456-def", 1)
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": other}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var next struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(tb.do("POST", "/api/brains/fam/calendar/connect", nil).Body.Bytes(), &next)
	if n, _ := url.Parse(next.AuthURL); n == nil || n.Query().Get("redirect_uri") != served {
		t.Fatalf("a sign-in started after the edit redirects to %q, want %q", next.AuthURL, served)
	}

	r := httptest.NewRequest("GET", "http://127.0.0.1:8765/?state="+url.QueryEscape(u.Query().Get("state"))+"&code=good-code", nil)
	r.Host = "127.0.0.1:8765"
	w := httptest.NewRecorder()
	tb.h.ServeHTTP(w, r)
	if w.Code != 302 {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	if (*got)["redirect_uri"] != served || (*got)["client_id"] != "123-abc.apps.googleusercontent.com" {
		t.Fatalf("exchange sent redirect_uri %q and client %q", (*got)["redirect_uri"], (*got)["client_id"])
	}
}

func TestTokenLifetime(t *testing.T) {
	for _, c := range []struct {
		in   int
		want time.Duration
	}{{3599, 3599 * time.Second}, {0, time.Hour}, {-5, time.Hour}} {
		if got := tokenLifetime(c.in); got != c.want {
			t.Errorf("tokenLifetime(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}
