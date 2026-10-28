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
	if !strings.Contains(p, FrideRule) {
		t.Error("the rule is missing")
	}
	if !strings.Contains(p, `"from":"FrIDE (its news, relayed by the bridge)"`) {
		t.Errorf("FrIDE's news reads as someone else's:\n%s", p)
	}
	in.Message.Source = chat.SourceDelegate
	if strings.Contains(Build(in), FrideRule) {
		t.Error("a delegated run is told the rule")
	}
	in.Message.Source, in.CodeToFride = "", false
	if strings.Contains(Build(in), FrideRule) {
		t.Error("a run without the tools is told the rule")
	}
}

// A core run with FrIDE's tools is told the FrIDE instances, what each is
// for, as JSON; one with none configured, or a delegated run, is not.
func TestFrideInstancesNote(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := Input{Now: now, BrainID: "core", CodeToFride: true,
		FrideInstances: []FrideInstanceNote{{Name: "work", Description: "work <repositories>", Repos: []string{"https://gitlab.example.com/group/app.git"}}},
		Message:        chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "fix the login"}}
	p := Build(in)
	want := "<fride_instances>\n" + FrideInstancesNote + "\n" + `{"name":"work","description":"work \u003crepositories>","repos":["https://gitlab.example.com/group/app.git"]}` + "\n</fride_instances>"
	if !strings.Contains(p, want) {
		t.Errorf("the instances are missing:\n%s", p)
	}
	in.FrideInstances = nil
	if strings.Contains(Build(in), "<fride_instances>") {
		t.Error("told of instances there are none of")
	}
	in.FrideInstances = []FrideInstanceNote{{Name: "work"}}
	in.Message.Source = chat.SourceDelegate
	if strings.Contains(Build(in), "<fride_instances>") {
		t.Error("a delegated run is told the instances")
	}
}
