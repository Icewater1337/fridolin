package main

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/pipeline"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
)

func messageSettingsOf(cfg *config.Config) pipeline.Settings {
	return pipeline.Settings{
		FirstTrigger: cfg.Triggers.First(), QueueNotify: cfg.QueueNotify(),
		OwnerChat: ownerChat(cfg),
	}
}

// newMessagePipeline projects live policy and current command capabilities.
// Notices resolve the transport at call time, after site-chat wrapping and
// with the same immediate-send behavior as the original bridge methods.
func (b *Bridge) newMessagePipeline() *pipeline.Workflow {
	return &pipeline.Workflow{
		Brains:      b.brainMgr.Manager,
		Settings:    func() pipeline.Settings { return messageSettingsOf(b.cfgs.Get()) },
		Notice:      func(ctx context.Context, jid, text string) error { return b.wa.SendMessage(ctx, jid, text) },
		SendChunked: func(ctx context.Context, jid, text string) error { return b.wa.SendChunked(ctx, jid, text) },
		AppendSent:  b.appendHTTPMessageToHistory,
		HandOffCode: b.handOffCode,
		Chats:       chatJIDs,
		ReplyChat: func(br *brain.Brain, ownerDM string) string {
			return replyChatForBrain(b.brainMgr.Manager, br, ownerDM)
		},
	}
}
