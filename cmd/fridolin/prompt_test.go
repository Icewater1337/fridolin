package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// promptBlock is what prompt holds between <tag> and </tag>, or "".
func promptBlock(prompt, tag string) string {
	i, j := strings.Index(prompt, "<"+tag+">"), strings.Index(prompt, "</"+tag+">")
	if i < 0 || j < i {
		return ""
	}
	return prompt[i:j]
}

// A run's prompt has the message it answers once, as the new message: the
// chat history ends before it. Messages that came in while it waited in
// the queue are not in its history either; they have runs of their own.
func TestPromptShowsTheMessageOnce(t *testing.T) {
	mgr, d, home := queueTestManager(t, 300, "")
	promptLog := filepath.Join(home, "prompts.log")
	t.Setenv("FRIDOLIN_FAKE_PROMPT_LOG", promptLog)
	for _, text := range []string{"MSG-ONE", "MSG-TWO", "MSG-THREE"} {
		if _, _, _, err := mgr.Submit(dm(text)); err != nil {
			t.Fatal(err)
		}
	}
	testutil.WaitFor(t, 10*time.Second, "three runs", func() bool { return d.count() == 3 })

	prompts := loggedPrompts(t, promptLog)
	cases := []struct {
		msg            string
		before, absent []string
	}{
		{"MSG-ONE", nil, []string{"MSG-TWO", "MSG-THREE"}},
		{"MSG-TWO", []string{"MSG-ONE"}, []string{"MSG-THREE"}},
		{"MSG-THREE", []string{"MSG-ONE", "MSG-TWO"}, nil},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			var p string
			for msg, prompt := range prompts {
				if strings.HasSuffix(msg, "says: "+c.msg) {
					p = prompt
				}
			}
			if p == "" {
				t.Fatalf("no prompt for %s in %q", c.msg, prompts)
			}
			history := ""
			if i, j := strings.Index(p, "<chat_history>"), strings.Index(p, "</chat_history>"); i >= 0 && j > i {
				history = p[i:j]
			}
			if n := strings.Count(p, c.msg); n != 1 {
				t.Errorf("%s is in its prompt %d times, want once:\n%s", c.msg, n, p)
			}
			for _, b := range c.before {
				if strings.Count(history, b) != 1 {
					t.Errorf("the history in the prompt for %s lacks %s:\n%s", c.msg, b, p)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(p, a) {
					t.Errorf("the prompt for %s has %s, which came in after it:\n%s", c.msg, a, p)
				}
			}
			if want := map[int]string{0: "", 1: "The message before the new one", 2: "The 2 messages before the new one"}[len(c.before)]; !strings.Contains(history, want) {
				t.Errorf("the history in the prompt for %s does not start %q:\n%s", c.msg, want, history)
			}
		})
	}
}

// The prompt says what kind of chat the run is in, which a JID does not
// tell the model, and when the new message did not come from that chat.
func TestPromptSaysWhatChatItIs(t *testing.T) {
	cases := []struct {
		name     string
		m        brain.Inbound
		want     string
		handedIn bool
	}{
		{"a direct chat", brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hi", Source: chat.SourceWhatsApp}}, "This chat is a direct chat on WhatsApp.", false},
		{"a group", brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15551234567", Text: "hi", Source: chat.SourceWhatsApp}}, "This chat is the WhatsApp group ", false},
		{"a build chat", brain.Inbound{Inbound: chat.Inbound{JID: buildChatJID("main", 2), Sender: "owner", Text: "make the header blue", Source: chat.SourceDev}}, "This chat is the site-building chat on the dashboard's Build page, not a WhatsApp chat.", false},
		{"files handed in", brain.Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "upload", Text: "a scan", Media: []string{"/media/a.pdf"}, Source: chat.SourceIngest}}, "This chat is a direct chat on WhatsApp.", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompttext.Build(prompttext.Input{Now: time.Now(), BrainID: "main", Message: c.m.Inbound, BuildChat: brainOfBuildJID(c.m.JID) != ""})
			if !strings.Contains(p, c.want) {
				t.Errorf("the prompt does not say %q:\n%s", c.want, p)
			}
			if got := strings.Contains(p, "handed to you outside WhatsApp"); got != c.handedIn {
				t.Errorf("the prompt says the files were handed in: %v, want %v:\n%s", got, c.handedIn, p)
			}
		})
	}
}
