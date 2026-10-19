package main

import (
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

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/otp"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
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
	tb := newTestBridge(t, `, "plaud":{"phone_upload":true}`)
	tb.addFolderBrain(t, manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
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
		"rclone":                      {tb.backup.rcloneConf(), boot.RcloneConf()},
		"backup":                      {tb.backup.dir(), boot.BackupDir()},
		"Google credentials fallback": {tb.google.credsDir(), boot.DataDir},
		"Google redirect":             {tb.google.redirectURI(), "http://" + boot.HTTPListen + "/"},
		"Plaud state":                 {tb.plaud.dir(), boot.PlaudDir()},
		"Plaud private HOME":          {tb.plaud.cliHome(), filepath.Join(boot.PlaudDir(), "home")},
		"Plaud phone inbox":           {tb.plaud.phoneDir(), filepath.Join(boot.PlaudDir(), "inbox")},
		"site media":                  {tb.siteBuilderHooks().mediaDir(), boot.MediaDir},
		"site bridge address":         {tb.siteBuilderHooks().bridgeURL(), "http://" + boot.HTTPListen},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", label, pair[0], pair[1])
		}
	}
	s := tb.brainMgr.Get("main").settings.Load()
	if s.dataDir != boot.DataDir || s.dbPath != boot.DBPath || s.mediaDir != boot.MediaDir || s.credsDir != boot.DataDir {
		t.Errorf("brain startup paths drifted: %#v", s)
	}
	if s.provider != "codex" || s.model != "live-model" || s.maxPending != 31 {
		t.Errorf("live run options did not apply: provider=%s model=%s queue=%d", s.provider, s.model, s.maxPending)
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
	if tb.google.credsDir() != explicitCreds || tb.plaud.phoneDir() != explicitPhone || tb.plaud.cliHome() != boot.Home || tb.brainMgr.Get("main").settings.Load().credsDir != explicitCreds {
		t.Error("explicit credential/phone paths or Plaud access stopped applying live")
	}

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

// Exercise the client actually wired by NewBridge, with only the network
// downloader replaced. A callback accidentally wired to Get would fail this.
func TestWhatsAppAttachmentKeepsBootMediaAfterAPISave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs, err := openConfigStore(writeConfig(t, fmt.Sprintf(`{"data_dir":%q}`, filepath.Join(home, "running"))))
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
	wa := b.wa.(siteChatMessenger).Messenger.(*WhatsAppClient)
	wa.media = &fakeDownloader{body: "%PDF-1.7"}
	msg := &events.Message{Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("scan.pdf"), FileLength: proto.Uint64(8)}}}
	path, _, notice := wa.downloadAttachment(msg, harnessDM)
	if want := filepath.Join(cs.Boot().MediaDir, ".incoming", "scan.pdf"); path != want || notice != "" {
		t.Fatalf("download = %q, %q; want %q", path, notice, want)
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Errorf("download created next-start media directory: %v", err)
	}
	msg.Message.DocumentMessage.FileLength = proto.Uint64(2 << 20)
	if path, _, notice := wa.downloadAttachment(msg, harnessDM); path != "" || notice == "" {
		t.Errorf("live media size limit not applied: %q %q", path, notice)
	}
}
