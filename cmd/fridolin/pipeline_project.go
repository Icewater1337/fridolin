package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
)

func messageSettingsOf(cfg *config.Config) messageSettings {
	return messageSettings{
		FirstTrigger: cfg.Triggers.First(), QueueNotify: cfg.QueueNotify(),
		OwnerChat: ownerChat(cfg),
	}
}

// newMessagePipeline projects live policy and current command capabilities.
// Notices resolve the transport at call time, after site-chat wrapping and
// with the same immediate-send behavior as the original bridge methods.
func (b *Bridge) newMessagePipeline() *messagePipeline {
	return &messagePipeline{
		brains:      b.brainMgr.Manager,
		settings:    func() messageSettings { return messageSettingsOf(b.cfgs.Get()) },
		notice:      func(ctx context.Context, jid, text string) error { return b.wa.SendMessage(ctx, jid, text) },
		handOffCode: b.handOffCode,
		chats:       chatJIDs,
		replyChat: func(br *brain.Brain, ownerDM string) string {
			return replyChatForBrain(b.brainMgr.Manager, br, ownerDM)
		},
	}
}
