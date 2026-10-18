package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// brainViews returns GET /api/brains.
func (h *harness) brainViews() []brainView {
	h.t.Helper()
	var views []brainView
	if w := h.Do("GET", "/api/brains", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &views) != nil {
		h.t.Fatalf("GET /api/brains: %d %s", w.Code, w.Body.String())
	}
	return views
}

// Allowing a group for a brain takes it away from the brain that had it,
// in both brains' brain.json. When the second one cannot be written, the
// first is put back: no brain.json keeps a change config.json never got.
func TestAssignGroupPutsBackTheFirstBrainJSON(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	a := h.AddBrain(BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	b := h.AddBrain(BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Groups: []string{harnessGroup}}})
	aBefore := readFile(t, filepath.Join(a, ManifestFile))
	bBefore := readFile(t, filepath.Join(b, ManifestFile))
	cfgBefore := readFile(t, filepath.Join(h.Root(), "config.json"))
	// b's brain.json is replaced through a temp file beside it.
	if err := os.Chmod(b, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(b, 0o700) })
	w := h.Do("POST", "/api/chats/allow", chatRequest{JID: harnessGroup, Kind: "group", Brain: "a"})
	if err := os.Chmod(b, 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("allow with b's folder read-only: %d %s, want the 422 with the write's error", w.Code, w.Body.String())
	}
	if got := readFile(t, filepath.Join(a, ManifestFile)); got != aBefore {
		t.Errorf("a's brain.json kept the group:\n%s", got)
	}
	if got := readFile(t, filepath.Join(b, ManifestFile)); got != bBefore {
		t.Errorf("b's brain.json changed:\n%s", got)
	}
	if got := readFile(t, filepath.Join(h.Root(), "config.json")); got != cfgBefore {
		t.Errorf("config.json changed:\n%s", got)
	}
	for _, v := range h.brainViews() {
		if v.Status != "ok" {
			t.Errorf("brain %s: %s %+v, want no drift", v.ID, v.Status, v.Issues)
		}
	}
}

// A brain added through the dashboard gets its brain.json with the update
// that registers it. When config.json cannot be written, the add fails and
// leaves no brain.json behind, nor the rest of what it scaffolded.
func TestAFailedAddLeavesNoBrainJSON(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	if err := os.Chmod(h.Root(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.Root(), 0o700) })
	w := h.Do("POST", "/api/brains", map[string]any{"path": "~/new", "manifest": BrainManifest{ID: "new", Name: "New"}, "create_dir": true})
	if err := os.Chmod(h.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 {
		t.Errorf("add with config.json not writable: %d %s, want 422", w.Code, w.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(h.Home(), "new")); !os.IsNotExist(err) {
		t.Errorf("the failed add left its folder: %v", err)
	}
	if w := h.Do("POST", "/api/brains", map[string]any{"path": "~/new", "manifest": BrainManifest{ID: "new", Name: "New"}, "create_dir": true}); w.Code != 200 {
		t.Errorf("adding the folder again: %d %s", w.Code, w.Body.String())
	}
	if m, err := ReadManifest(filepath.Join(h.Home(), "new")); err != nil || m.Name != "New" {
		t.Errorf("brain.json after the add: %+v, %v", m, err)
	}
}

// An add writes the brain's brain.json with the update that registers the
// brain, once that update's checks pass, not with the rest of the folder
// before them.
func TestAddWritesBrainJSONWithTheRegistration(t *testing.T) {
	tb := newTestBridge(t, "")
	// Another writer holds the config update while the add scaffolds.
	inside, release, saved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := tb.cfgs.Update("", func(c *Config) error {
			close(inside)
			<-release
			return nil
		})
		saved <- err
	}()
	<-inside
	added := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		added <- tb.do("POST", "/api/brains", map[string]any{"path": "~/b", "manifest": BrainManifest{ID: "b", Name: "B"}, "create_dir": true})
	}()
	dir := filepath.Join(tb.home, "b")
	waitFor(t, 5*time.Second, "the add's scaffold", func() bool { return fileExists(filepath.Join(dir, "skills", "README.md")) })
	time.Sleep(100 * time.Millisecond) // what the scaffold would write next
	if _, err := os.Lstat(filepath.Join(dir, ManifestFile)); !os.IsNotExist(err) {
		t.Errorf("brain.json is there before the update that registers the brain: %v", err)
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	w := <-added
	var resp struct{ Created []string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(resp.Created, ManifestFile) {
		t.Errorf("created = %v, want brain.json in it", resp.Created)
	}
	if m, err := ReadManifest(dir); err != nil || m.ID != "b" {
		t.Errorf("brain.json after the add: %+v, %v", m, err)
	}
}
