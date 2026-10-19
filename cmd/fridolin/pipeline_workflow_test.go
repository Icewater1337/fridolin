package main

import (
	"context"
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

type pipelineNoticeRecorder struct {
	Messenger
	texts []string
}

func (r *pipelineNoticeRecorder) SendMessage(_ context.Context, _, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

// The pipeline is constructed before the site's transport wrapper. A notice
// must use the current transport and the current trigger after a settings edit.
func TestMessagePipelineReadsLiveSettingsAndTransport(t *testing.T) {
	tb, _ := newMainBridge(t, "")
	original := tb.wa
	recorder := &pipelineNoticeRecorder{Messenger: original}
	tb.wa = recorder
	t.Cleanup(func() { tb.wa = original })
	for _, keyword := range []string{"@first", "@second"} {
		if _, err := tb.cfgs.Update("", func(c *config.File) error {
			c.Triggers = trigger.FromStrings([]string{keyword})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: harnessGroup, Sender: "15551234567", Text: " !help "}})
	}
	want := []string{outboundmsg.HelpReply("@first"), outboundmsg.HelpReply("@second")}
	if !slices.Equal(recorder.texts, want) {
		t.Fatalf("notices = %q, want %q", recorder.texts, want)
	}
}
