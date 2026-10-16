package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testBridge struct {
	*Bridge
	h    http.Handler
	home string
}

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
		{"cross-site origin", "POST", "/api/brains/migrate", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, 403},
		{"cross-site fetch", "POST", "/api/brains/migrate", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"no dashboard header", "POST", "/api/brains/migrate", func(r *http.Request) { r.Header.Del("X-Bridge-Dashboard") }, 403},
		{"form post", "POST", "/api/brains/migrate", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"same origin", "POST", "/api/brains/migrate", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8765") }, 200},
	}
	for _, c := range cases {
		var body any
		if c.method == "POST" {
			body = map[string]bool{"dry_run": true}
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

func TestIngestConfinedToMediaDir(t *testing.T) {
	tb := newTestBridge(t, `, "ingest": {"enabled": true}, "brains": [{"name": "personal", "work_dir": "~/p", "handles_dms": true}]`)
	cfg := tb.cfgs.Get()
	inside := filepath.Join(cfg.MediaDir, "upload", "scan.pdf")
	_ = os.MkdirAll(filepath.Dir(inside), 0o700)
	_ = os.WriteFile(inside, []byte("%PDF"), 0o600)
	outside := filepath.Join(tb.home, "secret.txt")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	link := filepath.Join(cfg.MediaDir, "link.txt")
	_ = os.Symlink(outside, link)

	ingest := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/ingest", strings.NewReader(body))
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		tb.h.ServeHTTP(w, r)
		return w
	}
	w := ingest(`{"scope":"personal","paths":["` + inside + `"],"text":"file this"}`)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"accepted"`) || !strings.Contains(w.Body.String(), "15551234567@s.whatsapp.net") {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"outside media": `{"scope":"personal","paths":["` + outside + `"]}`,
		"symlink out":   `{"scope":"personal","paths":["` + link + `"]}`,
		"relative":      `{"scope":"personal","paths":["upload/scan.pdf"]}`,
		"unknown brain": `{"scope":"nobody","paths":["` + inside + `"]}`,
	} {
		if w := ingest(body); w.Code != 400 {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	waitFor(t, 5*time.Second, "ingest reply", func() bool { return len(tb.dev.Outbox()) > 0 })
	hist, _ := os.ReadFile(filepath.Join(tb.home, "p", "conversation-history.jsonl"))
	if !strings.Contains(string(hist), "[attachment: "+canonicalDir(inside)+"]") {
		t.Errorf("history does not record the ingested file:\n%s", hist)
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
	if !contains(tb.cfgs.Get().AllowedGroups, "120363000000000001@g.us") {
		t.Error("group not allowed")
	}
	if br := tb.brainMgr.BrainFor("120363000000000001@g.us", true); br == nil || br.ID() != "family" {
		t.Error("group not routed to the chosen brain")
	}
	if disk, _ := ReadManifest(dir); !contains(disk.Routing.Groups, "120363000000000001@g.us") {
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
	if acts := tb.activity.Recent(10, "family"); len(acts) < 2 {
		t.Errorf("activity = %+v", acts)
	}

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
	if w := tb.do("POST", "/api/brains/notes/approve", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if tb.brainMgr.Get("notes").Name() != "Notes 2" {
		t.Error("approval did not apply")
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

func TestPutConfigKeepsBrainsAndReportsRestart(t *testing.T) {
	tb := newTestBridge(t, `, "brains": [{"name": "p", "work_dir": "~/p"}]`)
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
	if len(tb.cfgs.FileCopy().Brains) != 1 {
		t.Error("saving settings must not drop brains")
	}
	if !tb.cfgs.Get().Triggers.Matches("@c hi") || tb.cfgs.Get().Triggers.Matches("@carla") {
		t.Error("trigger change not applied live")
	}
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": got.ETag, "config": c}); w.Code != 409 {
		t.Errorf("stale etag: %d, want 409", w.Code)
	}
}

func TestAddBrainBackupDefaults(t *testing.T) {
	tb := newTestBridge(t, "")
	// No backup settings given: backed up, with the default exclusions.
	_ = tb.do("POST", "/api/brains", map[string]any{"path": "~/a", "create_dir": true, "manifest": BrainManifest{ID: "a", Name: "A"}})
	// Explicitly off stays off.
	off := BrainManifest{ID: "b", Name: "B", Backup: BrainBackup{Enabled: false, Exclude: []string{"*.log"}}}
	_ = tb.do("POST", "/api/brains", map[string]any{"path": "~/b", "create_dir": true, "manifest": off})
	a, _ := ReadManifest(filepath.Join(tb.home, "a"))
	b, _ := ReadManifest(filepath.Join(tb.home, "b"))
	if !a.Backup.Enabled || len(a.Backup.Exclude) == 0 {
		t.Errorf("a.backup = %+v, want on with defaults", a.Backup)
	}
	if b.Backup.Enabled || len(b.Backup.Exclude) != 1 {
		t.Errorf("b.backup = %+v, want off as requested", b.Backup)
	}
}

func TestOTPGuardHandsCodeToLocalScripts(t *testing.T) {
	tb := newTestBridge(t, `, "otp_guard": true, "otp_markers": ["Examplebank 2FA"], "brains": [{"name": "p", "work_dir": "~/p", "handles_dms": true}]`)
	// A local script asks for a code through /send...
	r := httptest.NewRequest("POST", "http://127.0.0.1:8765/send", strings.NewReader(`{"jid":"15551234567@s.whatsapp.net","text":"Examplebank 2FA: reply with the code"}`))
	r.Host = "127.0.0.1:8765"
	tb.h.ServeHTTP(httptest.NewRecorder(), r)
	// ...and the reply arrives.
	if w := tb.do("POST", "/api/dev/inbound", map[string]any{"jid": "15551234567@s.whatsapp.net", "text": " 483920 "}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	otp := filepath.Join(tb.cfgs.Get().DataDir, "otp", "p.json")
	waitFor(t, 3*time.Second, "otp file", func() bool { _, err := os.Stat(otp); return err == nil })
	data, _ := os.ReadFile(otp)
	var got map[string]string
	_ = json.Unmarshal(data, &got)
	if got["code"] != "483920" || got["chat"] != "15551234567@s.whatsapp.net" {
		t.Errorf("otp file = %s", data)
	}
	if st, _ := os.Stat(otp); st.Mode().Perm() != 0o600 {
		t.Errorf("otp file mode %v", st.Mode().Perm())
	}
	hist, _ := os.ReadFile(filepath.Join(tb.home, "p", "conversation-history.jsonl"))
	if strings.Contains(string(hist), "483920") {
		t.Error("the code must not be written to the conversation history")
	}
	time.Sleep(200 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 1 || !strings.Contains(out[0].Text, "Examplebank 2FA") {
		t.Errorf("outbox = %+v — only the script's own request may be there; the brain must not run on a caught code", out)
	}
}
