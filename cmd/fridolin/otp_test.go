package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/otp"
)

// The bridge sweeps the hand-offs when it starts: a code that expired
// while it was stopped is gone, one still valid stays for its script.
func TestBridgeStartRemovesExpiredOTPHandoffs(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "data", "otp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	files := map[string]int64{"expired.json": now.Add(-time.Minute).UnixMilli(), "valid.json": now.Add(time.Minute).UnixMilli()}
	for name, expires := range files {
		if err := os.WriteFile(filepath.Join(dir, name), fmt.Appendf(nil, `{"code":"483920","expires_unix_ms":%d}`, expires), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfgs, err := openConfigStore(writeConfig(t, `{"brain_mode": true, "data_dir": "`+home+`/data"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())
	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if _, err := os.Stat(filepath.Join(dir, "expired.json")); !os.IsNotExist(err) {
		t.Errorf("the expired code is still there (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "valid.json")); err != nil {
		t.Errorf("the valid code went: %v", err)
	}
}

// A code the brain asked for in its own words names no request, but says
// when the brain asked.
func TestHandOffOfACodeTheBrainAskedFor(t *testing.T) {
	tb, br := newMainBridge(t, "")
	asked := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	br.AppendEntry(history.Entry{TS: asked.Format(time.RFC3339), Chat: testDMChat, From: "main", Text: "🔐 Please send me the verification code", Type: history.Out})
	tb.handleInbound(t.Context(), Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "483920", Source: chat.SourceWhatsApp}})
	var h otp.Record
	data, err := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "otp", "main.json"))
	if err == nil {
		err = json.Unmarshal(data, &h)
	}
	if err != nil || h.Code != "483920" || h.Request != "" || h.RequestedUnixMS != asked.UnixMilli() {
		t.Errorf("hand-off %s (%v), want the code, no request id and the time the brain asked", data, err)
	}
}
