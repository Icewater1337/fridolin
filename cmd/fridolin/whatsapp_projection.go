package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/store"
)

// InboundHandler receives every allowed message at the command boundary.
type InboundHandler func(context.Context, Inbound)

func whatsAppSettings(get func() *Config) func() waSettings {
	return func() waSettings {
		cfg := get()
		return waSettings{Policy: policyOf(cfg), MediaDir: cfg.MediaDir, MaxMediaBytes: cfg.MaxMediaBytes(), MaxChunkSize: cfg.MaxChunkSize}
	}
}

func whatsAppInboundHandler(handler InboundHandler) func(context.Context, chat.Inbound) {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, m chat.Inbound) { handler(ctx, Inbound{Inbound: m}) }
}

func whatsAppVoteHandler(handler func(pollVote)) func(waVote) {
	if handler == nil {
		return nil
	}
	return func(v waVote) { handler(pollVote{Poll: store.SentPoll(v.Poll), Voter: v.Voter, Selected: v.Selected}) }
}

func whatsAppGroupRules(rules func(string) groupRules) func(string) waGroupRules {
	if rules == nil {
		return nil
	}
	return func(group string) waGroupRules {
		r := rules(group)
		return waGroupRules{Files: r.files, Forwards: r.forwards}
	}
}

type whatsAppPollStore struct{ *store.Store }

func (s whatsAppPollStore) SentPoll(ctx context.Context, id string) (waSentPoll, error) {
	p, err := s.Store.SentPoll(ctx, id)
	return waSentPoll(p), err
}

// NewWhatsAppClient supplies the command's config, store and runtime
// projections. Creating it does not connect; Connect keeps that boundary.
func NewWhatsAppClient(cfg func() *Config, s *store.Store, handler InboundHandler, onIgnored IgnoredHandler) (*WhatsAppClient, error) {
	return newWhatsAppClient(whatsAppOptions(cfg, s, handler, onIgnored))
}

func whatsAppOptions(cfg func() *Config, s *store.Store, handler InboundHandler, onIgnored IgnoredHandler) waOptions {
	// Keep the initial log-level read before loading the device.
	logger := logx.NewWaLogger("WhatsApp", cfg().LogLevel)
	return waOptions{Settings: whatsAppSettings(cfg), Container: s.Container, Polls: whatsAppPollStore{s}, OnInbound: whatsAppInboundHandler(handler), OnIgnored: onIgnored, Log: logger, Chunks: ChunkMessage, Identity: whatsAppIdentity(), Outbound: whatsAppOutbound(), Text: whatsAppText(), Inbox: mediaInboxAt}
}

func whatsAppOutbound() waOutbound {
	return waOutbound{Recipient: parseRecipient, Classify: classifySendErr, Chunks: func(ctx context.Context, chunks []string, send func(string) error) error {
		return sendChunks(ctx, chunks, outboxPace, send)
	}}
}

func whatsAppText() waText {
	return waText{ViewOnce: viewOnceNotice, MediaFailed: mediaFailedNotice, MediaTooLarge: mediaTooLargeNotice,
		MaskSender: func(number string, resolved bool, server string) string {
			return (groupSender{Number: number, Resolved: resolved, Server: server}).masked()
		},
		MaskJID:  func(user, server string) string { return maskJID(user, "", server) },
		MaskChat: maskChatJID,
		Redact:   func(text string) string { return (&redactor{}).text(text) },
	}
}

// saveAttachment is the existing command test seam; the transport consumes
// a supplied inbox directly, without reading process state itself.
func saveAttachment(ctx context.Context, dl mediaDownloader, dir string, att attachment, maxBytes int64) (string, error) {
	return saveAttachmentTo(ctx, dl, mediaInboxAt(dir), att, maxBytes)
}

func whatsAppIdentity() waIdentity {
	return waIdentity{Canonical: canonicalUser, LID: lidOf}
}
