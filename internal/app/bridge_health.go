package app

import (
	"context"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/startupcheck"
)

const requiredFilesEvery = 5 * time.Minute

func (b *App) Warnings() []string { return startupcheck.Warnings(startupCheckSettings(b.cfgs.Get())) }

func (b *App) watchRequiredFiles(ctx context.Context, every time.Duration) {
	startupcheck.Watch(ctx, every, startupcheck.Options{
		Settings: func() startupcheck.Settings { return startupCheckSettings(b.cfgs.Get()) },
		Send:     b.outbox.Text,
	})
}

// startupCheckSettings projects startup_checks for the startup check: the
// alert goes to notify_jid, else to the owner's chat.
func startupCheckSettings(cfg *config.Settings) startupcheck.Settings {
	chat := cfg.StartupChecks.NotifyJID
	if chat == "" {
		chat = ownerChat(cfg)
	}
	return startupcheck.Settings{
		RequiredFiles: cfg.StartupChecks.RequiredFiles,
		Hint:          cfg.StartupChecks.Hint,
		Notify:        cfg.StartupChecks.NotifyWhatsApp,
		Chat:          chat,
		MaxChunkSize:  cfg.MaxChunkSize,
	}
}
