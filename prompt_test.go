package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The prompts in testdata/prompt are what renderPrompt makes of
// promptCases. After a change to what runs are told, read the diff and
// rewrite them with
//
//	go test -run TestRenderPromptGolden -update-prompt-golden .
var updatePromptGolden = flag.Bool("update-prompt-golden", false, "rewrite testdata/prompt from renderPrompt")

// promptCases are the runs the golden prompts are made for.
func promptCases() map[string]promptInput {
	const alex, sam = "15551234567", "15559876543"
	names := map[string]string{alex: "Alex"}
	return map[string]promptInput{
		"dm": {
			Instructions: "You are Main, Alex's assistant.",
			BrainID:      "main",
			History: []historyEntry{
				{TS: "2026-10-12T09:12:00+02:00", From: alex, Chat: alex + "@s.whatsapp.net", Text: "Can we meet next week?", Type: historyIn},
				{TS: "2026-10-12T09:13:00+02:00", From: "main", Chat: alex + "@s.whatsapp.net", Text: "Sure, which day?", Type: historyOut},
			},
			Names:   names,
			Message: Inbound{JID: alex + "@s.whatsapp.net", Sender: alex, Text: "Tuesday at 3?"},
		},
		"group-files-quote": {
			Instructions: "You are Team.",
			BrainID:      "team",
			History: []historyEntry{
				{TS: "2026-10-18T08:55:00+02:00", From: sam, Chat: testGroup, Text: "Here are the slides", Type: historyIn},
			},
			Names: names,
			Message: Inbound{JID: testGroup, Sender: alex, Text: "file these", Media: []string{"/media/a.pdf", "/media/b.pdf"},
				Quoted: &Quote{From: sam, Text: "Here are the slides"}},
		},
		"first-message": {
			BrainID: "main",
			Message: Inbound{JID: alex + "@s.whatsapp.net", Sender: alex, Text: "hello", Media: []string{"/media/voice.ogg"}},
		},
	}
}

func TestRenderPromptGolden(t *testing.T) {
	for name, in := range promptCases() {
		t.Run(name, func(t *testing.T) {
			got := renderPrompt(in)
			path := filepath.Join("testdata", "prompt", name+".txt")
			if *updatePromptGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (rewrite the golden prompts with -update-prompt-golden)", err)
			}
			if got != string(want) {
				t.Errorf("renderPrompt =\n%s\n\nwant %s:\n%s", got, path, want)
			}
		})
	}
}

// promptBlock is what prompt holds between <tag> and </tag>, or "".
func promptBlock(prompt, tag string) string {
	i, j := strings.Index(prompt, "<"+tag+">"), strings.Index(prompt, "</"+tag+">")
	if i < 0 || j < i {
		return ""
	}
	return prompt[i:j]
}
