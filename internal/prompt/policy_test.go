package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// A brain that approves by itself is told it may act without asking, in
// place of the rule that it asks first; an observe-only run is told
// neither.
func TestAutoPolicyNote(t *testing.T) {
	in := Input{Now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), BrainID: "core",
		Message: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "post it"}}
	if p := Build(in); !strings.Contains(p, PolicyNote) || strings.Contains(p, AutoPolicyNote) {
		t.Error("a brain that asks is not told PolicyNote alone")
	}
	in.AutoApprove = true
	if p := Build(in); !strings.Contains(p, AutoPolicyNote) || strings.Contains(p, PolicyNote) {
		t.Error("a brain with approvals: auto is not told AutoPolicyNote alone")
	}
	in.ObserveOnly = true
	if p := Build(in); strings.Contains(p, AutoPolicyNote) || strings.Contains(p, PolicyNote) {
		t.Error("an observe-only run is told an approval rule")
	}
}
