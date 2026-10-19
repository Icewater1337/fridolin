package activity

import (
	"sort"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// Feed is the dashboard's feed of recent messages across all brains. It is
// fed live by every history write and seeded at startup from the tail of each
// brain's conversation-history.jsonl, so it is never written to disk itself.
type Feed struct {
	mu      sync.Mutex
	entries []Entry
	max     int
	start   int
	seeded  map[string]bool
}

// Entry is one message in the feed.
type Entry struct {
	TS    time.Time `json:"ts"`
	Brain string    `json:"brain"`
	Chat  string    `json:"chat"`
	From  string    `json:"from"`
	Type  string    `json:"type"` // in, out or event
	Text  string    `json:"text"`
	id    string    // the WhatsApp message's, for an edit or a deletion of it
}

// New keeps up to max entries.
func New(max int) *Feed {
	if max < 0 {
		max = 0
	}
	return &Feed{max: max, seeded: map[string]bool{}}
}

// Add records a history line. A reset marker is no message and stays out.
func (a *Feed) Add(brainID string, e history.Entry) {
	if e.Type == history.Reset {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addLocked(brainID, e)
}

func (a *Feed) addLocked(brainID string, e history.Entry) {
	if a.max == 0 {
		return
	}
	ts, err := time.Parse(time.RFC3339, e.TS)
	if err != nil {
		ts = time.Now()
	}
	entry := Entry{TS: ts, Brain: brainID, Chat: e.Chat, From: e.From, Type: e.Type, Text: textx.Truncate(e.Text, 600), id: e.ID}
	if len(a.entries) < a.max {
		a.entries = append(a.entries, entry)
	} else {
		a.entries[a.start] = entry
		a.start = (a.start + 1) % a.max
	}
}

// Change identifies an incoming message to edit or remove from the feed.
// The caller supplies the replacement history text separately.
type Change struct {
	Target, Sender, Chat, ChatAlt string
	Revoke                        bool
}

// Change does to the feed what recordChange did to brainID's history: the
// message c deletes leaves it, and the one an edit changed while it waited
// gets text, its new history text; "" leaves it as it was.
func (a *Feed) Change(brainID string, c Change, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.linearizeLocked()
	kept := a.entries[:0]
	for _, e := range a.entries {
		is := e.Brain == brainID && e.id != "" && e.id == c.Target && e.Type == history.In && e.From == c.Sender &&
			(e.Chat == c.Chat || (c.ChatAlt != "" && e.Chat == c.ChatAlt))
		switch {
		case is && c.Revoke:
			continue
		case is && text != "":
			e.Text = textx.Truncate(text, 600)
		}
		kept = append(kept, e)
	}
	clear(a.entries[len(kept):])
	a.entries = kept
}

// Seed loads the last messages of a brain's history once per brain.
// load runs while the feed is locked and must not call back into the feed.
func (a *Feed) Seed(brainID string, load func() []history.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seeded[brainID] {
		return
	}
	a.seeded[brainID] = true
	for _, e := range load() {
		a.addLocked(brainID, e)
	}
	a.linearizeLocked()
	sort.SliceStable(a.entries, func(i, j int) bool { return a.entries[i].TS.Before(a.entries[j].TS) })
}

// Recent returns up to limit entries, newest first, optionally for one brain.
func (a *Feed) Recent(limit int, brainID string) []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Entry{}
	for i := len(a.entries) - 1; i >= 0 && len(out) < limit; i-- {
		entry := a.entries[(a.start+i)%len(a.entries)]
		if brainID == "" || entry.Brain == brainID {
			out = append(out, entry)
		}
	}
	return out
}

// Edits and startup seeding need a chronological slice; ordinary writes do not.
func (a *Feed) linearizeLocked() {
	if a.start == 0 {
		return
	}
	ordered := make([]Entry, 0, len(a.entries))
	ordered = append(ordered, a.entries[a.start:]...)
	ordered = append(ordered, a.entries[:a.start]...)
	a.entries = ordered
	a.start = 0
}
