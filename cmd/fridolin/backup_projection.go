package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func backupSettings(cfg *config.Config) backup.Settings {
	bc := cfg.Backup
	s := backup.Settings{Enabled: bc.Enabled, Remote: bc.Remote, Root: bc.Root, DailyAt: bc.DailyAt, RclonePath: bc.RclonePath,
		KeepVersionsDays: bc.KeepVersions(), IncludeBridgeConfig: bc.IncludeBridge(), IncludeSecretFiles: bc.IncludeSecretFiles}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			s.ConfiguredIDs = append(s.ConfiguredIDs, ref.Approved.ID)
		}
	}
	return s
}

func backupTarget(br *brain.Brain, notify func(context.Context, *brain.Brain, string) error) backup.Target {
	t := backup.Target{ID: br.ID(), Dir: br.Dir(), WaitIdle: br.WaitIdle, Settings: func() backup.TargetSettings {
		m := br.Manifest()
		return backup.TargetSettings{Name: m.Name, Enabled: m.Backup.Enabled, Exclude: slices.Clone(m.Backup.Exclude)}
	}}
	if notify != nil {
		t.Notify = func(ctx context.Context, text string) error { return notify(ctx, br, text) }
	}
	return t
}

func backupTargets(brains []*brain.Brain, notify func(context.Context, *brain.Brain, string) error) []backup.Target {
	targets := make([]backup.Target, 0, len(brains))
	for _, br := range brains {
		targets = append(targets, backupTarget(br, notify))
	}
	return targets
}

func backupFiles(cfgs *config.Store) (backup.BridgeFiles, error) {
	file := cfgs.FileCopy()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return backup.BridgeFiles{}, err
	}
	files := backup.BridgeFiles{Config: data}
	for _, ref := range file.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		md, err := json.MarshalIndent(ref.Approved, "", "  ")
		files.Manifests = append(files.Manifests, backup.ManifestFile{ID: ref.Approved.ID, Data: md, Err: err})
	}
	return files, nil
}

func backupOptions(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *brain.Brain, string) error) backup.Options {
	boot := cfgs.Boot()
	opts := backup.Options{BackupDir: boot.BackupDir(), RcloneConf: boot.RcloneConf(), Settings: func() backup.Settings { return backupSettings(cfgs.Get()) },
		Targets: func() []backup.Target { return backupTargets(bm.Brains(), notify) }, BridgeFiles: func() (backup.BridgeFiles, error) { return backupFiles(cfgs) },
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
	cfgs.Subscribe(func(prev, next *config.Config) { m.SettingsChanged(backupSettings(prev), backupSettings(next)) })
	return m
}

// notifyBackup resolves the selected runtime's current reply destination.
func (b *Bridge) notifyBackup(ctx context.Context, br *brain.Brain, text string) error {
	jid := b.replyChatFor(br, b.cfgs.Get())
	if jid == "" {
		return fmt.Errorf("brain %s has no chat to send it to — set its reply chat", br.ID())
	}
	return b.wa.SendMessage(ctx, jid, text)
}
