package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/otp"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// A save changes what the next process opens. Every active writer and the
// brain's environment keep using the running paths in the meantime.
func TestRestartOnlyPathsStayAtBootAfterAPISave(t *testing.T) {
	h := newHarness(t, `{"otp_guard":true,"otp_markers":["Examplebank 2FA"],"ingest":{"enabled":true}}`)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	boot := h.DataDir()
	next := filepath.Join(h.Home(), "next-start")
	page := h.Config()
	var file map[string]any
	if err := json.Unmarshal(page.Config, &file); err != nil {
		t.Fatal(err)
	}
	file["data_dir"], file["db_path"], file["media_dir"] = next, filepath.Join(next, "state.db"), filepath.Join(next, "inbox")
	file["http_listen"] = "127.0.0.1:9876"
	w := h.Do("PUT", "/api/config", map[string]any{"etag": page.ETag, "config": file})
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var saved config.Config
	if err := json.Unmarshal(h.Config().Effective, &saved); err != nil || saved.DataDir != next {
		t.Fatalf("API lost next-start projection: %s, %v", h.Config().Effective, err)
	}
	var status struct {
		DataDir string   `json:"data_dir"`
		Restart []string `json:"restart_required"`
	}
	if w := h.Do("GET", "/api/status", nil); json.Unmarshal(w.Body.Bytes(), &status) != nil || status.DataDir != boot || !slices.Contains(status.Restart, "data_dir") {
		t.Fatalf("running status = %s", w.Body.String())
	}
	// Changing a live schedule writes its state into the original backup directory.
	if w := h.Do("PUT", "/api/backup/settings", map[string]any{"enabled": true, "daily_at": "04:15"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(boot, "backup", "state.json")); err != nil {
		t.Errorf("backup state moved before restart: %v", err)
	}
	if w := h.Do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, "https://oauth2.googleapis.com")}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(boot, "google", "oauth_client.json")); err != nil {
		t.Errorf("Google client moved before restart: %v", err)
	}
	// Both tokens and one-time-code handoffs must stay where running scripts look.
	if w := h.Do("POST", "/api/token/rotate", nil); w.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	if w := h.Script("POST", "/send", `{"jid":"15551234567@s.whatsapp.net","text":"Examplebank 2FA: reply with the code"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := h.Do("POST", "/api/dev/inbound", map[string]any{"jid": harnessDM, "text": "483920"}); w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	otpPath := filepath.Join(boot, "otp", "main.json")
	h.WaitFor("the OTP handoff at the running path", func() bool { _, err := os.Stat(otpPath); return err == nil })
	var handoff otp.Record
	data, err := os.ReadFile(otpPath)
	if err != nil || json.Unmarshal(data, &handoff) != nil || handoff.Code != "483920" {
		t.Fatalf("OTP handoff: %s, %v", data, err)
	}
	// The next run still receives the running data/database paths and a token
	// under that data directory. These are output by the fake CLI, not read
	// from internal brain settings.
	for key, want := range map[string]string{"BRIDGE_DATA_DIR": boot, "BRIDGE_DB_PATH": filepath.Join(boot, "bridge.db"), "FRIDOLIN_BRAIN_TOKEN_FILE": filepath.Join(boot, "run-tokens", "main")} {
		t.Setenv("FRIDOLIN_FAKE_ENV", key)
		if got := h.Answer(inbound{JID: harnessDM, Text: "show " + key}); !strings.Contains(got, want) || strings.Contains(got, next) {
			t.Errorf("run environment %s = %q, want %q", key, got, want)
		}
	}
	// Script paths continue to resolve inside the running media inbox.
	p := writeTestFile(t, filepath.Join(boot, "media", "main", "note.txt"), "note")
	beforeReply := len(h.Outbox())
	body, _ := json.Marshal(map[string]any{"brain": "main", "paths": []string{p}})
	if w := h.Script("POST", "/ingest", string(body)); w.Code != http.StatusAccepted {
		t.Errorf("ingest from %s: %d %s", p, w.Code, w.Body.String())
	}
	h.WaitFor("the ingested file reply", func() bool { return len(h.Outbox()) > beforeReply })
	// The active data directory remains hidden even when hidden folders are listed.
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	w = h.Do("GET", "/api/fs/list?hidden=1&path="+url.QueryEscape(h.Home()), nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listing) != nil {
		t.Fatalf("folder listing: %s", w.Body.String())
	}
	for _, entry := range listing.Entries {
		if entry.Name == filepath.Base(boot) {
			t.Error("running data directory became browsable after save")
		}
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Errorf("runtime activity created next-start data directory: %v", err)
	}
}

func TestRuntimePathConsumersKeepBootAndApplyLiveOptions(t *testing.T) {
	tb := newTestBridge(t, `, "plaud":{"phone_upload":true,"enabled":true}`)
	tb.addFolderBrain(t, manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	runSettings := captureBootRunSettings(t, tb)
	boot := tb.cfgs.Boot()
	next := filepath.Join(tb.home, "next-start")
	w := tb.editConfig(t, func(c map[string]any) {
		c["data_dir"], c["db_path"], c["media_dir"] = next, filepath.Join(next, "state.db"), filepath.Join(next, "inbox")
		c["http_listen"] = "127.0.0.1:9876"
		c["agent_provider"], c["codex_model"] = "codex", "live-model"
		c["queue"] = map[string]any{"mode": "queue", "max_pending": 31}
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	for label, pair := range map[string][2]string{
		"Google credentials fallback": {tb.google.CredsDir(), boot.DataDir},
		"Google redirect":             {tb.google.RedirectURI(), "http://" + boot.HTTPListen + "/"},
		"Plaud private login":         {tb.plaud.Status().LoginDir, filepath.Join(boot.PlaudDir(), "home", ".plaud")},
		"site media":                  {tb.siteBuilderHooks().mediaDir(), boot.MediaDir},
		"site bridge address":         {tb.siteBuilderHooks().bridgeURL(), "http://" + boot.HTTPListen},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", label, pair[0], pair[1])
		}
	}
	// Exercise the backup's actual read/write paths after requested paths change.
	connectAlias(t, tb)
	if !tb.backup.Summary().Connected {
		t.Error("backup connection follows next-start path")
	}
	if w := tb.do("PUT", "/api/backup/settings", map[string]any{"enabled": true}); w.Code != http.StatusOK {
		t.Fatalf("backup settings: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(boot.BackupDir(), "state.json")); err != nil {
		t.Errorf("backup state not saved under boot path: %v", err)
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Errorf("backup created next-start path: %v", err)
	}
	checkPlaudPhonePaths(t, tb, filepath.Join(boot.PlaudDir(), "inbox"))
	s := runSettings()
	if s.DataDir != boot.DataDir || s.DBPath != boot.DBPath || s.MediaDir != boot.MediaDir || s.CredsDir != boot.DataDir {
		t.Errorf("brain startup paths drifted: %#v", s)
	}
	if s.Provider != "codex" || s.Model != "live-model" || s.MaxPending != 31 {
		t.Errorf("live run options did not apply: provider=%s model=%s queue=%d", s.Provider, s.Model, s.MaxPending)
	}
	profile, err := tb.brainMgr.profiler.Profile("main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, boot.DataDir) || !strings.Contains(profile, boot.MediaDir) || strings.Contains(profile, next) {
		t.Error("sandbox follows saved paths instead of the running paths")
	}
	explicitCreds, explicitPhone := filepath.Join(tb.home, "creds"), filepath.Join(tb.home, "phone")
	w = tb.editConfig(t, func(c map[string]any) {
		c["sandbox"].(map[string]any)["credentials_dir"] = explicitCreds
		c["plaud"].(map[string]any)["phone_dir"] = explicitPhone
		c["plaud"].(map[string]any)["brain_access"] = true
	})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if tb.google.CredsDir() != explicitCreds || tb.plaud.Status().LoginDir != filepath.Join(boot.Home, ".plaud") || runSettings().CredsDir != explicitCreds {
		t.Error("explicit credential/phone paths or Plaud access stopped applying live")
	}
	if err := os.MkdirAll(explicitPhone, 0700); err != nil {
		t.Fatal(err)
	}
	checkPlaudPhonePaths(t, tb, explicitPhone)

	if runtime.GOOS == "darwin" {
		if w := tb.editConfig(t, func(c map[string]any) { c["sandbox"].(map[string]any)["enabled"] = true }); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		_, _, env, err := tb.siteBuilderHooks().sandbox("main", os.Args[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, filepath.Join(boot.DataDir, "brains", "main")) || strings.Contains(joined, next) {
			t.Errorf("site sandbox state follows next-start paths: %v", env)
		}
	}
	// Changing the process environment after open must not relocate home denies.
	replacementHome := t.TempDir()
	t.Setenv("HOME", replacementHome)
	profile, err = tb.brainMgr.profiler.Profile("main")
	if err != nil || !strings.Contains(profile, filepath.Join(boot.Home, ".ssh")) || strings.Contains(profile, filepath.Join(replacementHome, ".ssh")) {
		t.Errorf("sandbox home changed after startup: %v", err)
	}
}

// A first phone poll records which folder it read without handing old files
// to a brain. This exercises the real read/write paths through the public API.
func checkPlaudPhonePaths(t *testing.T, tb *testBridge, inbox string) {
	t.Helper()
	if err := tb.plaud.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state struct {
		Account string `json:"account"`
		Seeded  bool   `json:"seeded"`
	}
	path := filepath.Join(tb.cfgs.Boot().PlaudDir(), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Seeded || state.Account != "phone "+inbox {
		t.Errorf("Plaud read the wrong inbox or saved elsewhere: %+v", state)
	}
	if info, err := os.Stat(inbox); err != nil || !info.IsDir() {
		t.Errorf("Plaud phone inbox was not created: %v", err)
	}
}

// Exercise the options used by NewBridge with only I/O consumers replaced.
// A settings callback accidentally wired to Get would fail this.
func TestWhatsAppAttachmentKeepsBootMediaAfterAPISave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs, err := openConfigStore(writeConfig(t, fmt.Sprintf(`{"data_dir":%q,"allowed_numbers":["15551234567"]}`, filepath.Join(home, "running"))))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBridge(cs, BridgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	tb := &testBridge{Bridge: b, h: b.newHTTPHandler(config.DefaultHTTPListen), home: home}
	next := filepath.Join(home, "next-start")
	if w := tb.editConfig(t, func(c map[string]any) { c["media_dir"] = next; c["max_media_mb"] = 1 }); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	opts := b.whatsAppOptions()
	opts.Downloader = &fakeDownloader{body: "%PDF-1.7"}
	inbound := make(chan chat.Inbound, 1)
	notices := make(chan string, 1)
	opts.OnInbound = func(_ context.Context, m chat.Inbound) { inbound <- m }
	opts.Notify = func(_, text string) { notices <- text }
	wa, err := whatsapp.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wa.Disconnect)
	msg := dmEvent("boot-download", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("scan.pdf"), FileLength: proto.Uint64(8)}})
	wa.ProcessMessage(msg)
	select {
	case m := <-inbound:
		want := filepath.Join(cs.Boot().MediaDir, ".incoming", "scan.pdf")
		if len(m.Media) != 1 || m.Media[0] != want {
			t.Fatalf("download = %q; want %q", m.Media, want)
		}
		if body, err := os.ReadFile(want); err != nil || string(body) != "%PDF-1.7" {
			t.Fatalf("downloaded body = %q, %v", body, err)
		}
	case <-time.After(time.Second):
		t.Fatal("attachment was not delivered")
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Errorf("download created next-start media directory: %v", err)
	}
	msg.Message.DocumentMessage.FileLength = proto.Uint64(2 << 20)
	wa.ProcessMessage(msg)
	select {
	case notice := <-notices:
		if notice == "" {
			t.Error("empty media size notice")
		}
	case <-time.After(time.Second):
		t.Fatal("live media size limit not applied")
	}
	select {
	case m := <-inbound:
		t.Errorf("oversized media delivered: %q", m.Media)
	default:
	}

}

// A direct request must not turn the still-active private data folder into a
// writable brain just because Settings now names the next process's folder.
func TestRegistryKeepsRunningPathsProtectedAfterAPISave(t *testing.T) {
	// Use a non-default data path: the default home folders are always denied,
	// which would hide a regression in retaining this process's custom path.
	tb := newTestBridge(t, "")
	active := tb.cfgs.Boot().DataDir
	next := filepath.Join(tb.home, "next-start")
	if w := tb.editConfig(t, func(c map[string]any) { c["data_dir"] = next }); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	// The current and requested directories are independently protected. A raw
	// Add request bypasses the folder picker and its hidden-directory filtering.
	for _, dir := range []string{active, next} {
		w := tb.do("POST", "/api/brains", map[string]any{"path": dir, "create_dir": true, "manifest": manifest.Manifest{ID: "private", Name: "Private"}})
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "keeps from brains") {
			t.Errorf("adding %s: %d %s", dir, w.Code, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(dir, manifest.File)); !os.IsNotExist(err) {
			t.Errorf("refused folder %s was scaffolded: %v", dir, err)
		}
	}
	if len(tb.cfgs.FileCopy().BrainDirs) != 0 {
		t.Error("refused folder was approved")
	}
}

// A file edit can bypass the Add workflow. Runtime resolution must independently
// apply startup path protection when reconciling the saved brain set.
func TestRuntimeResolutionKeepsRunningPathsProtectedAfterFileEdit(t *testing.T) {
	tb := newTestBridge(t, "")
	active := tb.cfgs.Boot().DataDir
	next := filepath.Join(tb.home, "next-start")
	if w := tb.editConfig(t, func(c map[string]any) { c["data_dir"] = next }); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	m := manifest.Manifest{Version: manifest.Version, ID: "private", Name: "Private"}
	if err := manifest.Write(active, &m, false); err != nil {
		t.Fatal(err)
	}
	// This mirrors a hand edit: no registry workflow approved the new folder.
	file := tb.cfgs.FileCopy()
	file.BrainDirs = append(file.BrainDirs, config.BrainRef{Path: active, Approved: &m})
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.cfgs.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular settings save reads the external edit, publishes, and reconciles.
	if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Comment = "reload the external edit"; return nil }); err != nil {
		t.Fatal(err)
	}
	if tb.brainMgr.Get("private") != nil {
		t.Fatal("runtime started a brain in the active private data directory")
	}
	var refused bool
	for _, issue := range tb.brainMgr.Issues() {
		if issue.ID == "private" && strings.Contains(issue.Detail, "keeps from brains") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("runtime did not report private-path refusal: %v", tb.brainMgr.Issues())
	}
}

func TestRestoreKeepsRunningPathsProtectedAfterAPISave(t *testing.T) {
	tb := newTestBridge(t, "")
	active := tb.cfgs.Boot().DataDir
	next := filepath.Join(tb.home, "next-start")
	if w := tb.editConfig(t, func(c map[string]any) { c["data_dir"] = next }); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	for _, dir := range []string{filepath.Join(active, "restored"), filepath.Join(next, "restored")} {
		if _, err := tb.restoreTarget(dir); err == nil || !strings.Contains(err.Error(), "keeps from brains") {
			t.Errorf("restore accepted protected target %s: %v", dir, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("refused restore created target: %v", err)
		}
	}
	if _, err := tb.restoreTarget(filepath.Join(tb.home, "restored")); err != nil {
		t.Fatalf("ordinary restore destination refused: %v", err)
	}
}

// bootSettingsRunner observes the run's actual immutable settings at the
// process boundary, after the command's config reload has applied them.
type bootSettingsRunner struct{ settings chan brain.Settings }

func (r bootSettingsRunner) Run(_ context.Context, spec brain.RunSpec, _ claudecli.Callbacks) claudecli.Result {
	r.settings <- *spec.Settings
	return claudecli.Result{}
}

func captureBootRunSettings(t *testing.T, tb *testBridge) func() brain.Settings {
	t.Helper()
	runner := bootSettingsRunner{settings: make(chan brain.Settings, 1)}
	mgr := tb.brainMgr
	mgr.Manager.Stop()
	mgr.Manager = brain.NewManager(brain.Options{
		Runner: runner, Sink: brainHooksSink{mgr.hooks},
		Input: commandBrainInputRules(), ClaimMedia: claimBrainMedia,
		ShutdownGrace: func() time.Duration { return 0 },
	})
	if err := mgr.Apply(runtimeConfig(tb.cfgs)); err != nil {
		t.Fatal(err)
	}
	return func() brain.Settings {
		t.Helper()
		br, _, _, err := mgr.Submit(brain.Inbound{Inbound: chat.Inbound{
			JID: harnessDM, Brain: "main", Text: "capture settings", Source: chat.SourceIngest,
		}})
		if err != nil {
			t.Fatal(err)
		}
		var settings brain.Settings
		select {
		case settings = <-runner.settings:
		case <-time.After(10 * time.Second):
			t.Fatal("run did not reach the injected runner")
		}
		if !br.WaitIdle(t.Context(), 10*time.Second) {
			t.Fatal("captured run did not finish")
		}
		return settings
	}
}

// Exercise the options used by NewBridge with only I/O consumers replaced.
// A settings callback accidentally wired to Get would fail this.
