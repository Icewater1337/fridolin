package app

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) newBackupAPI() *httpapi.BackupAPI {
	return &httpapi.BackupAPI{
		Backup:        func() httpapi.BackupService { return b.backup },
		UpdateConfig:  func(update func(*config.File) error) error { _, err := b.cfgs.Update("", update); return err },
		IncludeBridge: func() bool { return b.cfgs.Get().Backup.IncludeBridge() },
		UpdateBrain: func(id string, enabled *bool, exclude []string) error {
			return b.registry.UpdateBackup(id, enabled, exclude)
		},
		RestoreTarget: b.restoreTarget,
	}
}
