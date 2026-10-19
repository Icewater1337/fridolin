package app

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// What a stop keeps in the inbound queue reads back whole after the store
// is opened again, in the order the next start takes it up, and taking it
// empties the queue.
func TestInboundQueueKeepsWhatTheStopLeft(t *testing.T) {
	sent := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)
	logged := func(id, text string) history.Entry {
		return history.Entry{TS: sent.Format(time.RFC3339), From: "15551234567", Chat: harnessDM, Text: text, Type: history.In, ID: id}
	}
	first := brain.Inbound{Inbound: chat.Inbound{JID: harnessDM, ChatAlt: "15559876543@lid", Sender: "15551234567", Text: "what is this?",
		Media: []string{"/Users/you/.fridolin/media/a.jpg"}, Source: chat.SourceWhatsApp, At: sent, ID: "3EB0A"}}
	firstSaved := first.Save()
	firstLog := logged("3EB0A", "what is this?")
	firstSaved.Logged = &firstLog
	first = firstSaved.Restore()
	second := brain.Inbound{Inbound: chat.Inbound{JID: harnessDM, Sender: "15551234567", Text: "and this", Quoted: &chat.Quote{From: "15559876543", Text: "lunch?"},
		Source: chat.SourceWhatsApp, At: sent.Add(time.Second), ID: "3EB0B"}}
	secondSaved := second.Save()
	secondLog := logged("3EB0B", "and this")
	secondSaved.Logged = &secondLog
	second = secondSaved.Restore()
	merged := first.Merge(second)
	mergedSaved := merged.Save()
	mergedSaved.Logged = first.Save().Logged
	merged = mergedSaved.Restore()
	chatter := brain.Inbound{Inbound: chat.Inbound{JID: harnessGroup, Sender: "15559876543", Text: "see you", Silent: true, Source: chat.SourceWhatsApp, At: sent, ID: "3EB0C"}}
	late := brain.Inbound{Inbound: chat.Inbound{JID: harnessDM, Sender: "15551234567", Text: "!stop", Source: chat.SourceWhatsApp, At: sent.Add(time.Minute), ID: "3EB0D"}}
	edit := chat.Change{Kind: chat.ChangeEdit, Chat: harnessDM, Sender: "15551234567", Target: "3EB0A", Text: "what is that?"}
	running := brain.Inbound{Inbound: chat.Inbound{JID: harnessGroup, Sender: "15551234567", Text: "summarise", Source: chat.SourceWhatsApp, At: sent, ID: "3EB0E"}}

	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, dbPath)
	ctx := t.Context()
	// In the order a stop keeps them, which is not the order to take
	// them up in.
	for _, keep := range []func() error{
		func() error { return KeepInbound(ctx, s, InboundLate, "", late) },
		func() error { return KeepInbound(ctx, s, InboundHeld, "", chatter) },
		func() error { return KeepInbound(ctx, s, InboundQueued, "main", merged) },
		func() error { return KeepChange(ctx, s, edit, sent.Add(2*time.Minute)) },
		func() error { return KeepInterrupted(ctx, s, "family", running) },
	} {
		if err := keep(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTestStore(t, dbPath)
	got, err := TakeInbound(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []KeptInbound{
		{Kind: InboundInterrupted, Chat: harnessGroup, Brain: "family", MsgID: "3EB0E", SentAt: sent},
		{Kind: InboundQueued, Chat: harnessDM, Brain: "main", MsgID: "3EB0A", SentAt: sent, Msg: merged},
		{Kind: InboundHeld, Chat: harnessGroup, MsgID: "3EB0C", SentAt: sent, Msg: chatter},
		{Kind: InboundLate, Chat: harnessDM, MsgID: "3EB0D", SentAt: sent.Add(time.Minute), Msg: late},
		{Kind: InboundChange, Chat: harnessDM, MsgID: "3EB0A", SentAt: sent.Add(2 * time.Minute), Change: edit},
	}
	if len(got) != len(want) {
		t.Fatalf("took %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		got[i].ID = 0
		got[i].SentAt = got[i].SentAt.UTC()
		got[i].Msg.At = got[i].Msg.At.UTC()
		saved := got[i].Msg.Save()
		for j := range saved.Parts {
			saved.Parts[j].At = saved.Parts[j].At.UTC()
		}
		got[i].Msg = saved.Restore()
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("row %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	if again, err := TakeInbound(ctx, s); err != nil || len(again) != 0 {
		t.Errorf("the queue still holds %+v (%v) once taken", again, err)
	}
}

// A row the bridge cannot read, written by another version say, is left
// out with an error, and the rest is taken all the same.
func TestInboundQueueSkipsARowItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, path)
	db := testStoreSQL(t, path)
	ctx := t.Context()
	m := brain.Inbound{Inbound: chat.Inbound{JID: harnessDM, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp, At: time.Now()}}
	if err := KeepInbound(ctx, s, InboundHeld, "", m); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO inbound_queue (kind, chat_jid, body, sent_at, created_at) VALUES ('held', 'x', '{not json', datetime('now'), datetime('now'))`,
		`INSERT INTO inbound_queue (kind, chat_jid, sent_at, created_at) VALUES ('newer', 'x', datetime('now'), datetime('now'))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := TakeInbound(ctx, s)
	if err == nil {
		t.Error("no error for the rows it could not read")
	}
	if len(got) != 1 || got[0].Msg.Text != "hello" {
		t.Errorf("took %+v, want the one message it can read", got)
	}
	if again, _ := TakeInbound(ctx, s); len(again) != 0 {
		t.Errorf("the queue still holds %+v", again)
	}
}
