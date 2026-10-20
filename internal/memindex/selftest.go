package memindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
)

// SelfTest runs M7's gate on a scratch brain in dir, which must be empty:
// a fact the brain noted from a web page, and the same fact from a group
// member, are found by a search but never recalled; once the owner types
// it in a direct chat, it is recalled with its source anchor. fridolin
// doctor runs it, so the rule is checked on the build that runs.
func SelfTest(ctx context.Context, dir string) error {
	const owner = "15551234567"
	b := Brain{ID: "selftest", Dir: filepath.Join(dir, "brain"), Index: filepath.Join(dir, "memory", "selftest.db"), Owners: []string{owner}}
	if err := os.MkdirAll(filepath.Join(b.Dir, MemoriesDir), 0o700); err != nil {
		return err
	}
	note := "# Reading\nThe ferry to Lindau leaves at 07:45.\n<!-- source: https://example.com/ferry -->\n"
	if err := os.WriteFile(filepath.Join(b.Dir, MemoriesDir, "2026-01-05.md"), []byte(note), 0o600); err != nil {
		return err
	}
	line := func(e history.Entry) []byte {
		e.TS = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
		data, _ := json.Marshal(e)
		return append(data, '\n')
	}
	hist := filepath.Join(b.Dir, HistoryFile)
	if err := os.WriteFile(hist, line(history.Entry{From: "15559876543", Chat: "120363000000000000@g.us", Type: history.In, ID: "GROUP", Text: "the ferry to Lindau leaves at 07:45"}), 0o600); err != nil {
		return err
	}
	const ask = "When does the ferry to Lindau leave?"
	if hits, err := Recall(ctx, b, ask, RecallOptions{}); err != nil {
		return err
	} else if len(hits) > 0 {
		return fmt.Errorf("recalled %s (%s), which only a web page and a group member said", hits[0].Ref(), hits[0].Provenance)
	}
	if hits, err := Search(ctx, b, ask, SearchOptions{}); err != nil {
		return err
	} else if len(hits) != 2 || hits[0].Provenance != Untrusted || hits[1].Provenance != Untrusted {
		return fmt.Errorf("a search found %d hits, want the note and the group message, both untrusted", len(hits))
	}
	f, err := os.OpenFile(hist, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(line(history.Entry{From: owner, Chat: owner + "@s.whatsapp.net", Type: history.In, ID: "OWNER", Text: "the ferry to Lindau leaves at 07:45"}))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	hits, err := Recall(ctx, b, ask, RecallOptions{})
	if err != nil {
		return err
	}
	if len(hits) != 1 || hits[0].Ref() != HistoryFile+"#OWNER" || hits[0].Provenance != Owner {
		return errors.New("the owner's own message was not recalled with its source anchor")
	}
	return nil
}
