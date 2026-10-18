package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin bugs that are known and left as they are while
// the code moves into packages: a move must not change them by accident,
// and the fix that changes one on purpose rewrites its test. One more is
// pinned elsewhere: the API golden files hold the status codes as each
// handler picks them.

// TestShutdownDropsTheMessagesItHolds: stopping the bridge, as SIGTERM and a
// restart do, drops the message a brain is answering and the messages
// waiting behind it. The chat that was told its message is queued hears
// nothing more, and since WhatsApp took the messages as delivered, nothing
// brings them back; only their lines in the history are left. A shutdown
// that lets the brains finish, and keeps what still waits for the next
// start, changes this.
func TestShutdownDropsTheMessagesItHolds(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "30000")
	h.Inject(inbound{JID: harnessDM, Text: "first"})
	h.WaitFor("the first run", func() bool { return h.Running("main") })
	h.Inject(inbound{JID: harnessDM, Text: "second"})
	h.WaitSent(harnessDM, "Queued")
	h.Shutdown()

	s := h.scrubber()
	var sb strings.Builder
	sb.WriteString("sent:\n")
	for _, m := range h.Outbox() {
		fmt.Fprintf(&sb, "  %s %q\n", m.JID, s.text(m.Text))
	}
	sb.WriteString("history:\n")
	for _, l := range historyLines(t, dir) {
		fmt.Fprintf(&sb, "  %s\n", s.text(l))
	}
	assertGolden(t, "run/shutdown-drops.golden", []byte(sb.String()))
}

// TestAFailedConfigWriteLeavesBrainJSONAsItWas: saving a brain's settings
// writes its brain.json, once the edit has passed its checks, just before
// config.json, which approves it. This pinned a bug: brain.json was
// written from inside the config update, and kept the edit when
// config.json then could not be written, so the brain showed as changed
// on disk. Now the failed save puts brain.json back as it was.
func TestAFailedConfigWriteLeavesBrainJSONAsItWas(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := newHarness(t, "")
	dir := h.AddBrain(BrainManifest{ID: "main", Name: "Main", Routing: BrainRouting{HandlesDMs: true}})
	before := readFile(t, filepath.Join(dir, ManifestFile))
	// config.json is replaced through a temp file beside it, which a
	// read-only folder refuses. HOME, below it, stays writable.
	if err := os.Chmod(h.Root(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.Root(), 0o700) })
	w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": BrainManifest{ID: "main", Name: "Renamed", Routing: BrainRouting{HandlesDMs: true}}})
	if err := os.Chmod(h.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Errorf("save with config.json not writable: %d %s, want the 422 with the write's error", w.Code, w.Body.String())
	}
	disk, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Name != "Main" {
		t.Errorf("brain.json names the brain %q, want Main as before the failed save", disk.Name)
	}
	if after := readFile(t, filepath.Join(dir, ManifestFile)); after != before {
		t.Errorf("brain.json changed:\n%s\nwas\n%s", after, before)
	}
	if got := h.Approved()["main"].Name; got != "Main" {
		t.Errorf("config.json approves %q, want the name before the edit", got)
	}
	if views := h.brainViews(); len(views) != 1 || views[0].Status != "ok" || views[0].Disk != nil {
		t.Errorf("GET /api/brains = %+v, want main without drift", views)
	}
}
