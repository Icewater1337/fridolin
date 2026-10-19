package main

import "github.com/Icewater1337/fridolin/internal/app"

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/google"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/store"
)

// InboundHandler receives every allowed message at the command boundary.
type InboundHandler func(context.Context, brain.Inbound)

func whatsAppSettings(get func() *config.File) func() whatsapp.Settings {
	return func() whatsapp.Settings {
		cfg := get()
		return whatsapp.Settings{Policy: app.AccessPolicy(cfg), MediaDir: cfg.MediaDir, MaxMediaBytes: cfg.MaxMediaBytes(), MaxChunkSize: cfg.MaxChunkSize}
	}
}

func whatsAppInboundHandler(handler InboundHandler) func(context.Context, chat.Inbound) {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, m chat.Inbound) { handler(ctx, brain.Inbound{Inbound: m}) }
}

func whatsAppVoteHandler(handler func(pollVote)) func(whatsapp.Vote) {
	if handler == nil {
		return nil
	}
	return func(v whatsapp.Vote) {
		handler(pollVote{Poll: store.SentPoll(v.Poll), Voter: v.Voter, Selected: v.Selected})
	}
}

func whatsAppGroupRules(rules func(string) groupRules) func(string) whatsapp.GroupRules {
	if rules == nil {
		return nil
	}
	return func(group string) whatsapp.GroupRules {
		r := rules(group)
		return whatsapp.GroupRules{Files: r.files, Forwards: r.forwards}
	}
}

type whatsAppPollStore struct{ *store.Store }

func (s whatsAppPollStore) SentPoll(ctx context.Context, id string) (whatsapp.SentPoll, error) {
	p, err := s.Store.SentPoll(ctx, id)
	return whatsapp.SentPoll(p), err
}

// NewWhatsAppClient supplies the command's config, store and runtime
// projections. Creating it does not connect; Connect keeps that boundary.
func NewWhatsAppClient(cfg func() *config.File, s *store.Store, handler InboundHandler, onIgnored whatsapp.IgnoredHandler) (*whatsapp.Client, error) {
	return whatsapp.New(whatsAppOptions(cfg, s, handler, onIgnored))
}

func whatsAppOptions(cfg func() *config.File, s *store.Store, handler InboundHandler, onIgnored whatsapp.IgnoredHandler) whatsapp.Options {
	// Keep the initial log-level read before loading the device.
	logger := logx.NewWaLogger("WhatsApp", cfg().LogLevel)
	return whatsapp.Options{Settings: whatsAppSettings(cfg), Container: s.Container, Polls: whatsAppPollStore{s}, OnInbound: whatsAppInboundHandler(handler), OnIgnored: onIgnored, Log: logger, Chunks: outboundmsg.Chunk, Identity: whatsAppIdentity(), Outbound: whatsAppOutbound(), Text: whatsAppText(), Inbox: mediaInboxAt}
}

func whatsAppOutbound() whatsapp.Outbound {
	return whatsapp.Outbound{Recipient: parseRecipient, Classify: classifySendErr, Chunks: func(ctx context.Context, chunks []string, send func(string) error) error {
		return outboundmsg.SendChunks(ctx, chunks, outboundmsg.OutboxPace, send)
	}}
}

func whatsAppText() whatsapp.Text {
	return whatsapp.Text{ViewOnce: outboundmsg.ViewOnceNotice, MediaFailed: outboundmsg.MediaFailedNotice, MediaTooLarge: outboundmsg.MediaTooLargeNotice,
		MaskSender: func(number string, resolved bool, server string) string {
			return (groupSender{Number: number, Resolved: resolved, Server: server}).masked()
		},
		MaskJID:  func(user, server string) string { return logx.MaskJID(user, "", server) },
		MaskChat: maskChatJID,
		Redact:   func(text string) string { return logx.RedactIdentifiers(text, google.MaskEmail) },
	}
}

func whatsAppIdentity() whatsapp.Identity {
	return whatsapp.Identity{Canonical: canonicalUser, LID: lidOf}
}

// whatsAppOptions is the production composition shared by construction and
// integration tests. Startup paths come from Boot; policy and limits stay live.
func (b *Bridge) whatsAppOptions() whatsapp.Options {
	opts := whatsAppOptions(func() *config.File { return app.RuntimeConfig(b.cfgs) }, b.store, b.handleInbound, b.recordIgnored)
	opts.OnVote = whatsAppVoteHandler(b.recordVote)
	opts.OnChange = b.handleChange
	opts.GroupRules = whatsAppGroupRules(b.groupRules)
	return opts
}
