package app

import (
	"context"
	"fmt"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func backupOptions(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *brain.Brain, string) error) backup.Options {
	boot := cfgs.Boot()
	opts := backup.Options{BackupDir: boot.BackupDir(), RcloneConf: boot.RcloneConf(), Settings: func() backup.Settings { return BackupSettings(cfgs.Get()) },
		Targets: func() []backup.Target { return BackupTargets(bm.Brains(), notify) }, BridgeFiles: func() (backup.BridgeFiles, error) { return BackupFiles(cfgs) },
		FindRclone: findRclone, Environ: func(ctx context.Context) []string {
			return childEnv(helperScope(ctx), nil, "RCLONE_CONFIG="+boot.RcloneConf())
		},
		DesktopContext: withDesktop, InstallHint: rcloneInstallHint, RefusalText: outboundmsg.BackupRefusedAlert}
	if notify != nil {
		opts.Notify = func(ctx context.Context, target backup.Target, text string) error { return target.Notify(ctx, text) }
	}
	return opts
}

func newProjectedBackup(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *brain.Brain, string) error) *backup.Manager {
	m := backup.New(backupOptions(cfgs, bm, notify))
	// Publication succeeds before the schedule moves; failed saves never reach this callback.
	cfgs.Subscribe(func(prev, next *config.File) { m.SettingsChanged(BackupSettings(prev), BackupSettings(next)) })
	return m
}

// notifyBackup resolves the selected runtime's current reply destination.
func (b *App) notifyBackup(ctx context.Context, br *brain.Brain, text string) error {
	jid := b.replyChatFor(br, b.cfgs.Get())
	if jid == "" {
		return fmt.Errorf("brain %s has no chat to send it to — set its reply chat", br.ID())
	}
	return b.wa.SendMessage(ctx, jid, text)
}
