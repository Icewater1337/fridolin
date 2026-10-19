package main

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
	first := Inbound{JID: harnessDM, ChatAlt: "15559876543@lid", Sender: "15551234567", Text: "what is this?",
		Media: []string{"/Users/you/.fridolin/media/a.jpg"}, Source: sourceWhatsApp, At: sent, ID: "3EB0A", logged: logged("3EB0A", "what is this?")}
	second := Inbound{JID: harnessDM, Sender: "15551234567", Text: "and this", Quoted: &Quote{From: "15559876543", Text: "lunch?"},
		Source: sourceWhatsApp, At: sent.Add(time.Second), ID: "3EB0B", logged: logged("3EB0B", "and this")}
	merged := first.merge(second)
	merged.logged = first.logged
	chatter := Inbound{JID: harnessGroup, Sender: "15559876543", Text: "see you", Silent: true, Source: sourceWhatsApp, At: sent, ID: "3EB0C"}
	late := Inbound{JID: harnessDM, Sender: "15551234567", Text: "!stop", Source: sourceWhatsApp, At: sent.Add(time.Minute), ID: "3EB0D"}
	edit := messageChange{Kind: changeEdit, Chat: harnessDM, Sender: "15551234567", Target: "3EB0A", Text: "what is that?"}
	running := Inbound{JID: harnessGroup, Sender: "15551234567", Text: "summarise", Source: sourceWhatsApp, At: sent, ID: "3EB0E"}

	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, dbPath)
	ctx := t.Context()
	// In the order a stop keeps them, which is not the order to take
	// them up in.
	for _, keep := range []func() error{
		func() error { return keepInbound(ctx, s, inboundLate, "", late) },
		func() error { return keepInbound(ctx, s, inboundHeld, "", chatter) },
		func() error { return keepInbound(ctx, s, inboundQueued, "main", merged) },
		func() error { return keepChange(ctx, s, edit, sent.Add(2*time.Minute)) },
		func() error { return keepInterrupted(ctx, s, "family", running) },
	} {
		if err := keep(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTestStore(t, dbPath)
	got, err := takeInbound(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []keptInbound{
		{Kind: inboundInterrupted, Chat: harnessGroup, Brain: "family", MsgID: "3EB0E", SentAt: sent},
		{Kind: inboundQueued, Chat: harnessDM, Brain: "main", MsgID: "3EB0A", SentAt: sent, Msg: merged},
		{Kind: inboundHeld, Chat: harnessGroup, MsgID: "3EB0C", SentAt: sent, Msg: chatter},
		{Kind: inboundLate, Chat: harnessDM, MsgID: "3EB0D", SentAt: sent.Add(time.Minute), Msg: late},
		{Kind: inboundChange, Chat: harnessDM, MsgID: "3EB0A", SentAt: sent.Add(2 * time.Minute), Change: edit},
	}
	if len(got) != len(want) {
		t.Fatalf("took %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		got[i].ID = 0
		got[i].SentAt = got[i].SentAt.UTC()
		got[i].Msg.At = got[i].Msg.At.UTC()
		for j := range got[i].Msg.parts {
			got[i].Msg.parts[j].At = got[i].Msg.parts[j].At.UTC()
		}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("row %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	if again, err := takeInbound(ctx, s); err != nil || len(again) != 0 {
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
	m := Inbound{JID: harnessDM, Sender: "15551234567", Text: "hello", Source: sourceWhatsApp, At: time.Now()}
	if err := keepInbound(ctx, s, inboundHeld, "", m); err != nil {
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
	got, err := takeInbound(ctx, s)
	if err == nil {
		t.Error("no error for the rows it could not read")
	}
	if len(got) != 1 || got[0].Msg.Text != "hello" {
		t.Errorf("took %+v, want the one message it can read", got)
	}
	if again, _ := takeInbound(ctx, s); len(again) != 0 {
		t.Errorf("the queue still holds %+v", again)
	}
}
