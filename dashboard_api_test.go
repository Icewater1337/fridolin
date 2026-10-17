package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
