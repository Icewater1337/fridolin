package main

import (
	"context"
	"github.com/Icewater1337/fridolin/internal/app"
	"time"
)

const requiredFilesEvery = 5 * time.Minute

func (b *Bridge) Warnings() []string { return app.RequiredFileWarnings(b.cfgs.Get()) }

func (b *Bridge) watchRequiredFiles(ctx context.Context, every time.Duration) {
	app.WatchRequiredFiles(ctx, every, app.RequiredFilesOptions{Config: b.cfgs.Get, OwnerChat: ownerChat, Notify: b.outbox.Text})
}
