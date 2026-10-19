package pipeline

import (
	"errors"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
)

// Ingest resolves already authorized input before scheduling its hand-off.
// The owner chat is from the request's original live configuration snapshot.
func (p *Workflow) Ingest(who chat.Principal, br *brain.Brain, requested []string, sender, text, owner string) (string, error) {
	paths, err := p.ResolveMedia(br.ID(), requested, who.Kind == chat.CallerBrain)
	if err != nil {
		return "", err
	}
	jid := p.ReplyChat(br, owner)
	if jid == "" {
		return "", errors.New("brain " + br.ID() + " has no chat to reply in — set reply_chat in its settings")
	}
	if sender == "" {
		sender = "upload"
	}
	if text == "" {
		text = "New files were uploaded."
	}
	go p.HandOff(br, jid, sender, text, paths)
	return jid, nil
}
