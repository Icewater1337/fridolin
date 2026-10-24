package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// The tests in this file pin bugs that are known and left as they are while
// the code moves into packages: a move must not change them by accident,
// and the fix that changes one on purpose rewrites its test. One more is
// pinned elsewhere: the API golden files hold the status codes as each
// handler picks them.

// TestAFailedConfigWriteLeavesBrainJSONAsItWas: saving a brain's settings
// writes its brain.json, once the edit has passed its checks, just before
// config.json, which approves it. This pinned a bug: brain.json was
// written from inside the config update, and kept the edit when
// config.json then could not be written, so the brain showed as changed
// on disk. Now the failed save puts brain.json back as it was.
func TestAFailedConfigWriteLeavesBrainJSONAsItWas(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	before := readFile(t, filepath.Join(dir, manifest.File))
	// config.json is replaced through a temp file beside it, which a
	// read-only folder refuses. HOME, below it, stays writable.
	if err := os.Chmod(h.Root(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.Root(), 0o700) })
	w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": manifest.Manifest{ID: "main", Name: "Renamed", Routing: manifest.Routing{HandlesDMs: true}}})
	if err := os.Chmod(h.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("save with config.json not writable: %d %s, want the 422 with the write's error", w.Code, w.Body.String())
	}
	disk, err := manifest.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Name != "Main" {
		t.Errorf("brain.json names the brain %q, want Main as before the failed save", disk.Name)
	}
	if after := readFile(t, filepath.Join(dir, manifest.File)); after != before {
		t.Errorf("brain.json changed:\n%s\nwas\n%s", after, before)
	}
	if got := h.Approved()["main"].Name; got != "Main" {
		t.Errorf("config.json approves %q, want the name before the edit", got)
	}
	if views := h.brainViews(); len(views) != 1 || views[0].Status != "ok" || views[0].Disk != nil {
		t.Errorf("GET /api/brains = %+v, want main without drift", views)
	}
}
