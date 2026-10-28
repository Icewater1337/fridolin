package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// A core run with FrIDE's tools is told that code goes to FrIDE, and sees
// FrIDE's news in the history as FrIDE's; a delegated run is not told.
func TestFrideRule(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	news := history.Entry{TS: "2026-10-20T11:59:00Z", From: "fride", Text: "❓ *FrIDE #1* asks", Type: history.Out, Via: "fride"}
	in := Input{Now: now, BrainID: "core", CodeToFride: true, History: []history.Entry{news},
		Message: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "the no-results one"}}
	p := Build(in)
	if !strings.Contains(p, FrideRule) || !strings.Contains(p, MachinesRule) {
		t.Error("a rule is missing")
	}
	if !strings.Contains(p, `"from":"FrIDE (its news, relayed by the bridge)"`) {
		t.Errorf("FrIDE's news reads as someone else's:\n%s", p)
	}
	in.Message.Source = chat.SourceDelegate
	if p := Build(in); strings.Contains(p, FrideRule) || strings.Contains(p, MachinesRule) {
		t.Error("a delegated run is told the rules")
	}
	in.Message.Source, in.CodeToFride = "", false
	if p := Build(in); strings.Contains(p, FrideRule) || strings.Contains(p, MachinesRule) {
		t.Error("a run without the tools is told the rules")
	}
}
