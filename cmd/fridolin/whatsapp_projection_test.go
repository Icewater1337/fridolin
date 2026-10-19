package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow/types"
)

func TestWhatsAppProjectionReadBoundaries(t *testing.T) {
	cfg := &config.File{AllowedNumbers: []string{"15551234567"}, MediaDir: "incoming", MaxChunkSize: 4000, MaxMediaMB: 12}
	reads := 0
	get := func() *config.File { reads++; return cfg }
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	opts := whatsAppOptions(get, s, nil, nil)
	wa, err := whatsapp.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wa.Disconnect)
	if reads != 1 {
		t.Fatalf("constructor config reads = %d, want initial log-level read only", reads)
	}
	first := opts.Settings()
	if reads != 2 || first.MediaDir != cfg.MediaDir || first.MaxChunkSize != 4000 || first.MaxMediaBytes != cfg.MaxMediaBytes() || !reflect.DeepEqual(first.AllowedNumbers, cfg.AllowedNumbers) {
		t.Fatalf("first settings = %+v, reads=%d", first, reads)
	}
	cfg = &config.File{AllowedNumbers: []string{"15559876543"}, MediaDir: "later", MaxChunkSize: 2000, MaxMediaMB: 24}
	second := opts.Settings()
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
	handler := whatsAppInboundHandler(func(gotCtx context.Context, m brain.Inbound) {
		calls++
		if gotCtx != ctx || !reflect.DeepEqual(m.Inbound, in) || m.Save().Logged != nil || len(m.Save().Parts) != 0 {
			t.Fatalf("inbound projection = %+v", m)
		}
	})
	handler(ctx, in)
	if calls != 1 {
		t.Fatalf("inbound calls = %d", calls)
	}
	vote := whatsapp.Vote{Poll: whatsapp.SentPoll{Chat: testDMChat, Brain: "main", Question: "when", Options: []string{"today", "tomorrow"}}, Voter: types.NewJID("15551234567", types.DefaultUserServer), Selected: []string{"today"}}
	whatsAppVoteHandler(func(got pollVote) {
		if !reflect.DeepEqual(whatsapp.SentPoll(got.Poll), vote.Poll) || got.Voter != vote.Voter || !reflect.DeepEqual(got.Selected, vote.Selected) {
			t.Fatalf("vote projection = %+v", got)
		}
	})(vote)
	if whatsAppInboundHandler(nil) != nil || whatsAppVoteHandler(nil) != nil || whatsAppGroupRules(nil) != nil {
		t.Fatal("optional callback became non-nil")
	}
}
