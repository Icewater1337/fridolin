package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveRuns(t *testing.T) {
	chatter := Inbound{JID: testGroup, Sender: "15551234567", Text: "we meet at noon", Silent: true}
	junk := Inbound{JID: testGroup, Sender: "15551234567", Text: "ok", Silent: true}
	file := Inbound{JID: testGroup, Sender: "15551234567", Media: []string{"/m/1.jpg"}, Silent: true}
	cases := []struct {
		name string
		mode string
		m    Inbound
		want bool
	}{
		{"chatter, default", "", chatter, false},
		{"chatter, history", observeHistory, chatter, false},
		{"chatter, run", observeRun, chatter, true},
		{"junk, run", observeRun, junk, false},
		{"a file, run", observeRun, file, true},
		{"a file, history", observeHistory, file, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := observeRuns(c.mode, c.m); got != c.want {
				t.Errorf("observeRuns(%q, %q) = %v, want %v", c.mode, c.m.Text, got, c.want)
			}
		})
	}
}

func TestManifestObserve(t *testing.T) {
	cases := []struct {
		observe, normalized string
		ok                  bool
	}{
		{"", observeHistory, true},
		{observeHistory, observeHistory, true},
		{observeRun, observeRun, true},
		{"always", "", false},
		{"Run", "", false},
	}
	for _, c := range cases {
		t.Run(c.observe, func(t *testing.T) {
			m := BrainManifest{ID: "g", Name: "G", Observe: c.observe}
			if err := m.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() = %v, want ok %v", err, c.ok)
			}
			if c.ok && m.normalized().Observe != c.normalized {
				t.Errorf("normalized observe = %q, want %q", m.normalized().Observe, c.normalized)
			}
		})
	}
	// A brain.json written before the setting existed is the same brain as
	// its approved copy that has the default spelt out: no drift.
	if !(BrainManifest{ID: "g", Name: "G"}).Equal(BrainManifest{ID: "g", Name: "G", Observe: observeHistory}) {
		t.Error("a manifest without observe differs from one with the default")
	}
}

// Untagged group chatter goes into the brain's history, and gets a run of
// its own only when the brain observes with runs. Either way nothing is
// sent back.
func TestUntaggedChatterRunsOnlyWhenObserved(t *testing.T) {
	cases := []struct {
		observe string
		runs    int // Claude runs for the chatter and the DM after it
	}{
		{"", 1},
		{observeHistory, 1},
		{observeRun, 2},
	}
	for _, c := range cases {
		t.Run("observe "+c.observe, func(t *testing.T) {
			tb, br := newMainBridgeWith(t, `, "queue": {"notify": false}`, BrainManifest{Observe: c.observe})
			argsLog := filepath.Join(tb.home, "args.log")
			t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
			ctx := context.Background()
			tb.handleInbound(ctx, Inbound{JID: testGroup, Sender: otherNumber, Text: "we meet at noon", Silent: true, Source: sourceWhatsApp})
			waitFor(t, 5*time.Second, "the chatter in the history", func() bool { return len(readHistory(t, br)) == 1 })
			if c.runs > 1 {
				// The DM would stop the observe-only run, see
				// TestAddressedWorkGoesAheadOfObserveRuns.
				waitFor(t, 5*time.Second, "the observe-only run", func() bool { return br.Snapshot().Last != nil })
			}
			tb.handleInbound(ctx, Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: sourceWhatsApp})
			waitFor(t, 10*time.Second, "the DM's answer", func() bool { return len(tb.sentTo(testDMChat)) == 1 })

			data, _ := os.ReadFile(argsLog)
			if runs := strings.Count(string(data), "\n"); runs != c.runs {
				t.Errorf("%d Claude runs, want %d", runs, c.runs)
			}
			var chatter []string
			for _, e := range readHistory(t, br) {
				if e.Chat == testGroup {
					chatter = append(chatter, e.Type+": "+e.Text)
				}
			}
			if strings.Join(chatter, "|") != "in: we meet at noon" {
				t.Errorf("the group's history = %q, want only the chatter", chatter)
			}
			if sent := tb.sentTo(testGroup); len(sent) > 0 {
				t.Errorf("sent %q to the group", sent)
			}
		})
	}
}
