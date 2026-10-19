package main

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// A transcript may be fetched while its brain is replaced. The handoff must
// retain the original runtime, and the status warning must distinguish that
// runtime from a newly routed brain with the same ID.
func TestPlaudTargetRetainsRuntimeAcrossReplacement(t *testing.T) {
	const first = "15551234567@s.whatsapp.net"
	const second = "15559876543@s.whatsapp.net"
	old := &Brain{id: "a"}
	old.settings.Store(&brainSettings{manifest: manifest.Manifest{ReplyChat: first}})
	mgr := &BrainManager{brains: map[string]*Brain{"a": old}, dmBrain: old}
	var submitted *Brain
	target := plaudTargetOf(mgr, "a", func(br *Brain, _, _, _ string, _ []string, done func(error)) error {
		submitted = br
		done(nil)
		return nil
	})
	if target == nil || target.ID() != "a" || target.ReplyChat() != first || target.OtherOwner(first) != "" {
		t.Fatal("selected runtime lost its routing or reply settings")
	}
	old.settings.Store(&brainSettings{manifest: manifest.Manifest{ReplyChat: second}})
	if target.ReplyChat() != second {
		t.Fatal("a retained target froze settings that must remain live")
	}
	current := &Brain{id: "a"}
	current.settings.Store(&brainSettings{manifest: manifest.Manifest{ReplyChat: first}})
	mgr.mu.Lock()
	mgr.brains["a"], mgr.dmBrain = current, current
	mgr.mu.Unlock()
	if target.ReplyChat() != second || target.OtherOwner(second) != "a" {
		t.Fatal("replacement with the same ID was mistaken for the selected runtime")
	}
	ended := false
	if err := target.Submit(second, "plaud", "transcript", nil, func(error) { ended = true }); err != nil || submitted != old || !ended {
		t.Fatalf("handoff changed runtime or completion callback: %v", err)
	}
	if automatic := plaudTargetOf(mgr, "", nil); automatic == nil || automatic.ReplyChat() != first {
		t.Fatal("a fresh automatic selection missed the current route")
	}
	if plaudTargetOf(mgr, "missing", nil) != nil {
		t.Fatal("an explicit missing target fell back to another brain")
	}
}
