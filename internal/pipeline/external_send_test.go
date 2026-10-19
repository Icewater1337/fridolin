package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/outbound"
)

type externalRequestKey struct{}

func TestExternalSendRecordsOnlyDeliveredParts(t *testing.T) {
	offline := errors.New("offline")
	partial := &outbound.PartialSendError{Sent: []string{"first", "second"}, Total: 3, Err: offline}
	for _, tc := range []struct {
		name  string
		err   error
		want  string
		calls int
	}{{"nothing", offline, "", 0}, {"partial", partial, "first\nsecond", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			who := chat.Principal{Kind: chat.CallerBrain, Brain: "notes"}
			ctx := context.WithValue(context.Background(), externalRequestKey{}, "request")
			recorded := 0
			p := &Workflow{
				SendChunked: func(got context.Context, jid, text string) error {
					if got != ctx || jid != "chat" || text != "whole" {
						t.Fatal("send inputs changed")
					}
					return tc.err
				},
				AppendSent: func(got chat.Principal, jid, text, request string) {
					recorded++
					if got != who || jid != "chat" || text != tc.want || request != "" {
						t.Fatalf("recorded %v %q %q %q", got, jid, text, request)
					}
				},
			}
			request, err := p.SendExternal(ctx, who, "chat", "whole")
			if err != tc.err || request != "" || recorded != tc.calls {
				t.Fatalf("request=%q error=%v records=%d", request, err, recorded)
			}
		})
	}
}
