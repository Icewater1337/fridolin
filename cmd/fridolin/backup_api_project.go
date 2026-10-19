package main

import "github.com/Icewater1337/fridolin/internal/config"

func (b *Bridge) newBackupAPI() *BackupAPI {
	return &BackupAPI{
		Backup:        func() BackupService { return b.backup },
		UpdateConfig:  func(update func(*config.Config) error) error { _, err := b.cfgs.Update("", update); return err },
		IncludeBridge: func() bool { return b.cfgs.Get().Backup.IncludeBridge() },
		UpdateBrain: func(id string, enabled *bool, exclude []string) error {
			return b.registry.UpdateBackup(id, enabled, exclude)
		},
		RestoreTarget: b.restoreTarget,
	}
}
