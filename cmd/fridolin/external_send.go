package main

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/otp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// sendExternal sends an already authorized message and records only what
// reached the chat. The HTTP adapter owns parsing, authorization and status codes.
func (b *Bridge) sendExternal(ctx context.Context, who chat.Principal, jid, text string) (string, error) {
	if err := b.wa.SendChunked(ctx, jid, text); err != nil {
		log.Printf("HTTP send error to %s: %v", jid, err)
		var partial *outboundmsg.PartialSendError
		if errors.As(err, &partial) {
			b.appendHTTPMessageToHistory(who, jid, strings.Join(partial.Sent, "\n"), "")
		}
		return "", err
	}
	request := ""
	if br := b.brainMgr.BrainFor(jid, chat.IsGroupJID(jid)); br != nil && br.AsksForCode(text) {
		request = otp.NewRequestID()
	}
	b.appendHTTPMessageToHistory(who, jid, text, request)
	return request, nil
}
