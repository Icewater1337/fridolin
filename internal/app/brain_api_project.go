package app

import (
	"context"
	"net/http"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) brainAPIHandler() http.Handler {
	return (&httpapi.BrainAPI{
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
		Google: func() httpapi.BrainCalendarService { return b.google },
	}).Handler()
}
