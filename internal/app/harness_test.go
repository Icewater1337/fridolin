package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/registry"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// harness is a dev-mode bridge driven through what it offers from outside:
// the HTTP API, the dev inbound and outbox, the files it writes.
// End-to-end tests use it, so they keep working while the code behind the
// API moves between packages. Tests of a component's insides use
// newTestBridge, which hands out the App itself.
//
// The harness never calls App.Start: there is no listener, no WhatsApp
// connect loop, no startup checks and no backup, Plaud or site schedulers.
// The goldens therefore show a bridge before any background job has run,
// which keeps them the same from run to run; TestSmoke is the test that
// starts one.
//
// Three places still reach inside, and the moves that change what they
// use must change them too: newHarness and Reopen build the bridge with
// New and newHTTPHandler, Reopen takes up what the last stop kept
// with App.resume, as Start does, and WhatsApp hands an event to a
// WhatsApp client built through its normal constructor, with the bridge's
// handleInbound and dev transport.
type harness struct {
	t *testing.T
	// b is the bridge, for the harness's own setup and shutdown only.
	b    *App
	h    http.Handler
	root string
	home string
	cfg  string
	// stop stops the bridge once, whether a test or the cleanup asks.
	stop func()
	// sentBefore is what the bridges before a Reopen sent, oldest first.
	sentBefore []sent
}

// harnessGroup is the group the harness's config allows, and harnessDM the
// number.
const (
	harnessGroup = "120363000000000000@g.us"
	harnessDM    = "15551234567@s.whatsapp.net"
)

// newHarness starts a dev-mode bridge in a temp folder: HOME is
// <root>/home, the data dir ~/.fridolin, and the Claude CLI is this test
// binary (see TestMain). cfgJSON is a JSON object whose keys replace those
// of the base config, which allows harnessDM and harnessGroup and turns
// the debounce off; "" or "{}" keeps the base config.
func newHarness(t *testing.T, cfgJSON string) *harness {
	t.Helper()
	root := fsx.Canonical(t.TempDir())
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	cfg := map[string]any{
		"data_dir":        filepath.Join(home, ".fridolin"),
		"claude_path":     fakeClaudeEnv(t, 10),
		"allowed_numbers": []string{chat.NormalizeJID(harnessDM)},
		"allowed_groups":  []string{harnessGroup},
		"debounce":        map[string]int{"quiet_ms": 0},
	}
	if cfgJSON != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(cfgJSON), &extra); err != nil {
			t.Fatalf("newHarness: the config is not a JSON object: %v", err)
		}
		maps.Copy(cfg, extra)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfgs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())
	b, err := New(Options{Config: cfgs, NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	b.outbox.Start()
	h := &harness{t: t, b: b, h: b.newHTTPHandler(config.DefaultHTTPListen), root: root, home: home, cfg: path, stop: sync.OnceFunc(b.Stop)}
	t.Cleanup(h.stop)
	return h
}

// Shutdown stops the bridge as SIGTERM does. The API still answers what it
// can from memory afterwards, such as the dev outbox.
func (h *harness) Shutdown() { h.stop() }

// Restart stops the bridge as SIGTERM does, then starts a new one on the
// same config and data dir, as the service does after it (Reopen).
func (h *harness) Restart() {
	h.t.Helper()
	h.Shutdown()
	h.Reopen()
}

// Reopen starts a new bridge on the config and data dir of the one
// Shutdown stopped, and takes up what that one kept for the next start,
// as App.Start does before WhatsApp connects. Outbox and SentTo go on
// to list what the stopped bridges sent, then what the new one sends.
func (h *harness) Reopen() {
	h.t.Helper()
	h.sentBefore = h.Outbox()
	cfgs, err := OpenConfigStore(h.cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	b, err := New(Options{Config: cfgs, NoWhatsApp: true})
	if err != nil {
		h.t.Fatal(err)
	}
	b.outbox.Start()
	h.b, h.h, h.stop = b, b.newHTTPHandler(config.DefaultHTTPListen), sync.OnceFunc(b.Stop)
	h.t.Cleanup(h.stop)
	b.resume(context.Background())
}

// Do sends a request the way the dashboard does: with the token, and on
// writes the dashboard header and a JSON body. mutate adjusts it first.
func (h *harness) Do(method, path string, body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, "http://"+config.DefaultHTTPListen+path, rd)
	r.Header.Set("X-Bridge-Token", h.token())
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("X-Bridge-Dashboard", "1")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

// Script sends a request the way a local script does: a JSON body and the
// scripts token from the data dir, no dashboard token or header. mutate
// adjusts it first.
func (h *harness) Script(method, path, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, "http://"+config.DefaultHTTPListen+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+h.readToken("scripts.token"))
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

// token is the dashboard token, read from the data dir as `fridolin open`
// reads it.
func (h *harness) token() string {
	h.t.Helper()
	return h.readToken("dashboard.token")
}

// readToken reads a token the bridge keeps in its data dir.
func (h *harness) readToken(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.home, ".fridolin", name))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// inbound is a message for the dev inbound, POST /api/dev/inbound.
type inbound struct {
	JID    string   `json:"jid"`
	Sender string   `json:"sender,omitempty"`
	Text   string   `json:"text"`
	Media  []string `json:"media,omitempty"`
	Silent bool     `json:"silent,omitempty"`
	Brain  string   `json:"brain,omitempty"`
}

// Inject hands the bridge a message as if it had come from WhatsApp, and
// fails the test unless the bridge takes it.
func (h *harness) Inject(m inbound) {
	h.t.Helper()
	if w := h.Do("POST", "/api/dev/inbound", m); w.Code != http.StatusAccepted {
		h.t.Fatalf("dev inbound %+v: %d %s", m, w.Code, w.Body.String())
	}
}

// WhatsApp hands the bridge a message event as whatsmeow delivers one, to
// the bridge's WhatsApp handling without a connection: what the dev inbound
// cannot carry, such as a quoted reply, an edit or a deletion, goes this
// way, as does a message that must come from WhatsApp, as those a stop
// keeps for the next start do. Attachments cannot
// be downloaded; what the bridge tells the chat about that is sent through
// the dev outbox.
func (h *harness) WhatsApp(ev *events.Message) {
	h.t.Helper()
	h.whatsApp(ev, h.b.handleInbound)
}

// WhatsAppAndWait hands an inbound message to the normal WhatsApp adapter
// and waits until the bridge has handled it. Unlike WhatsApp, this is only
// for events that become inbound messages, not edits, deletions or ignored
// chatter. It lets a test stop the bridge after intake without inspecting
// the debouncer or the brain's private queue.
func (h *harness) WhatsAppAndWait(ev *events.Message) {
	h.t.Helper()
	handled := make(chan struct{})
	h.whatsApp(ev, func(ctx context.Context, m brain.Inbound) {
		h.b.handleInbound(ctx, m)
		close(handled)
	})
	h.WaitFor("WhatsApp intake to finish", func() bool {
		select {
		case <-handled:
			return true
		default:
			return false
		}
	})
}

func (h *harness) whatsApp(ev *events.Message, handle InboundHandler) {
	h.t.Helper()
	opts := whatsAppOptions(func() config.Settings { return RuntimeSettings(h.b.cfgs) }, h.b.store, handle, nil)
	opts.Container = fixedDeviceContainer{&store.Device{}}
	opts.Log = waLog.Noop
	opts.OnChange = h.b.handleChange
	opts.Downloader = (*whatsmeow.Client)(nil)
	opts.Notify = func(jid, text string) { _ = h.b.dev.SendMessage(context.Background(), jid, text) }
	wa, err := whatsapp.New(opts)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(wa.Disconnect)
	wa.ProcessMessage(ev)
}

// sent is a message the dev-mode bridge sent, as GET /api/dev/outbox lists
// it.
type sent struct {
	TS   time.Time `json:"ts"`
	JID  string    `json:"jid"`
	Text string    `json:"text"`
	Poll []string  `json:"poll"`
}

// Outbox returns what the bridge has sent, oldest first, after what the
// bridges before a Reopen sent.
func (h *harness) Outbox() []sent {
	h.t.Helper()
	w := h.Do("GET", "/api/dev/outbox", nil)
	var out []sent
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("dev outbox: %d %s", w.Code, w.Body.String())
	}
	// The API lists the newest first.
	slices.Reverse(out)
	return append(slices.Clone(h.sentBefore), out...)
}

// SentTo returns the texts sent to jid, oldest first.
func (h *harness) SentTo(jid string) []string {
	h.t.Helper()
	var out []string
	for _, m := range h.Outbox() {
		if m.JID == jid {
			out = append(out, m.Text)
		}
	}
	return out
}

// AddBrain adds a brain in a new folder ~/<id> through the API, as the Add
// brain page does, and returns the folder.
func (h *harness) AddBrain(m manifest.Manifest) string {
	h.t.Helper()
	// A manifest that sets no backup goes without the key, so the brain
	// gets the defaults the page starts with: backed up, with the default
	// exclusions. BrainManifest itself would send backups off.
	var manifest any = m
	if !m.Backup.Enabled && m.Backup.Exclude == nil {
		var obj map[string]json.RawMessage
		data, err := json.Marshal(m)
		if err == nil {
			err = json.Unmarshal(data, &obj)
		}
		if err != nil {
			h.t.Fatal(err)
		}
		delete(obj, "backup")
		manifest = obj
	}
	if w := h.Do("POST", "/api/brains", map[string]any{"path": "~/" + m.ID, "manifest": manifest, "create_dir": true}); w.Code != http.StatusOK {
		h.t.Fatalf("add brain %s: %d %s", m.ID, w.Code, w.Body.String())
	}
	return filepath.Join(h.home, m.ID)
}

// Answer hands the bridge m and returns the next message the bridge sends
// to m's chat. The fake CLI's answer starts with echo:<folder>:, which says
// which brain ran.
func (h *harness) Answer(m inbound) string {
	h.t.Helper()
	n := len(h.SentTo(m.JID))
	h.Inject(m)
	h.WaitFor("the answer in "+m.JID, func() bool { return len(h.SentTo(m.JID)) > n })
	return h.SentTo(m.JID)[n]
}

// Approved returns the approved copy of each brain's brain.json, by id, as
// config.json holds them.
func (h *harness) Approved() map[string]manifest.Manifest {
	h.t.Helper()
	var file struct {
		BrainDirs []config.BrainRef `json:"brain_dirs"`
	}
	data, err := os.ReadFile(h.cfg)
	if err != nil || json.Unmarshal(data, &file) != nil {
		h.t.Fatalf("config.json: %v\n%s", err, data)
	}
	out := map[string]manifest.Manifest{}
	for _, ref := range file.BrainDirs {
		if ref.Approved != nil {
			out[ref.Approved.ID] = *ref.Approved
		}
	}
	return out
}

// Issues returns the problems with brains that GET /api/status reports.
func (h *harness) Issues() []registry.Issue {
	h.t.Helper()
	var st struct {
		Issues []registry.Issue `json:"issues"`
	}
	if w := h.Do("GET", "/api/status", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		h.t.Fatalf("GET /api/status: %d %s", w.Code, w.Body.String())
	}
	return st.Issues
}

// Running reports whether brain id is answering a message, as GET
// /api/status says.
func (h *harness) Running(id string) bool {
	h.t.Helper()
	var st struct {
		Brains []struct {
			ID      string          `json:"id"`
			Running json.RawMessage `json:"running"`
		} `json:"brains"`
	}
	if w := h.Do("GET", "/api/status", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		h.t.Fatalf("GET /api/status: %d %s", w.Code, w.Body.String())
	}
	for _, b := range st.Brains {
		if b.ID == id {
			return len(b.Running) > 0 && string(b.Running) != "null"
		}
	}
	h.t.Fatalf("GET /api/status lists no brain %q", id)
	return false
}

// WaitFor polls cond until it holds, for up to ten seconds.
func (h *harness) WaitFor(what string, cond func() bool) {
	h.t.Helper()
	testutil.WaitFor(h.t, 10*time.Second, what, cond)
}

// WaitSent waits until jid got a message containing substr and returns it.
func (h *harness) WaitSent(jid, substr string) string {
	h.t.Helper()
	var got string
	h.WaitFor("a message to "+jid+" containing "+substr, func() bool {
		for _, text := range h.SentTo(jid) {
			if strings.Contains(text, substr) {
				got = text
				return true
			}
		}
		return false
	})
	return got
}

// Home is the HOME the bridge runs with.
func (h *harness) Home() string { return h.home }

// Root is the test's temp folder, which holds HOME and the config.
func (h *harness) Root() string { return h.root }

// DataDir is the bridge's data dir, ~/.fridolin.
func (h *harness) DataDir() string { return filepath.Join(h.home, ".fridolin") }

// configView is GET /api/config.
type configView struct {
	ETag      string          `json:"etag"`
	Path      string          `json:"path"`
	Config    json.RawMessage `json:"config"`
	Effective json.RawMessage `json:"effective"`
}

// Config returns the config as GET /api/config reports it.
func (h *harness) Config() configView {
	h.t.Helper()
	w := h.Do("GET", "/api/config", nil)
	var v configView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		h.t.Fatalf("GET /api/config: %d %s", w.Code, w.Body.String())
	}
	return v
}

// scrubber masks what differs between runs in this harness's output:
// besides paths and times, the ETag (a hash of a file with paths in it),
// counters and durations, and what says which system runs the tests.
func (h *harness) scrubber() *scrubber {
	return newScrubber(h.root).maskKeys("etag", "uptime_s", "pid", "boot", "next", "seconds", "platform", "sandbox.supported")
}

func TestHarnessAnswersADM(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	h.Inject(inbound{JID: harnessDM, Text: "hello"})
	if got := h.WaitSent(harnessDM, "hello"); got != "echo:"+dir+":"+chat.NormalizeJID(harnessDM)+" says: hello" {
		t.Errorf("reply = %q", got)
	}
	if h.Config().Path != filepath.Join(h.Root(), "config.json") {
		t.Errorf("config path = %q", h.Config().Path)
	}
}
