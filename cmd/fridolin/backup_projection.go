package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Icewater1337/fridolin/internal/config"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func backupSettings(cfg *config.Config) BackupSettings {
	bc := cfg.Backup
	s := BackupSettings{Enabled: bc.Enabled, Remote: bc.Remote, Root: bc.Root, DailyAt: bc.DailyAt, RclonePath: bc.RclonePath,
		KeepVersionsDays: bc.KeepVersions(), IncludeBridgeConfig: bc.IncludeBridge(), IncludeSecretFiles: bc.IncludeSecretFiles}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			s.ConfiguredIDs = append(s.ConfiguredIDs, ref.Approved.ID)
		}
	}
	return s
}

func backupTarget(br *Brain, notify func(context.Context, *Brain, string) error) BackupTarget {
	t := BackupTarget{ID: br.ID(), Dir: br.Dir(), WaitIdle: br.waitIdle, Settings: func() BackupTargetSettings {
		s := br.settings.Load()
		return BackupTargetSettings{Name: s.name, Enabled: s.manifest.Backup.Enabled, Exclude: slices.Clone(s.manifest.Backup.Exclude)}
	}}
	if notify != nil {
		t.Notify = func(ctx context.Context, text string) error { return notify(ctx, br, text) }
	}
	return t
}

func backupTargets(brains []*Brain, notify func(context.Context, *Brain, string) error) []BackupTarget {
	targets := make([]BackupTarget, 0, len(brains))
	for _, br := range brains {
		targets = append(targets, backupTarget(br, notify))
	}
	return targets
}

func backupFiles(cfgs *config.Store) (BackupBridgeFiles, error) {
	file := cfgs.FileCopy()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return BackupBridgeFiles{}, err
	}
	files := BackupBridgeFiles{Config: data}
	for _, ref := range file.BrainDirs {
		if ref.Approved == nil {
			continue
		}
		md, err := json.MarshalIndent(ref.Approved, "", "  ")
		files.Manifests = append(files.Manifests, BackupManifestFile{ID: ref.Approved.ID, Data: md, Err: err})
	}
	return files, nil
}

func backupOptions(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *Brain, string) error) BackupOptions {
	boot := cfgs.Boot()
	opts := BackupOptions{BackupDir: boot.BackupDir(), RcloneConf: boot.RcloneConf(), Settings: func() BackupSettings { return backupSettings(cfgs.Get()) },
		Targets: func() []BackupTarget { return backupTargets(bm.Brains(), notify) }, BridgeFiles: func() (BackupBridgeFiles, error) { return backupFiles(cfgs) },
		FindRclone: findRclone, Environ: func(ctx context.Context) []string {
			return childEnv(helperScope(ctx), nil, "RCLONE_CONFIG="+boot.RcloneConf())
		},
		DesktopContext: withDesktop, InstallHint: rcloneInstallHint, RefusalText: outboundmsg.BackupRefusedAlert}
	if notify != nil {
		opts.Notify = func(ctx context.Context, target BackupTarget, text string) error { return target.Notify(ctx, text) }
	}
	return opts
}

func newProjectedBackup(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *Brain, string) error) *BackupManager {
	m := newBackupManager(backupOptions(cfgs, bm, notify))
	// Publication succeeds before the schedule moves; failed saves never reach this callback.
	cfgs.Subscribe(func(prev, next *config.Config) { m.SettingsChanged(backupSettings(prev), backupSettings(next)) })
	return m
}

func NewBackupManager(cfgs *config.Store, bm *BrainManager) *BackupManager {
	return newProjectedBackup(cfgs, bm, nil)
}

// rcloneEnv retains the command test seam while core callers pass a context to
// the injected environment function.
func (m *BackupManager) rcloneEnv(scope childScope) []string {
	ctx := context.Background()
	if scope == desktopChild {
		ctx = withDesktop(ctx)
	}
	return m.opts.Environ(ctx)
}

// notifyBackup resolves the selected runtime's current reply destination.
func (b *Bridge) notifyBackup(ctx context.Context, br *Brain, text string) error {
	jid := b.replyChatFor(br, b.cfgs.Get())
	if jid == "" {
		return fmt.Errorf("brain %s has no chat to send it to — set its reply chat", br.ID())
	}
	return b.wa.SendMessage(ctx, jid, text)
}
