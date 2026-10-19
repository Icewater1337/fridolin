package main

import (
	"context"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"net/http"
)

func (b *Bridge) brainAPIHandler() http.Handler {
	return (&BrainAPI{
		BrainForToken: func(tok string) *brainruntime.Brain { return b.brainMgr.Get(b.runTokens.brainOf(tok)) },
		OwnsChat:      b.ownsChat,
		ReadPoll: func(ctx context.Context, id string) (string, any, error) {
			p, err := b.store.Poll(ctx, id)
			if err != nil {
				return "", nil, err
			}
			return p.ChatJID, p, nil
		},
		RecentPolls: func(ctx context.Context, jid string, n int, keep func(string) bool) (any, int, error) {
			p, err := b.store.RecentPolls(ctx, jid, n, keep)
			return p, len(p), err
		},
		Google: func() BrainCalendarService { return b.google },
	}).Handler()
}
