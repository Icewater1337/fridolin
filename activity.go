package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"time"
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
	Type  string    `json:"type"` // in or out
	Text  string    `json:"text"`
}

// NewActivity keeps up to max entries.
func NewActivity(max int) *Activity { return &Activity{max: max, seeded: map[string]bool{}} }

// Add records a history line.
func (a *Activity) Add(brainID string, e historyEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addLocked(brainID, e)
}

func (a *Activity) addLocked(brainID string, e historyEntry) {
	ts, err := time.Parse(time.RFC3339, e.TS)
	if err != nil {
		ts = time.Now()
	}
	a.entries = append(a.entries, ActivityEntry{TS: ts, Brain: brainID, Chat: e.Chat, From: e.From, Type: e.Type, Text: truncate(e.Text, 600)})
	if over := len(a.entries) - a.max; over > 0 {
		a.entries = append([]ActivityEntry(nil), a.entries[over:]...)
	}
}

// Seed loads the last messages of a brain's history once per brain.
func (a *Activity) Seed(brainID, dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seeded[brainID] {
		return
	}
	a.seeded[brainID] = true
	lines := tailLines(filepath.Join(dir, "conversation-history.jsonl"), 128*1024)
	if len(lines) > 50 {
		lines = lines[len(lines)-50:]
	}
	for _, l := range lines {
		var e historyEntry
		if json.Unmarshal([]byte(l), &e) == nil {
			a.addLocked(brainID, e)
		}
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
