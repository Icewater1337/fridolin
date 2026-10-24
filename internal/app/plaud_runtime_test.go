package app

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// A transcript may be fetched while its brain is replaced. The handoff must
// retain the original runtime, and the status warning must distinguish that
// runtime from a newly routed brain with the same ID.
func TestPlaudTargetRetainsRuntimeAcrossReplacement(t *testing.T) {
	t.Parallel()
	const first = "15551234567@s.whatsapp.net"
	const second = "15559876543@s.whatsapp.net"
	mgr := &BrainManager{Manager: brain.NewManager(brain.Options{})}
	t.Cleanup(mgr.Stop)
	sp := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", ReplyChat: first, Routing: manifest.Routing{HandlesDMs: true}}}
	apply := func() {
		t.Helper()
		if failures, err := mgr.Manager.Apply([]manifest.Spec{sp}, brain.Globals{}, brain.ApplyOptions{}); err != nil || len(failures) != 0 {
			t.Fatalf("apply: %v %v", failures, err)
		}
	}
	apply()
	old := mgr.Get("a")
	var submitted *brain.Brain
	target := plaudTargetOf(mgr, "a", func(br *brain.Brain, _, _, _ string, _ []string, done func(error)) error {
		submitted = br
		done(nil)
		return nil
	})
	if target == nil || target.ID() != "a" || target.ReplyChat() != first || target.OtherOwner(first) != "" {
		t.Fatal("selected runtime lost its routing or reply settings")
	}
	sp.Manifest.ReplyChat = second
	apply()
	if mgr.Get("a") != old {
		t.Fatal("settings reload replaced the runtime")
	}
	if target.ReplyChat() != second {
		t.Fatal("a retained target froze settings that must remain live")
	}
	sp.Dir = t.TempDir()
	sp.Manifest.ReplyChat = first
	apply()
	if mgr.Get("a") == old {
		t.Fatal("folder replacement retained the old runtime")
	}
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
