package app

import (
	"context"
	"time"
)

const requiredFilesEvery = 5 * time.Minute

func (b *App) Warnings() []string { return RequiredFileWarnings(b.cfgs.Get()) }

func (b *App) watchRequiredFiles(ctx context.Context, every time.Duration) {
	WatchRequiredFiles(ctx, every, RequiredFilesOptions{Config: b.cfgs.Get, OwnerChat: ownerChat, Notify: b.outbox.Text})
}
