package main

import (
	"sort"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// Activity is the dashboard's feed of recent messages across all brains. It is
// fed live by every history write and seeded at startup from the tail of each
// brain's conversation-history.jsonl, so it is never written to disk itself.
type Activity struct {
	mu      sync.Mutex
	entries []ActivityEntry
	max     int
	seeded  map[string]bool
}

// ActivityEntry is one message in the feed.
type ActivityEntry struct {
	TS    time.Time `json:"ts"`
	Brain string    `json:"brain"`
	Chat  string    `json:"chat"`
	From  string    `json:"from"`
	Type  string    `json:"type"` // in, out or event
	Text  string    `json:"text"`
	id    string    // the WhatsApp message's, for an edit or a deletion of it
}

// NewActivity keeps up to max entries.
func NewActivity(max int) *Activity { return &Activity{max: max, seeded: map[string]bool{}} }

// Add records a history line. A reset marker is no message and stays out.
func (a *Activity) Add(brainID string, e history.Entry) {
	if e.Type == history.Reset {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addLocked(brainID, e)
}

func (a *Activity) addLocked(brainID string, e history.Entry) {
	ts, err := time.Parse(time.RFC3339, e.TS)
	if err != nil {
		ts = time.Now()
	}
	a.entries = append(a.entries, ActivityEntry{TS: ts, Brain: brainID, Chat: e.Chat, From: e.From, Type: e.Type, Text: textx.Truncate(e.Text, 600), id: e.ID})
	if over := len(a.entries) - a.max; over > 0 {
		a.entries = append([]ActivityEntry(nil), a.entries[over:]...)
	}
}

// Change does to the feed what recordChange did to brainID's history: the
// message c deletes leaves it, and the one an edit changed while it waited
// gets text, its new history text; "" leaves it as it was.
func (a *Activity) Change(brainID string, c messageChange, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := a.entries[:0]
	for _, e := range a.entries {
		is := e.Brain == brainID && e.id != "" && e.id == c.Target && e.Type == history.In && e.From == c.Sender &&
			(e.Chat == c.Chat || (c.ChatAlt != "" && e.Chat == c.ChatAlt))
		switch {
		case is && c.Kind == changeRevoke:
			continue
		case is && text != "":
			e.Text = textx.Truncate(text, 600)
		}
		kept = append(kept, e)
	}
	a.entries = kept
}

// Seed loads the last messages of a brain's history once per brain.
func (a *Activity) Seed(br *Brain) {
	a.mu.Lock()
	defer a.mu.Unlock()
	brainID := br.ID()
	if a.seeded[brainID] {
		return
	}
	a.seeded[brainID] = true
	for _, e := range br.recentHistory(50, 128<<10) {
		a.addLocked(brainID, e)
	}
	sort.SliceStable(a.entries, func(i, j int) bool { return a.entries[i].TS.Before(a.entries[j].TS) })
}

// Recent returns up to limit entries, newest first, optionally for one brain.
func (a *Activity) Recent(limit int, brainID string) []ActivityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []ActivityEntry{}
	for i := len(a.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if brainID == "" || a.entries[i].Brain == brainID {
			out = append(out, a.entries[i])
		}
	}
	return out
}
