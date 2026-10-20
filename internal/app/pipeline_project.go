package app

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/pipeline"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
)

func messageSettingsOf(settings *config.Settings) pipeline.Settings {
	cfg := settings.Live
	return pipeline.Settings{
		FirstTrigger: cfg.Triggers.First(), QueueNotify: cfg.Queue.Notify,
		OwnerChat: ownerChatNumbers(cfg.AllowedNumbers), MaxChunkSize: cfg.MaxChunkSize,
	}
}

// newMessagePipeline projects live policy and current command capabilities.
// Notices resolve the transport at call time, after site-chat wrapping and
// with the same immediate-send behavior as the original bridge methods.
func (b *App) newMessagePipeline() *pipeline.Workflow {
	return &pipeline.Workflow{
		SendQuestion: func(ctx context.Context, id, jid, text string, max int) error {
			return b.outbox.Question(ctx, id, jid, text, max)
		},
		Reply: func(ctx context.Context, id, jid, text string, max int) error {
			return b.outbox.Reply(ctx, id, jid, text, max)
		},
		Brains:      b.brainMgr.Manager,
		Settings:    func() pipeline.Settings { return messageSettingsOf(b.cfgs.Get()) },
		Notice:      func(ctx context.Context, jid, text string) error { return b.wa.SendMessage(ctx, jid, text) },
		SendChunked: func(ctx context.Context, jid, text string) error { return b.wa.SendChunked(ctx, jid, text) },
		AppendSent:  b.appendHTTPMessageToHistory,
		ResolveMedia: func(id string, paths []string, scoped bool) ([]string, error) {
			return mediaInboxAt(b.cfgs.Boot().MediaDir).Resolve(id, paths, scoped)
		},
		HandOff:     b.handOff,
		Fleet:       b.fleetText,
		HandOffCode: b.handOffCode,
		Lockdown:    b.lockdownCommand,
		Answer:      b.answerText,
		Chats:       chatJIDs,
		ReplyChat: func(br *brain.Brain, ownerDM string) string {
			return replyChatForBrain(b.brainMgr.Manager, br, ownerDM)
		},
	}
}
