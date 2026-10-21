package devchat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDevMessenger(t *testing.T) {
	calls := 0
	d := New(func(text string) []string { calls++; return []string{text + " first", text + " second"} })
	ctx := context.Background()
	if err := d.Connect(); err != nil {
		t.Fatal(err)
	}
	defer d.Disconnect()
	select {
	case <-d.Connected():
	default:
		t.Fatal("dev messenger is not connected")
	}
	if st := d.State(); st.Mode != "disabled" || st.Detail != "Dev mode (--no-whatsapp): messages are simulated" {
		t.Fatalf("state = %+v", st)
	}
	if err := d.StartPairing(); err == nil || err.Error() != "WhatsApp is disabled in dev mode" {
		t.Fatalf("pairing = %v", err)
	}
	if groups, err := d.JoinedGroups(ctx); err != nil || groups != nil {
		t.Fatalf("groups = %v, %v", groups, err)
	}
	if box := d.Outbox(); box == nil || len(box) != 0 {
		t.Fatalf("initial outbox = %#v", box)
	}
	before := time.Now()
	if err := d.SendMessage(ctx, "chat", "plain"); err != nil {
		t.Fatal(err)
	}
	if err := d.SendChunked(ctx, "chat", "chunk"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("chunk callback calls = %d", calls)
	}
	// The poll's id comes before the poll shows, where it can be voted on.
	var kept string
	id, err := d.SendPoll(ctx, "chat", "when", []string{"today", "tomorrow"}, 1, func(id string) {
		kept = id
		if len(d.Outbox()) != 3 {
			t.Errorf("the poll showed before it was kept: %+v", d.Outbox())
		}
	})
	if err != nil || !strings.HasPrefix(id, "dev-poll-") || kept != id {
		t.Fatalf("poll = %q, kept %q, %v", id, kept, err)
	}
	box := d.Outbox()
	if len(box) != 4 {
		t.Fatalf("outbox = %+v", box)
	}
	for i, want := range []string{"when", "chunk second", "chunk first", "plain"} {
		if box[i].Text != want || box[i].JID != "chat" || box[i].TS.Before(before) || box[i].TS.After(time.Now()) {
			t.Errorf("row %d = %+v", i, box[i])
		}
	}
	if strings.Join(box[0].Poll, ",") != "today,tomorrow" {
		t.Errorf("poll choices = %v", box[0].Poll)
	}
	box[0].Text = "changed returned snapshot"
	if d.Outbox()[0].Text != "when" {
		t.Fatal("outbox snapshot aliases internal entries")
	}
}

func TestDevMessengerBoundAndConcurrency(t *testing.T) {
	d := New(nil)
	ctx := context.Background()
	for i := range 201 {
		if err := d.SendMessage(ctx, "chat", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	box := d.Outbox()
	if len(box) != 200 || box[0].Text != "200" || box[199].Text != "1" {
		t.Fatalf("bounded outbox = %d, newest=%q oldest=%q", len(box), box[0].Text, box[len(box)-1].Text)
	}
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			for i := range 75 {
				if err := d.SendMessage(ctx, "chat", fmt.Sprintf("%d:%d", worker, i)); err != nil {
					t.Error(err)
				}
				if got := d.Outbox(); len(got) > 200 || len(got) == 0 {
					t.Errorf("concurrent outbox size %d", len(got))
				}
			}
		})
	}
	workers.Wait()
	if len(d.Outbox()) != 200 {
		t.Fatal("outbox did not remain bounded")
	}
}

func TestDevMessageJSON(t *testing.T) {
	m := Message{TS: time.Unix(0, 0).UTC(), JID: "chat", Text: "hello"}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ts":"1970-01-01T00:00:00Z","jid":"chat","text":"hello"}`; string(got) != want {
		t.Fatalf("message JSON = %s, want %s", got, want)
	}
	m.Poll = []string{"a", "b"}
	got, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ts":"1970-01-01T00:00:00Z","jid":"chat","text":"hello","poll":["a","b"]}`; string(got) != want {
		t.Fatalf("poll JSON = %s, want %s", got, want)
	}
}
