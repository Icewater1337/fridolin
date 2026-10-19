package brain

import (
	"os"
	"testing"

	"github.com/Icewater1337/fridolin/internal/history"
)

// A change to a message the history does not hold leaves the file alone:
// it is read, not copied, which a brain folder the bridge cannot write to
// shows.
func TestChangeOfAMessageTheHistoryLacksWritesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder whatever its mode")
	}
	br := bareBrain(t)
	br.AppendEntry(history.Entry{From: "15551234567", Chat: testDMChat, Text: "hi", Type: history.In, ID: "3EB0A"})
	if err := os.Chmod(br.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(br.Dir(), 0o700) })
	drop := func(history.Entry) *history.Entry { return nil }
	if n, err := br.rewriteHistory("3EB0B", func(e history.Entry) bool { return e.ID == "3EB0B" }, drop); n != 0 || err != nil {
		t.Errorf("rewriteHistory for an absent ID = %d, %v; want 0, nil without writing", n, err)
	}
	if _, err := br.rewriteHistory("3EB0A", func(e history.Entry) bool { return e.ID == "3EB0A" }, drop); err == nil {
		t.Errorf("rewriteHistory for a present ID wrote into a folder it cannot write to")
	}
}
