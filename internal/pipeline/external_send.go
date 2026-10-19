package pipeline

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
func (p *Workflow) SendExternal(ctx context.Context, who chat.Principal, jid, text string) (string, error) {
	if err := p.SendChunked(ctx, jid, text); err != nil {
		log.Printf("HTTP send error to %s: %v", jid, err)
		if partial, ok := errors.AsType[*outboundmsg.PartialSendError](err); ok {
			p.AppendSent(who, jid, strings.Join(partial.Sent, "\n"), "")
		}
		return "", err
	}
	request := ""
	if br := p.Brains.BrainFor(jid, chat.IsGroupJID(jid)); br != nil && br.AsksForCode(text) {
		request = otp.NewRequestID()
	}
	p.AppendSent(who, jid, text, request)
	return request, nil
}
