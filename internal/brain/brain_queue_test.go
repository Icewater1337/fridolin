package brain

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

func TestBuildPromptListsAllAttachments(t *testing.T) {
	br := bareBrain(t)
	p := br.buildPrompt(&Settings{}, Inbound{Inbound: chat.Inbound{JID: "x@s.whatsapp.net", Sender: "1", Text: "file these", Media: []string{"/m/1.jpg", "/m/2.pdf"}}})
	if !strings.Contains(p, `The 2 files in its "files" came with it`) || !strings.Contains(p, `"files":["/m/1.jpg","/m/2.pdf"]`) {
		t.Errorf("prompt:\n%s", p)
	}
	if got := chat.HistoryText("hi", []string{"/m/1.jpg"}); got != "hi\n[attachment: /m/1.jpg]" {
		t.Errorf("history text = %q", got)
	}
}

// TestBuildPromptNamesTheHistorySearch: the chat history in the prompt
// tells the brain how to look further back, with the command every run
// can reach. It used to name tools/history_search.py, relative to the
// brain's folder, where there is no tools/.
func TestBuildPromptNamesTheHistorySearch(t *testing.T) {
	br := bareBrain(t)
	line, _ := json.Marshal(history.Entry{TS: "2026-01-15T10:00:00Z", From: "15551234567", Chat: "15551234567@s.whatsapp.net", Text: "earlier", Type: "in"})
	if err := os.WriteFile(filepath.Join(br.workDir, history.FileName), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	p := br.buildPrompt(&Settings{}, dm("hello"))
	if !strings.Contains(p, "<chat_history>") || !strings.Contains(p, `with `+"`"+`"$FRIDOLIN_BIN" history QUERY`+"`") || strings.Contains(p, "tools/") {
		t.Errorf("prompt:\n%s", p)
	}
}

// Each chat's messages wait in a lane of their own, which runs in order,
// and the chats take turns: the chat whose last run started longest ago
// goes next, one that never had a run first. Work addressed to the brain
// goes before untagged chatter whatever the turns.
func TestQueueOrderGivesEachChatItsTurn(t *testing.T) {
	const x, y, z = "x@s.whatsapp.net", "y@s.whatsapp.net", "z@g.us"
	msg := func(chatJID, text string) Inbound { return Inbound{Inbound: chat.Inbound{JID: chatJID, Text: text}} }
	chatter := func(chatJID, text string) Inbound {
		return Inbound{Inbound: chat.Inbound{JID: chatJID, Text: text, Silent: true}}
	}
	cases := []struct {
		name    string
		turns   map[string]uint64
		pending []Inbound
		want    string
	}{
		{"one chat runs in order", nil, []Inbound{msg(x, "x1"), msg(x, "x2"), msg(x, "x3")}, "x1 x2 x3"},
		{"chats alternate", nil, []Inbound{msg(x, "x1"), msg(x, "x2"), msg(y, "y1"), msg(y, "y2")}, "x1 y1 x2 y2"},
		{"the chat that just ran goes last", map[string]uint64{x: 3}, []Inbound{msg(x, "x2"), msg(y, "y1"), msg(z, "z1")}, "y1 z1 x2"},
		{"the longest ago goes first", map[string]uint64{x: 3, y: 1, z: 2}, []Inbound{msg(x, "x1"), msg(z, "z1"), msg(y, "y1")}, "y1 z1 x1"},
		{"requests before chatter", nil, []Inbound{chatter(z, "z1"), msg(x, "x1"), msg(x, "x2"), msg(y, "y1")}, "x1 y1 x2 z1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := &Brain{pending: c.pending, turns: maps.Clone(c.turns), turn: 10}
			var got []string
			for _, i := range br.queueOrderLocked() {
				got = append(got, c.pending[i].Text)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("order %q, want %q", got, c.want)
			}
			// The worker takes them in that order.
			got = nil
			for len(br.pending) > 0 {
				i := nextIn(br.pending, br.turns)
				got = append(got, br.pending[i].Text)
				chatJID := br.pending[i].JID
				br.pending = slices.Delete(br.pending, i, i+1)
				br.takeTurnLocked(chatJID)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("ran %q, want %q", got, c.want)
			}
			if len(br.turns) != 1 {
				t.Errorf("turns = %v once all ran, want only the last chat's", br.turns)
			}
		})
	}
}
