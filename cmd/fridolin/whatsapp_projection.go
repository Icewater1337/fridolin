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
	// Keep the initial log-level read before loading the device.
	logger := logx.NewWaLogger("WhatsApp", cfg().LogLevel)
	return newWhatsAppClient(waOptions{Settings: whatsAppSettings(cfg), Container: s.Container, Polls: whatsAppPollStore{s}, OnInbound: whatsAppInboundHandler(handler), OnIgnored: onIgnored, Log: logger, Chunks: ChunkMessage})
}
