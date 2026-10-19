package main

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow/types"
	"net/http"
)

func (b *Bridge) newScriptAPI() *ScriptAPI {
	return &ScriptAPI{
		Authorize:     func(r *http.Request) (chat.Principal, error) { return b.scriptAuth.authorize(r) },
		Recipient:     b.scriptRecipient,
		MaySend:       func(who chat.Principal, jid string) error { return b.maySend(who, jid, b.cfgs.Get()) },
		TokenRequired: errTokenRequired,
		SendExternal: func(ctx context.Context, who chat.Principal, jid, text string) (string, error) {
			return b.pipeline.SendExternal(ctx, who, jid, text)
		},
		IngestPolicy: func() IngestPolicy {
			cfg := b.cfgs.Get()
			return IngestPolicy{Enabled: cfg.Ingest.Enabled, OwnerChat: ownerChat(cfg)}
		},
		Brain: func(id string) *brain.Brain { return b.brainMgr.Get(id) },
		IngestTarget: func(who chat.Principal, br *brain.Brain, paths []string, sender, text, owner string) (string, error) {
			return b.pipeline.Ingest(who, br, paths, sender, text, owner)
		},
	}
}
func (b *Bridge) scriptRecipient(ctx context.Context, raw string) (string, error) {
	to, err := chatJID(raw)
	if err == nil {
		err = checkRecipient(to)
	}
	if err != nil {
		return "", err
	}
	// Preserve number-based history when a recipient arrives as a LID.
	if to.Server == types.HiddenUserServer && b.lids != nil {
		to = canonicalUser(ctx, b.lids(), to, types.EmptyJID)
	}
	return to.String(), nil
}
