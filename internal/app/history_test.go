package app

import (
	"fmt"
	"testing"

	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/history"
)

// The dashboard's feed is seeded with the history's last lines.
func TestActivitySeedReadsTheLastLines(t *testing.T) {
	br := bareBrain(t)
	for i := range 60 {
		br.AppendHistory(testDMChat, "15551234567", fmt.Sprintf("message %d", i), history.In)
	}
	feed := activity.New(100)
	seedActivity(feed, br)
	got := feed.Recent(100, "")
	if len(got) != 50 || got[0].Text != "message 59" || got[49].Text != "message 10" {
		var texts []string
		for _, e := range got {
			texts = append(texts, e.Text)
		}
		t.Errorf("feed = %q, want messages 59 down to 10", texts)
	}
}
