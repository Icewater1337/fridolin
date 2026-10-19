package activity

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/history"
)

// The feed's memory limit, stable seed ordering, copies, and JSON shape stay
// independent of the runtime that supplies each brain's history.
func TestActivityFeedSeedAndSnapshot(t *testing.T) {
	a := New(3)
	if data, err := json.Marshal(a.Recent(10, "")); err != nil || string(data) != "[]" {
		t.Fatalf("empty feed = %s, %v", data, err)
	}
	a.Add("a", history.Entry{TS: "2026-10-18T10:00:03Z", Text: "live", Type: history.In})
	a.Add("a", history.Entry{Type: history.Reset})
	a.Seed("b", func() []history.Entry {
		return []history.Entry{
			{TS: "2026-10-18T10:00:01Z", Text: "old", Type: history.In},
			{TS: "2026-10-18T10:00:02Z", Text: "first", Type: history.In},
			{TS: "2026-10-18T10:00:02Z", Text: "second", Type: history.Out},
		}
	})
	a.Seed("b", func() []history.Entry {
		t.Fatal("a brain's history was read twice")
		return nil
	})
	got := a.Recent(10, "")
	if len(got) != 3 || got[0].Text != "second" || got[1].Text != "first" || got[2].Text != "old" {
		t.Fatalf("capped and sorted feed = %+v", got)
	}
	got[0].Text = "changed by reader"
	if again := a.Recent(1, "b"); len(again) != 1 || again[0].Text != "second" {
		t.Fatalf("reader changed the feed: %+v", again)
	}
	if got := a.Recent(10, "a"); len(got) != 0 {
		t.Fatalf("brain filter = %+v", got)
	}
	data, err := json.Marshal(a.Recent(1, "b"))
	want := `[{"ts":"2026-10-18T10:00:02Z","brain":"b","chat":"","from":"","type":"out","text":"second"}]`
	if err != nil || string(data) != want {
		t.Fatalf("feed JSON = %s, %v; want %s", data, err, want)
	}
}

func TestActivityFeedChangeScopeAndTruncation(t *testing.T) {
	a := New(10)
	message := history.Entry{TS: "2026-10-18T10:00:00Z", Chat: "chat", From: "sender", ID: "message", Text: "before", Type: history.In}
	a.Add("a", message)
	a.Add("b", message)
	other := message
	other.From = "other"
	a.Add("a", other)
	other = message
	other.Type = history.Out
	a.Add("a", other)
	change := Change{Target: "message", Sender: "sender", Chat: "canonical", ChatAlt: "chat"}
	a.Change("a", change, strings.Repeat("界", 250))
	got := a.Recent(10, "")
	if len(got) != 4 || got[0].Text != "before" || got[1].Text != "before" || got[2].Text != "before" {
		t.Fatalf("change escaped its brain/sender/type: %+v", got)
	}
	if text := got[3].Text; !utf8.ValidString(text) || text != strings.Repeat("界", 200)+"…" {
		t.Fatalf("truncated edit = %q (%d bytes)", text, len(text))
	}
	a.Change("a", change, "")
	if again := a.Recent(10, ""); again[3].Text != got[3].Text {
		t.Fatal("empty edit changed the text")
	}
	change.Revoke = true
	a.Change("a", change, "ignored")
	if got := a.Recent(10, ""); len(got) != 3 {
		t.Fatalf("revoke removed the wrong messages: %+v", got)
	}
}

// A slow history load and live writes still serialize through the feed's
// original lock; concurrent callers load each brain only once.
func TestActivityFeedConcurrentSeedAndWrites(t *testing.T) {
	a := New(200)
	started, release := make(chan struct{}), make(chan struct{})
	var workers sync.WaitGroup
	workers.Go(func() {
		a.Seed("a", func() []history.Entry {
			close(started)
			<-release
			return []history.Entry{{TS: "2026-10-18T10:00:00Z", Text: "seed", Type: history.In}}
		})
	})
	<-started
	for range 20 {
		workers.Go(func() {
			a.Seed("a", func() []history.Entry {
				t.Error("concurrent seed loaded the same brain twice")
				return nil
			})
			a.Add("a", history.Entry{Text: "live", Type: history.In})
			a.Recent(10, "")
		})
	}
	close(release)
	workers.Wait()
	got := a.Recent(200, "")
	if len(got) != 21 || got[len(got)-1].Text != "seed" {
		t.Fatalf("serialized feed = %+v", got)
	}
	if got[0].TS.IsZero() || got[0].TS.After(time.Now()) {
		t.Fatalf("fallback timestamp = %v", got[0].TS)
	}
}

func TestActivityWrapEditRevokeAndSeedOrder(t *testing.T) {
	t.Parallel()
	feed := New(3)
	add := func(id string) {
		feed.Add("a", history.Entry{TS: "2026-01-01T00:00:00Z", ID: id, Type: history.In, Chat: "chat", From: "sender", Text: id})
	}
	for _, id := range []string{"one", "two", "three", "four", "five"} {
		add(id)
	}
	feed.Change("a", Change{Target: "four", Sender: "sender", Chat: "chat"}, "edited")
	feed.Change("a", Change{Target: "three", Sender: "sender", Chat: "chat", Revoke: true}, "")
	add("six")
	got := feed.Recent(10, "")
	if len(got) != 3 || got[0].Text != "six" || got[1].Text != "five" || got[2].Text != "edited" {
		t.Fatalf("wrapped edit/revoke: %+v", got)
	}
	feed.Seed("b", func() []history.Entry {
		return []history.Entry{{TS: "2026-01-01T00:00:01Z", Type: history.In, Text: "seed"}}
	})
	got = feed.Recent(10, "")
	if len(got) != 3 || got[0].Text != "seed" || got[1].Text != "six" || got[2].Text != "five" {
		t.Fatalf("wrapped seed: %+v", got)
	}
}
