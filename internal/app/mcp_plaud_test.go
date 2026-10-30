package app

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// The core brain may update the phone app while it uploads through the
// site; another brain may not, and without the phone app neither may.
func TestCoreBrainUpdatesThePlaudApp(t *testing.T) {
	t.Parallel()
	h := newServedHarness(t, `{"plaud": {"phone_upload": true}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "core", ID: "r1", Chat: harnessDM})
	defer revoke()
	famTok, revokeFam := h.b.runTokens.mintRun(runInfo{Brain: "family", ID: "r2", Chat: harnessGroup})
	defer revokeFam()
	call := func(method, path, body, run, token string) string {
		w := h.Script(method, path, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Fridolin-Run", run)
		})
		if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if got := call("GET", "/brain/mcp/tools", "", "r1", tok); !strings.Contains(got, `"name":"plaud_app_update"`) || !strings.Contains(got, `"name":"plaud_app_status"`) {
		t.Errorf("the core's tools: %s", got)
	}
	if got := call("GET", "/brain/mcp/tools", "", "r2", famTok); strings.Contains(got, "plaud_app") {
		t.Errorf("family's tools: %s", got)
	}
	if got := call("POST", "/brain/mcp/call", `{"name":"plaud_app_status","arguments":{}}`, "r1", tok); !strings.Contains(got, "No build yet.") || !strings.Contains(got, "No phone is paired.") {
		t.Errorf("status: %s", got)
	}
	// It builds as the card's button does, and says why it cannot.
	if got := call("POST", "/brain/mcp/call", `{"name":"plaud_app_update","arguments":{}}`, "r1", tok); !strings.Contains(got, `"isError":true`) || !strings.Contains(got, "the app cannot be built: enter the Plaud developer app") {
		t.Errorf("update: %s", got)
	}
	if got := call("POST", "/brain/mcp/call", `{"name":"plaud_app_update","arguments":{}}`, "r2", famTok); !strings.Contains(got, "this run has no tool plaud_app_update") {
		t.Errorf("family's update: %s", got)
	}

	if w := h.Do("PUT", "/api/plaud/settings", map[string]any{"phone_upload": false}); w.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if got := call("GET", "/brain/mcp/tools", "", "r1", tok); strings.Contains(got, "plaud_app") {
		t.Errorf("the core's tools without the phone app: %s", got)
	}
}

func TestPlaudAppSummary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	rel := &plaud.Release{VersionCode: 9, VersionName: "0.7", Built: now.Add(-time.Hour), Node: "mac-mini"}
	s := &plaud.AppStatus{
		Release: rel,
		Build:   nil,
		Devices: []plaud.PhoneRow{
			{ID: "dev_a", App: &plaud.PhoneApp{VersionCode: 8, VersionName: "0.7", Asked: now.Add(-5 * time.Minute), Update: &plaud.AppUpdate{VersionCode: 9, State: "confirm"}}},
			{ID: "dev_b", App: &plaud.PhoneApp{VersionCode: 9, VersionName: "0.7", Asked: now.Add(-time.Minute), Update: &plaud.AppUpdate{VersionCode: 9, State: "installed"}}},
			{ID: "dev_c", OtherKey: true, App: &plaud.PhoneApp{VersionCode: 8, VersionName: "0.7", Asked: now}},
			{ID: "dev_d"},
		},
	}
	got := plaudAppSummary(s, now)
	for _, want := range []string{
		"Latest build: 0.7 (9), built 1h0m0s ago on node mac-mini.",
		"Phone dev_a: runs 0.7 (8), asked 5m0s ago; updates to 9 at its next check-in. Last update, to 9: waits for a tap on the phone",
		"Phone dev_b: runs 0.7 (9), asked 1m0s ago; up to date. Last update, to 9: installed.",
		"Phone dev_c: runs 0.7 (8), asked 0s ago; the latest build is signed with another key",
		"Phone dev_d: its app has not asked for updates yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if got := plaudAppSummary(nil, now); !strings.Contains(got, "phone_upload is off") {
		t.Errorf("without the phone app: %s", got)
	}
}
