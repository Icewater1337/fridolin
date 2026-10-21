package app

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/Icewater1337/fridolin/internal/backup"
)

// The Drive link's doctor check: rclone's shared Google Drive client is
// retired during 2026, and a remote that still signs in with it stops
// working then, taking the Drive backup and the vault's weekly copy along.
var driveDoctorChecks = []DoctorCheck{
	{ID: "drive-client", Title: "Google Drive link uses your own Google client", Run: checkDriveClient},
}

func init() { DoctorRegistry = append(DoctorRegistry, driveDoctorChecks...) }

// checkDriveClient warns while a Drive remote in the bridge's rclone
// config has no client_id, that is uses rclone's shared client.
func checkDriveClient(_ context.Context, env *DoctorEnv) DoctorResult {
	if env.Config == nil {
		return noConfig
	}
	data, err := env.ReadFile(env.Config.Boot.Layout().RcloneConf())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return DoctorResult{Status: DoctorOK, Detail: "Google Drive is not connected"}
	case err != nil:
		return DoctorResult{Status: DoctorWarn, Detail: "cannot read the rclone config: " + err.Error()}
	}
	shared := backup.SharedClientRemotes(string(data))
	if len(shared) == 0 {
		return DoctorResult{Status: DoctorOK, Detail: "no Drive remote uses rclone's shared client"}
	}
	return DoctorResult{Status: DoctorWarn,
		Detail: "the Drive remote " + strings.Join(shared, ", ") + " uses rclone's shared Google client, which Google retires during 2026; Drive backups stop then",
		Fix:    "Dashboard → Backup → Switch to your own Google client (docs/BACKUP.md, Use your own Google client)"}
}
