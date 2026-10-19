package main

import (
	"context"
	"net/http"
)

func (b *Bridge) builderHandler() http.Handler {
	m := b.site
	return (&BuilderAPI{
		BrainForToken: m.brainForToken,
		Context:       func(id string) any { return m.BuilderContext(id) },
		Check:         func(ctx context.Context, id string) (any, error) { return m.BuilderCheck(ctx, id) },
		Preview:       func(ctx context.Context, id string) (any, error) { return m.BuilderPreview(ctx, id) },
		Sync:          m.BuilderSync,
		Propose: func(ctx context.Context, id, title, summary string) (any, any, error) {
			p, changes, err := m.BuilderPropose(ctx, id, title, summary)
			return p, changes, err
		},
	}).Handler()
}
