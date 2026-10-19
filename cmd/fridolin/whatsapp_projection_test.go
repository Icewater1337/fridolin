package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow/types"
)

func TestWhatsAppProjectionReadBoundaries(t *testing.T) {
	cfg := &Config{AllowedNumbers: []string{"15551234567"}, MediaDir: "incoming", MaxChunkSize: 4000, MaxMediaMB: 12}
	reads := 0
	get := func() *Config { reads++; return cfg }
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	wa, err := NewWhatsAppClient(get, s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wa.Disconnect)
	if reads != 1 {
		t.Fatalf("constructor config reads = %d, want initial log-level read only", reads)
	}
	first := wa.cfg()
	if reads != 2 || first.MediaDir != cfg.MediaDir || first.MaxChunkSize != 4000 || first.MaxMediaBytes != cfg.MaxMediaBytes() || !reflect.DeepEqual(first.AllowedNumbers, cfg.AllowedNumbers) {
		t.Fatalf("first settings = %+v, reads=%d", first, reads)
	}
	cfg = &Config{AllowedNumbers: []string{"15559876543"}, MediaDir: "later", MaxChunkSize: 2000, MaxMediaMB: 24}
	second := wa.cfg()
	if reads != 3 || second.MediaDir != "later" || second.MaxChunkSize != 2000 || second.MaxMediaBytes != cfg.MaxMediaBytes() || second.AllowedNumbers[0] != "15559876543" {
		t.Fatalf("later settings = %+v, reads=%d", second, reads)
	}
	if first.MediaDir != "incoming" || first.MaxChunkSize != 4000 || first.AllowedNumbers[0] != "15551234567" {
		t.Fatalf("earlier snapshot changed: %+v", first)
	}
}

func TestWhatsAppCallbackProjections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := chat.Inbound{JID: testDMChat, ChatAlt: personLID + "@lid", Sender: "15551234567", Text: "hello", Media: []string{"file"}, Quoted: &chat.Quote{From: "15559876543", Text: "earlier"}, Source: chat.SourceWhatsApp, ID: "message", Silent: true}
	calls := 0
	handler := whatsAppInboundHandler(func(gotCtx context.Context, m Inbound) {
		calls++
		if gotCtx != ctx || !reflect.DeepEqual(m.Inbound, in) || !reflect.ValueOf(m.logged).IsZero() || len(m.parts) != 0 {
			t.Fatalf("inbound projection = %+v", m)
		}
	})
	handler(ctx, in)
	if calls != 1 {
		t.Fatalf("inbound calls = %d", calls)
	}
	vote := waVote{Poll: waSentPoll{Chat: testDMChat, Brain: "main", Question: "when", Options: []string{"today", "tomorrow"}}, Voter: types.NewJID("15551234567", types.DefaultUserServer), Selected: []string{"today"}}
	whatsAppVoteHandler(func(got pollVote) {
		if !reflect.DeepEqual(waSentPoll(got.Poll), vote.Poll) || got.Voter != vote.Voter || !reflect.DeepEqual(got.Selected, vote.Selected) {
			t.Fatalf("vote projection = %+v", got)
		}
	})(vote)
	if whatsAppInboundHandler(nil) != nil || whatsAppVoteHandler(nil) != nil || whatsAppGroupRules(nil) != nil {
		t.Fatal("optional callback became non-nil")
	}
}
