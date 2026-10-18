package main

import (
	"fmt"
	"github.com/Icewater1337/fridolin/internal/history"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run's prompt reads the history back from its end, not the whole file:
// the chat's last messages are found behind megabytes of another chat's,
// a line too long to take does not hide what came after it, and what lies
// further back than the budget is not read at all.
func TestPromptHistoryIsReadFromTheEnd(t *testing.T) {
	prevBudget, prevLine := promptHistoryBudget, history.MaxLine
	promptHistoryBudget, history.MaxLine = 4<<20, 256<<10
	t.Cleanup(func() { promptHistoryBudget, history.MaxLine = prevBudget, prevLine })
	filler := strings.Repeat("chatter ", 128)
	other := func(br *Brain, bytes int) {
		for range bytes / len(filler) {
			br.appendHistory(testGroup, "15559876543", filler, historyIn)
		}
	}
	cases := []struct {
		name  string
		setup func(br *Brain)
		want  string
	}{
		{"behind another chat's megabytes", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "one", historyIn)
			other(br, 3<<20)
		}, "one"},
		{"after a line too long to take", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "old", historyIn)
			// JSON writes each < as \u003c, six bytes.
			br.appendHistory(testDMChat, "15551234567", strings.Repeat("<", history.MaxLine/2), historyIn)
			br.appendHistory(testDMChat, "15551234567", "newest", historyIn)
		}, "old newest"},
		{"after a line that is no JSON", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "old", historyIn)
			f, err := os.OpenFile(filepath.Join(br.Dir(), historyFileName), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(f, `{"chat":"`+testDMChat+`", broken`)
			f.Close()
			br.appendHistory(testDMChat, "15551234567", "newest", historyIn)
		}, "old newest"},
		{"further back than the budget", func(br *Brain) {
			br.appendHistory(testDMChat, "15551234567", "ancient", historyIn)
			other(br, 5<<20)
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := bareBrain(t)
			logged := captureLog(t)
			c.setup(br)
			before, since := br.historyBefore(br.logInbound(dm("now")), dmHistoryWindow)
			if entryTexts(before) != c.want || len(since) != 0 {
				t.Errorf("historyBefore = %q, %q; want %q", entryTexts(before), entryTexts(since), c.want)
			}
			switch {
			case strings.Contains(c.name, "too long") && !logContains(logged, "brain a: skipped 1 history lines longer than 256 KiB"):
				t.Errorf("log = %q, want the long line noted", logged())
			case strings.Contains(c.name, "no JSON") && !logContains(logged, "brain a: skipped 1 history lines that are not JSON"):
				t.Errorf("log = %q, want the broken line noted", logged())
			}
		})
	}
}

// The dashboard's feed is seeded with the history's last lines.
func TestActivitySeedReadsTheLastLines(t *testing.T) {
	br := bareBrain(t)
	for i := range 60 {
		br.appendHistory(testDMChat, "15551234567", fmt.Sprintf("message %d", i), historyIn)
	}
	feed := NewActivity(100)
	feed.Seed(br)
	got := feed.Recent(100, "")
	if len(got) != 50 || got[0].Text != "message 59" || got[49].Text != "message 10" {
		var texts []string
		for _, e := range got {
			texts = append(texts, e.Text)
		}
		t.Errorf("feed = %q, want messages 59 down to 10", texts)
	}
}

// entryTexts is the texts of es, joined by spaces.
func entryTexts(es []historyEntry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Text)
	}
	return strings.Join(out, " ")
}
